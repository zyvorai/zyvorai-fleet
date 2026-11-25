// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package server

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zyvorai/fleet/internal/model"
)

// Rollout controls added after comparing AWS IoT Jobs, Eclipse hawkBit, Mender
// and Azure IoT Edge (see docs/RESEARCH.md): percentage failure budget, bounded
// per-site retries, a recurring daily window, manual wave advance and a
// convergence report. None of them gives the agent a new capability.

const (
	maxSiteRetries        = 10   // AWS IoT Jobs caps retries per device at 10
	maxRetryDelaySeconds  = 3600 // one hour
	maxConvergenceSiteIDs = 1000
)

// validateRolloutPolicy checks the percentage budget, retry and window fields.
func validateRolloutPolicy(in rolloutRequest) error {
	if in.MaxFailurePercent < 0 || in.MaxFailurePercent > 99 {
		return errors.New("maxFailurePercent must be between 0 and 99")
	}
	if in.SiteRetries < 0 || in.SiteRetries > maxSiteRetries {
		return fmt.Errorf("siteRetries must be between 0 and %d", maxSiteRetries)
	}
	if in.RetryDelaySeconds < 0 || in.RetryDelaySeconds > maxRetryDelaySeconds {
		return fmt.Errorf("retryDelaySeconds must be between 0 and %d", maxRetryDelaySeconds)
	}
	if in.RetryDelaySeconds > 0 && in.SiteRetries == 0 {
		return errors.New("retryDelaySeconds requires siteRetries")
	}
	if in.DailyWindow != "" {
		if in.Strategy == "all-at-once" {
			return errors.New("dailyWindow cannot be combined with the all-at-once strategy")
		}
		if _, _, err := parseDailyWindow(in.DailyWindow); err != nil {
			return err
		}
	}
	return nil
}

// parseDailyWindow parses "HH:MM-HH:MM" (UTC) into minutes after midnight.
// A window whose end is before its start wraps past midnight.
func parseDailyWindow(s string) (start, end int, err error) {
	bad := errors.New(`dailyWindow must look like "HH:MM-HH:MM" in UTC, for example "01:00-05:00"`)
	parts := strings.Split(s, "-")
	if len(parts) != 2 {
		return 0, 0, bad
	}
	parse := func(v string) (int, bool) {
		hm := strings.Split(strings.TrimSpace(v), ":")
		if len(hm) != 2 || len(hm[0]) != 2 || len(hm[1]) != 2 {
			return 0, false
		}
		h, e1 := strconv.Atoi(hm[0])
		m, e2 := strconv.Atoi(hm[1])
		if e1 != nil || e2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
			return 0, false
		}
		return h*60 + m, true
	}
	a, ok1 := parse(parts[0])
	b, ok2 := parse(parts[1])
	if !ok1 || !ok2 {
		return 0, 0, bad
	}
	if a == b {
		return 0, 0, errors.New("dailyWindow start and end must differ")
	}
	return a, b, nil
}

// inDailyWindow reports whether now (UTC) falls inside the window. An empty or
// unparsable window is always open so a bad stored value never freezes a rollout.
func inDailyWindow(spec string, now time.Time) bool {
	if spec == "" {
		return true
	}
	a, b, err := parseDailyWindow(spec)
	if err != nil {
		return true
	}
	u := now.UTC()
	m := u.Hour()*60 + u.Minute()
	if a < b {
		return m >= a && m < b
	}
	return m >= a || m < b
}

// failureBudgetExceeded applies the absolute and percentage failure budgets.
// The percentage is taken over all target sites; either budget stops the rollout.
func failureBudgetExceeded(ro *model.Rollout, failed int) bool {
	if failed > ro.MaxFailures {
		return true
	}
	if ro.MaxFailurePercent > 0 && len(ro.SiteIDs) > 0 && failed*100 > ro.MaxFailurePercent*len(ro.SiteIDs) {
		return true
	}
	return false
}

