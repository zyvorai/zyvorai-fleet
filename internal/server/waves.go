// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package server

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zyvorai/fleet/internal/model"
)

// Ring-ordered waves (Rancher Fleet partitions, GKE rollout stages, OCM
// decision groups) and the healthy soak (OCM minSuccessTime, GKE soak time).
// See docs/RESEARCH.md.

const (
	maxRings             = 20
	maxMinHealthySeconds = 86400
)

// validateRings checks the ring and soak fields of a rollout request.
func validateRings(in rolloutRequest) error {
	if in.MinHealthySeconds < 0 || in.MinHealthySeconds > maxMinHealthySeconds {
		return fmt.Errorf("minHealthySeconds must be between 0 and %d", maxMinHealthySeconds)
	}
	if in.RingLabel == "" && len(in.RingOrder) == 0 {
		return nil
	}
	if in.RingLabel == "" || len(in.RingOrder) == 0 {
		return errors.New("ringLabel and ringOrder must be set together")
	}
	if in.Strategy == "all-at-once" {
		return errors.New("ringOrder cannot be combined with the all-at-once strategy")
	}
	if _, err := normalizeLabels(map[string]string{in.RingLabel: ""}); err != nil || strings.TrimSpace(in.RingLabel) != in.RingLabel {
		return errors.New("ringLabel must be a valid label key")
	}
	if len(in.RingOrder) > maxRings {
		return fmt.Errorf("ringOrder may list at most %d rings", maxRings)
	}
	seen := map[string]bool{}
	for _, v := range in.RingOrder {
		if v == "" || len(v) > 128 || strings.TrimSpace(v) != v {
			return errors.New("ringOrder values must be non-empty label values of at most 128 characters")
		}
		if seen[v] {
			return fmt.Errorf("ringOrder lists %q twice", v)
		}
		seen[v] = true
	}
	return nil
}

// ringSummary describes one ring of a plan. Value is empty for the trailing
// ring of sites whose label is missing or not listed.
type ringSummary struct {
	Value    string   `json:"value"`
	Unlisted bool     `json:"unlisted,omitempty"`
	SiteIDs  []string `json:"siteIds"`
}

// assignRings orders targets by ring and returns each site's ring index. Sites
// without a listed value go last (index len(order)) so none is silently
// dropped. Ring membership is fixed here: relabelling a site later does not
// reorder a running rollout.
func assignRings(st model.State, targets []string, label string, order []string) (ordered []string, rings map[string]int, summary []ringSummary) {
	if label == "" {
		return targets, nil, nil
	}
	index := map[string]int{}
	for i, v := range order {
		index[v] = i
	}
	labels := map[string]map[string]string{}
	for _, site := range st.Sites {
		labels[site.ID] = site.Labels
	}
	rings = map[string]int{}
	for _, id := range targets {
		ring, ok := index[labels[id][label]]
		if !ok {
			ring = len(order)
		}
		rings[id] = ring
	}
	ordered = append([]string(nil), targets...)
	sort.SliceStable(ordered, func(i, j int) bool { return rings[ordered[i]] < rings[ordered[j]] })
	for ring := 0; ring <= len(order); ring++ {
		entry := ringSummary{SiteIDs: []string{}, Unlisted: ring == len(order)}
		if ring < len(order) {
			entry.Value = order[ring]
		}
		for _, id := range ordered {
			if rings[id] == ring {
				entry.SiteIDs = append(entry.SiteIDs, id)
			}
		}
		if !entry.Unlisted || len(entry.SiteIDs) > 0 {
			summary = append(summary, entry)
		}
	}
	return ordered, rings, summary
}

// ringWaveCount counts waves when no wave may span two rings. The canary sizes
// the very first wave only.
func ringWaveCount(summary []ringSummary, canary, wave int) int {
	total := 0
	for _, ring := range summary {
		if len(ring.SiteIDs) == 0 {
			continue
		}
		total += waveCount(len(ring.SiteIDs), canary, wave)
		canary = 0
	}
	return total
}

func idSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}

