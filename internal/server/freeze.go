// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package server

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/zyvorai/fleet/internal/model"
)

// Change freezes follow Octopus Deploy deployment freezes and GKE maintenance
// exclusions (see docs/RESEARCH.md): for a bounded period no rollout moves a
// covered site to a new revision. Sites keep their current desired state, and
// rollback and abort stay available.

const (
	maxFreezes        = 200
	maxFreezeDuration = 366 * 24 * time.Hour
	maxFreezeReason   = 240
)

func freezeActive(f model.Freeze, now time.Time) bool {
	return !f.StartAt.After(now) && f.EndAt.After(now)
}

// freezeSiteIDs resolves a freeze scope against the current fleet, so sites
// that gain a matching label during the freeze are covered too.
func freezeSiteIDs(st *model.State, f model.Freeze) []string {
	if f.AllSites {
		ids := make([]string, 0, len(st.Sites))
		for _, site := range st.Sites {
			ids = append(ids, site.ID)
		}
		return ids
	}
	ids := append([]string(nil), f.SiteIDs...)
	if f.GroupID != "" {
		for _, g := range st.SiteGroups {
			if g.ID == f.GroupID {
				ids = append(ids, resolveGroupSites(*st, g)...)
				break
			}
		}
	}
	if len(f.Selector) > 0 {
		for _, site := range st.Sites {
			if labelsMatch(site.Labels, f.Selector) {
				ids = append(ids, site.ID)
			}
		}
	}
	return unique(ids)
}

// frozenSites returns the set of sites covered by any freeze active at now.
func frozenSites(st *model.State, now time.Time) map[string]bool {
	out := map[string]bool{}
	for _, f := range st.Freezes {
		if !freezeActive(f, now) {
			continue
		}
		for _, id := range freezeSiteIDs(st, f) {
			out[id] = true
		}
	}
	return out
}

type freezeRequest struct {
	Name          string            `json:"name"`
	Reason        string            `json:"reason"`
	StartAt       *time.Time        `json:"startAt"`
	EndAt         *time.Time        `json:"endAt"`
	DurationHours int               `json:"durationHours"`
	AllSites      bool              `json:"allSites"`
	SiteIDs       []string          `json:"siteIds"`
	GroupID       string            `json:"groupId"`
	Selector      map[string]string `json:"selector"`
}

// buildFreeze validates a request against the current state.
func buildFreeze(st model.State, in freezeRequest, now time.Time) (model.Freeze, error) {
	var f model.Freeze
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 80 {
		return f, errors.New("name must be 1 to 80 characters")
	}
	reason := strings.TrimSpace(in.Reason)
	if len(reason) > maxFreezeReason {
		return f, fmt.Errorf("reason must be at most %d characters", maxFreezeReason)
	}
	start := now
	if in.StartAt != nil {
		start = in.StartAt.UTC()
	}
	var end time.Time
	switch {
	case in.EndAt != nil && in.DurationHours != 0:
		return f, errors.New("set either endAt or durationHours, not both")
	case in.EndAt != nil:
		end = in.EndAt.UTC()
	case in.DurationHours > 0 && in.DurationHours <= int(maxFreezeDuration/time.Hour):
		end = start.Add(time.Duration(in.DurationHours) * time.Hour)
	case in.DurationHours != 0:
		return f, fmt.Errorf("durationHours must be between 1 and %d", int(maxFreezeDuration/time.Hour))
	default:
		return f, errors.New("endAt or durationHours is required")
	}
	if !end.After(start) {
		return f, errors.New("endAt must be after startAt")
	}
	if !end.After(now) {
		return f, errors.New("endAt must be in the future")
	}
	if end.Sub(start) > maxFreezeDuration {
		return f, errors.New("a freeze may last at most 366 days")
	}
	selector, err := normalizeLabels(in.Selector)
	if err != nil {
		return f, err
	}
	scoped := len(in.SiteIDs) > 0 || in.GroupID != "" || len(selector) > 0
	// An empty scope must be asked for explicitly: forgetting the selector must
	// not silently freeze the whole fleet.
	if in.AllSites == scoped {
		return f, errors.New("set allSites, or at least one of siteIds, groupId and selector (not both)")
	}
	known := map[string]bool{}
	for _, site := range st.Sites {
		known[site.ID] = true
	}
	for _, id := range in.SiteIDs {
		if !known[id] {
			return f, fmt.Errorf("unknown site: %s", id)
		}
	}
	if in.GroupID != "" {
		found := false
		for _, g := range st.SiteGroups {
			found = found || g.ID == in.GroupID
		}
		if !found {
			return f, errors.New("site group not found")
		}
	}
	return model.Freeze{ID: newID("frz"), Name: name, Reason: reason, StartAt: start, EndAt: end, AllSites: in.AllSites, SiteIDs: unique(in.SiteIDs), GroupID: in.GroupID, Selector: selector, CreatedAt: now}, nil
}