// applySiteRetries gives each failed, activated site up to SiteRetries more
// attempts, spaced by RetryDelaySeconds. Sites still waiting or just retried
// are removed from failed so they count against no budget yet. It returns
// whether anything changed.
func applySiteRetries(st *model.State, ro *model.Rollout, completed, failed map[string]bool, now time.Time) {
	if ro.SiteRetries <= 0 {
		return
	}
	activated := map[string]bool{}
	for _, id := range ro.ActivatedSites {
		activated[id] = true
	}
	delay := time.Duration(ro.RetryDelaySeconds) * time.Second
	for _, id := range ro.SiteIDs {
		if !failed[id] || !activated[id] || completed[id] || ro.Retries[id] >= ro.SiteRetries {
			continue
		}
		due, scheduled := ro.RetryAt[id]
		if !scheduled {
			due = now.Add(delay)
			if ro.RetryAt == nil {
				ro.RetryAt = map[string]time.Time{}
			}
			ro.RetryAt[id] = due
		}
		delete(failed, id)
		if due.After(now) {
			continue
		}
		delete(ro.RetryAt, id)
		if ro.Retries == nil {
			ro.Retries = map[string]int{}
		}
		ro.Retries[id]++
		if ro.ActivatedAt == nil {
			ro.ActivatedAt = map[string]time.Time{}
		}
		ro.ActivatedAt[id] = now
		delete(ro.HealthySince, id)
		for i := range st.Sites {
			if st.Sites[i].ID == id {
				retrySite(&st.Sites[i], ro.RevisionID)
				break
			}
		}
		keep := ro.TimedOutSites[:0]
		for _, t := range ro.TimedOutSites {
			if t != id {
				keep = append(keep, t)
			}
		}
		ro.TimedOutSites = keep
		st.Events = appendEvent(st.Events, model.Event{ID: newID("evt"), SiteID: id, Kind: "rollout.site_retry", Severity: "warning", Message: fmt.Sprintf("%s: retrying site (attempt %d of %d)", ro.Name, ro.Retries[id], ro.SiteRetries), CreatedAt: now})
	}
	// A retry that no longer applies (the site recovered) leaves no schedule behind.
	for id := range ro.RetryAt {
		if completed[id] {
			delete(ro.RetryAt, id)
		}
	}
}

// retrySite hands a site the revision again with a clean failure report.
func retrySite(site *model.Site, revisionID string) {
	site.FailedRevision = ""
	site.RevisionError = ""
	site.DesiredRevision = revisionID
	site.AwaitingSync = true
}

// advanceRollout starts the next wave immediately, ignoring the pause, the
// daily window and any still-running sites of the previous wave. This is the
// manual trigger hawkBit added for groups whose threshold is never met.
func advanceRollout(st *model.State, ro *model.Rollout, now time.Time) error {
	if ro.Status != "running" {
		return fmt.Errorf("rollout cannot be advanced from %s", ro.Status)
	}
	handled := map[string]bool{}
	for _, id := range ro.ActivatedSites {
		handled[id] = true
	}
	for _, id := range ro.CompletedSites {
		handled[id] = true
	}
	for _, id := range ro.FailedSites {
		handled[id] = true
	}
	remaining := 0
	for _, id := range ro.SiteIDs {
		if !handled[id] {
			remaining++
		}
	}
	if remaining == 0 {
		return errors.New("every site has already been activated")
	}
	if eligible, held := nextWaveCandidates(st, ro, now); len(eligible) == 0 && len(held) > 0 {
		return &conflictError{msg: "every site of the next wave is under a change freeze"}
	}
	ro.NextWaveAt = nil
	activateWaveNow(st, ro, now)
	refreshHold(st, ro, now)
	return nil
}

