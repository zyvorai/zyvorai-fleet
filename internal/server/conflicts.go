// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package server

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zyvorai/fleet/internal/model"
)

// A site has one desired revision, so two unfinished rollouts that share a site
// overwrite each other. Azure Device Update allows one active deployment per
// device group and supersedes the older one (see docs/RESEARCH.md); Fleet
// rejects the overlap unless the request says how to resolve it.

const (
	conflictReject    = "reject"
	conflictSupersede = "supersede"
	conflictAllow     = "allow"
)

type rolloutConflict struct {
	RolloutID   string   `json:"rolloutId"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	SharedSites []string `json:"sharedSites"`
}

// conflictError is a request that is well formed but clashes with current
// state; it maps to HTTP 409.
type conflictError struct {
	msg       string
	conflicts []rolloutConflict
}

func (e *conflictError) Error() string { return e.msg }

func validateConflictPolicy(in rolloutRequest, now time.Time) error {
	switch in.ConflictPolicy {
	case "", conflictReject, conflictAllow:
		return nil
	case conflictSupersede:
		// Aborting the old rollout before the new one may start would leave
		// the sites with no rollout in charge of them.
		if in.ApprovalRequired || (in.StartAt != nil && in.StartAt.After(now)) {
			return errors.New("conflictPolicy supersede needs a rollout that starts immediately (no approvalRequired or future startAt)")
		}
		return nil
	default:
		return errors.New("conflictPolicy must be reject, supersede or allow")
	}
}

// findConflicts lists unfinished rollouts that share a target site and whose
// time window overlaps [startAt, endAt).
func findConflicts(st model.State, targets []string, startAt, endAt *time.Time, excludeID string, now time.Time) []rolloutConflict {
	wanted := idSet(targets)
	start := now
	if startAt != nil && startAt.After(now) {
		start = *startAt
	}
	out := []rolloutConflict{}
	for _, ro := range st.Rollouts {
		if ro.ID == excludeID || terminalRollout(ro.Status) {
			continue
		}
		if ro.EndAt != nil && !ro.EndAt.After(start) {
			continue // it ends before the new one begins
		}
		if endAt != nil && ro.StartAt != nil && !ro.StartAt.Before(*endAt) {
			continue // it begins after the new one ends
		}
		shared := []string{}
		for _, id := range ro.SiteIDs {
			if wanted[id] {
				shared = append(shared, id)
			}
		}
		if len(shared) > 0 {
			out = append(out, rolloutConflict{RolloutID: ro.ID, Name: ro.Name, Status: ro.Status, SharedSites: shared})
		}
	}
	return out
}

func newOverlapError(conflicts []rolloutConflict) *conflictError {
	ids := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		ids = append(ids, c.RolloutID)
	}
	return &conflictError{msg: fmt.Sprintf("target sites belong to unfinished rollout(s) %s; finish or abort them, or set conflictPolicy to supersede or allow", strings.Join(ids, ", ")), conflicts: conflicts}
}

// supersedeRollouts aborts the conflicting rollouts in favour of newRolloutID.
func supersedeRollouts(st *model.State, conflicts []rolloutConflict, newRolloutID string, now time.Time) {
	replaced := map[string]bool{}
	for _, c := range conflicts {
		replaced[c.RolloutID] = true
	}
	for i := range st.Rollouts {
		ro := &st.Rollouts[i]
		if !replaced[ro.ID] {
			continue
		}
		ro.Status = "aborted"
		ro.SupersededBy = newRolloutID
		ro.HeldSites, ro.HeldByFreeze, ro.SoakingSites = nil, false, nil
		ro.UpdatedAt = now
		st.Events = appendEvent(st.Events, model.Event{ID: newID("evt"), Kind: "rollout.superseded", Severity: "warning", Message: ro.Name + " aborted: superseded by a newer rollout", Details: map[string]string{"rolloutId": ro.ID, "supersededBy": newRolloutID}, CreatedAt: now})
	}
}