// nextWaveCandidates returns the not-yet-activated sites that may start in the
// next wave, and those of the same ring that a change freeze holds back. A
// later ring is never offered while the current one still has pending sites.
func nextWaveCandidates(st *model.State, ro *model.Rollout, now time.Time) (eligible, held []string) {
	done := idSet(ro.ActivatedSites)
	for _, id := range ro.CompletedSites {
		done[id] = true
	}
	for _, id := range ro.FailedSites {
		done[id] = true
	}
	exists := map[string]bool{}
	for _, site := range st.Sites {
		exists[site.ID] = true
	}
	frozen := map[string]bool{}
	if !ro.OverrideFreeze {
		frozen = frozenSites(st, now)
	}
	ring := -1
	if len(ro.SiteRings) > 0 {
		for _, id := range ro.SiteIDs {
			if !done[id] && exists[id] && (ring < 0 || ro.SiteRings[id] < ring) {
				ring = ro.SiteRings[id]
			}
		}
	}
	for _, id := range ro.SiteIDs {
		if done[id] || !exists[id] || (ring >= 0 && ro.SiteRings[id] != ring) {
			continue
		}
		if frozen[id] {
			held = append(held, id)
			continue
		}
		eligible = append(eligible, id)
	}
	return eligible, held
}

// refreshHold records which pending sites a freeze holds and announces, once,
// that a rollout is waiting on nothing but a freeze.
func refreshHold(st *model.State, ro *model.Rollout, now time.Time) (blocked bool) {
	if ro.Status != "running" {
		ro.HeldSites, ro.HeldByFreeze = nil, false
		return false
	}
	eligible, held := nextWaveCandidates(st, ro, now)
	ro.HeldSites = held
	blocked = len(eligible) == 0 && len(held) > 0 && activeRolloutSites(ro) == 0
	if blocked && !ro.HeldByFreeze {
		ro.UpdatedAt = now
		st.Events = appendEvent(st.Events, model.Event{ID: newID("evt"), Kind: "rollout.held", Severity: "warning", Message: fmt.Sprintf("%s is waiting: %d site(s) are under a change freeze", ro.Name, len(held)), CreatedAt: now})
	}
	ro.HeldByFreeze = blocked
	return blocked
}

// evaluateSites sorts a rollout's sites into completed, failed and soaking from
// what each site last reported.
//
// Without minHealthySeconds a site completes as soon as it reports the revision
// applied. With it, an activated site must stay applied without reporting the
// revision failed, and must be heard from at least that long after it was first
// seen healthy: silence is not evidence of health.
func evaluateSites(st *model.State, ro *model.Rollout, now time.Time) (completed, failed, soaking map[string]bool) {
	completed, failed, soaking = map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, id := range ro.FailedSites {
		failed[id] = true
	}
	targets := idSet(ro.SiteIDs)
	activated := idSet(ro.ActivatedSites)
	passed := idSet(ro.CompletedSites)
	soak := time.Duration(ro.MinHealthySeconds) * time.Second
	for i := range st.Sites {
		site := &st.Sites[i]
		id := site.ID
		if !targets[id] {
			continue
		}
		if soak <= 0 || !activated[id] || passed[id] {
			if site.AppliedRevision == ro.RevisionID {
				completed[id] = true
				delete(failed, id)
				continue
			}
			if site.FailedRevision == ro.RevisionID {
				failed[id] = true
			}
			continue
		}
		since, started := ro.HealthySince[id]
		switch {
		case site.FailedRevision == ro.RevisionID:
			if started {
				delete(ro.HealthySince, id)
				st.Events = appendEvent(st.Events, model.Event{ID: newID("evt"), SiteID: id, Kind: "rollout.site_unhealthy", Severity: "warning", Message: ro.Name + ": site became unhealthy during the soak period", CreatedAt: now})
			}
			failed[id] = true
		case site.AppliedRevision != ro.RevisionID || site.AwaitingSync:
			delete(ro.HealthySince, id)
		default:
			if !started {
				since = now
				if ro.HealthySince == nil {
					ro.HealthySince = map[string]time.Time{}
				}
				ro.HealthySince[id] = since
			}
			if !site.LastSeen.Before(since.Add(soak)) {
				completed[id] = true
				delete(failed, id)
				delete(ro.HealthySince, id)
			} else if !failed[id] {
				soaking[id] = true
			}
		}
	}
	return completed, failed, soaking
}
