// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package server

import (
	"net/http"
	"time"

	"github.com/zyvorai/fleet/internal/model"
)

// Per-site rollout status, the per-device view Azure Device Update and Memfault
// offer (see docs/RESEARCH.md). It is a read model over stored rollout and site
// fields; it records nothing new.

// RolloutSiteStatus is one target site of a rollout.
type RolloutSiteStatus struct {
	SiteID     string `json:"siteId"`
	Name       string `json:"name,omitempty"`
	SiteStatus string `json:"siteStatus,omitempty"`
	// State is one of completed, soaking, applying, retry_wait, failed,
	// timed_out, held, waiting or not_started.
	State        string     `json:"state"`
	Ring         string     `json:"ring,omitempty"`
	Retries      int        `json:"retries,omitempty"`
	ActivatedAt  *time.Time `json:"activatedAt,omitempty"`
	HealthySince *time.Time `json:"healthySince,omitempty"`
	RetryAt      *time.Time `json:"retryAt,omitempty"`
	LastSeen     *time.Time `json:"lastSeen,omitempty"`
	Error        string     `json:"error,omitempty"`
}

// RolloutSitesReport is the response of GET /api/v1/rollouts/{id}/sites.
type RolloutSitesReport struct {
	RolloutID  string              `json:"rolloutId"`
	Name       string              `json:"name"`
	Status     string              `json:"status"`
	RevisionID string              `json:"revisionId"`
	Counts     map[string]int      `json:"counts"`
	Sites      []RolloutSiteStatus `json:"sites"`
}

func timeIn(m map[string]time.Time, id string) *time.Time {
	if t, ok := m[id]; ok {
		return &t
	}
	return nil
}

func buildRolloutSites(st model.State, ro model.Rollout) RolloutSitesReport {
	sites := map[string]model.Site{}
	for _, s := range st.Sites {
		sites[s.ID] = s
	}
	completed, failed, activated := idSet(ro.CompletedSites), idSet(ro.FailedSites), idSet(ro.ActivatedSites)
	timedOut, held, soaking := idSet(ro.TimedOutSites), idSet(ro.HeldSites), idSet(ro.SoakingSites)
	rep := RolloutSitesReport{RolloutID: ro.ID, Name: ro.Name, Status: ro.Status, RevisionID: ro.RevisionID, Counts: map[string]int{}, Sites: []RolloutSiteStatus{}}
	for _, id := range ro.SiteIDs {
		row := RolloutSiteStatus{SiteID: id, Retries: ro.Retries[id], ActivatedAt: timeIn(ro.ActivatedAt, id), HealthySince: timeIn(ro.HealthySince, id), RetryAt: timeIn(ro.RetryAt, id)}
		if ring, ok := ro.SiteRings[id]; ok {
			if ring >= 0 && ring < len(ro.RingOrder) {
				row.Ring = ro.RingOrder[ring]
			} else {
				row.Ring = "(unlisted)"
			}
		}
		site, known := sites[id]
		if known {
			row.Name, row.SiteStatus = site.Name, site.Status
			if !site.LastSeen.IsZero() {
				seen := site.LastSeen
				row.LastSeen = &seen
			}
			if site.FailedRevision == ro.RevisionID {
				row.Error = site.RevisionError
			}
		}
		switch {
		case completed[id]:
			row.State = "completed"
		case failed[id] && timedOut[id]:
			row.State = "timed_out"
		case failed[id]:
			row.State = "failed"
		case row.RetryAt != nil:
			row.State = "retry_wait"
		case soaking[id]:
			row.State = "soaking"
		case activated[id]:
			row.State = "applying"
		case terminalRollout(ro.Status):
			row.State = "not_started"
		case held[id]:
			row.State = "held"
		default:
			row.State = "waiting"
		}
		rep.Counts[row.State]++
		rep.Sites = append(rep.Sites, row)
	}
	return rep
}

func (s *Server) rolloutSites(w http.ResponseWriter, r *http.Request) {
	s.markOfflineAndReconcile()
	st := s.store.Snapshot()
	for _, ro := range st.Rollouts {
		if ro.ID == r.PathValue("id") {
			writeJSON(w, 200, buildRolloutSites(st, ro))
			return
		}
	}
	writeError(w, http.StatusNotFound, "rollout not found")
}