// ConvergenceBucket is one revision's fleet-wide state, modelled on Azure IoT
// Edge's Targeted / Applied / Reporting Success / Reporting Failure metrics.
type ConvergenceBucket struct {
	RevisionID string `json:"revisionId"`
	Name       string `json:"name,omitempty"`
	Digest     string `json:"digest,omitempty"`
	// Targeted sites have this revision as their desired revision.
	Targeted int `json:"targeted"`
	// Applied sites are targeted and report it applied.
	Applied int `json:"applied"`
	// Failed sites are targeted and report this revision failed.
	Failed int `json:"failed"`
	// Pending sites are targeted but neither applied nor failed yet.
	Pending int `json:"pending"`
	// Offline counts targeted sites that are not applied and are offline.
	Offline int `json:"offline"`
	// Stale sites still run this revision while desiring a different one.
	Stale int `json:"stale"`
	// ConvergedPercent is Applied/Targeted, 0 when nothing is targeted.
	ConvergedPercent float64             `json:"convergedPercent"`
	Sites            map[string][]string `json:"sites,omitempty"`
}

// ConvergenceReport is the response of GET /api/v1/convergence.
type ConvergenceReport struct {
	GeneratedAt time.Time           `json:"generatedAt"`
	TotalSites  int                 `json:"totalSites"`
	Unassigned  int                 `json:"unassigned"`
	Revisions   []ConvergenceBucket `json:"revisions"`
}

func buildConvergence(st model.State, onlyRevision string, withSites bool, now time.Time) ConvergenceReport {
	names := map[string]model.Revision{}
	for _, r := range st.Revisions {
		names[r.ID] = r
	}
	buckets := map[string]*ConvergenceBucket{}
	get := func(id string) *ConvergenceBucket {
		b := buckets[id]
		if b == nil {
			b = &ConvergenceBucket{RevisionID: id}
			if r, ok := names[id]; ok {
				b.Name = r.Name
				b.Digest = withDigest(r).Digest
			}
			if withSites {
				b.Sites = map[string][]string{"applied": {}, "failed": {}, "pending": {}, "offline": {}, "stale": {}}
			}
			buckets[id] = b
		}
		return b
	}
	add := func(b *ConvergenceBucket, kind, site string) {
		if b.Sites != nil && len(b.Sites[kind]) < maxConvergenceSiteIDs {
			b.Sites[kind] = append(b.Sites[kind], site)
		}
	}
	report := ConvergenceReport{GeneratedAt: now, TotalSites: len(st.Sites), Revisions: []ConvergenceBucket{}}
	sites := append([]model.Site(nil), st.Sites...)
	sort.Slice(sites, func(i, j int) bool { return sites[i].ID < sites[j].ID })
	for _, s := range sites {
		if s.DesiredRevision == "" {
			report.Unassigned++
		}
		if s.AppliedRevision != "" && s.AppliedRevision != s.DesiredRevision && (onlyRevision == "" || onlyRevision == s.AppliedRevision) {
			b := get(s.AppliedRevision)
			b.Stale++
			add(b, "stale", s.ID)
		}
		d := s.DesiredRevision
		if d == "" || (onlyRevision != "" && d != onlyRevision) {
			continue
		}
		b := get(d)
		b.Targeted++
		switch {
		case s.AppliedRevision == d:
			b.Applied++
			add(b, "applied", s.ID)
		case s.FailedRevision == d:
			b.Failed++
			add(b, "failed", s.ID)
		default:
			b.Pending++
			add(b, "pending", s.ID)
		}
		if s.AppliedRevision != d && s.Status == "offline" {
			b.Offline++
			add(b, "offline", s.ID)
		}
	}
	for _, b := range buckets {
		if b.Targeted > 0 {
			b.ConvergedPercent = float64(b.Applied*10000/b.Targeted) / 100
		}
		report.Revisions = append(report.Revisions, *b)
	}
	sort.Slice(report.Revisions, func(i, j int) bool {
		if report.Revisions[i].Targeted != report.Revisions[j].Targeted {
			return report.Revisions[i].Targeted > report.Revisions[j].Targeted
		}
		return report.Revisions[i].RevisionID < report.Revisions[j].RevisionID
	})
	return report
}

func (s *Server) convergence(w http.ResponseWriter, r *http.Request) {
	st := s.store.Snapshot()
	only := strings.TrimSpace(r.URL.Query().Get("revisionId"))
	if only != "" && !revisionExists(st, only) {
		writeError(w, http.StatusNotFound, "revision not found")
		return
	}
	writeJSON(w, 200, buildConvergence(st, only, only != "", time.Now().UTC()))
}