type freezeView struct {
	model.Freeze
	Active          bool     `json:"active"`
	Expired         bool     `json:"expired"`
	ResolvedSiteIDs []string `json:"resolvedSiteIds"`
}

func viewFreeze(st *model.State, f model.Freeze, now time.Time) freezeView {
	return freezeView{Freeze: f, Active: freezeActive(f, now), Expired: !f.EndAt.After(now), ResolvedSiteIDs: freezeSiteIDs(st, f)}
}

func (s *Server) listFreezes(w http.ResponseWriter, _ *http.Request) {
	st := s.store.Snapshot()
	now := time.Now().UTC()
	out := make([]freezeView, 0, len(st.Freezes))
	for _, f := range st.Freezes {
		out = append(out, viewFreeze(&st, f, now))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartAt.Before(out[j].StartAt) })
	writeJSON(w, 200, out)
}

func (s *Server) createFreeze(w http.ResponseWriter, r *http.Request) {
	var in freezeRequest
	if err := decodeJSON(r, &in, 64<<10); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	now := time.Now().UTC()
	actor := claimsFrom(r.Context())
	var out freezeView
	var invalid error
	err := s.store.Update(func(st *model.State) error {
		f, err := buildFreeze(*st, in, now)
		if err != nil {
			invalid = err
			return err
		}
		// Expired freezes are history; drop them first when the list is full.
		if len(st.Freezes) >= maxFreezes {
			keep := st.Freezes[:0]
			for _, old := range st.Freezes {
				if old.EndAt.After(now) {
					keep = append(keep, old)
				}
			}
			st.Freezes = keep
		}
		if len(st.Freezes) >= maxFreezes {
			invalid = fmt.Errorf("at most %d freezes may be pending or active", maxFreezes)
			return invalid
		}
		f.CreatedBy = actor.Email
		st.Freezes = append(st.Freezes, f)
		st.Events = appendEvent(st.Events, model.Event{ID: newID("evt"), Kind: "freeze.created", Severity: "warning", Message: fmt.Sprintf("Change freeze %s from %s to %s", f.Name, f.StartAt.Format(time.RFC3339), f.EndAt.Format(time.RFC3339)), Details: map[string]string{"actor": actor.Email, "freezeId": f.ID}, CreatedAt: now})
		out = viewFreeze(st, f, now)
		return nil
	})
	if invalid != nil {
		writeError(w, 400, invalid.Error())
		return
	}
	if err != nil {
		writeError(w, 500, "could not save freeze")
		return
	}
	writeJSON(w, 201, out)
}

func (s *Server) deleteFreeze(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	actor := claimsFrom(r.Context())
	now := time.Now().UTC()
	found := false
	err := s.store.Update(func(st *model.State) error {
		keep := st.Freezes[:0]
		for _, f := range st.Freezes {
			if f.ID == id {
				found = true
				st.Events = appendEvent(st.Events, model.Event{ID: newID("evt"), Kind: "freeze.deleted", Severity: "info", Message: "Change freeze " + f.Name + " lifted", Details: map[string]string{"actor": actor.Email, "freezeId": f.ID}, CreatedAt: now})
				continue
			}
			keep = append(keep, f)
		}
		st.Freezes = keep
		if found {
			// Held rollouts resume right away instead of on the next agent poll.
			reconcileRolloutsAt(st, now)
		}
		return nil
	})
	if err != nil {
		writeError(w, 500, "could not delete freeze")
		return
	}
	if !found {
		writeError(w, 404, "freeze not found")
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}
