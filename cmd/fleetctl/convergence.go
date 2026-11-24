// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// convergencePath builds the API path for `fleetctl convergence [REVISION_ID]`.
func convergencePath(revisionID string) string {
	if revisionID == "" {
		return "/api/v1/convergence"
	}
	return "/api/v1/convergence?revisionId=" + url.QueryEscape(revisionID)
}

type convergenceReport struct {
	TotalSites int `json:"totalSites"`
	Unassigned int `json:"unassigned"`
	Revisions  []struct {
		RevisionID       string              `json:"revisionId"`
		Name             string              `json:"name"`
		Targeted         int                 `json:"targeted"`
		Applied          int                 `json:"applied"`
		Failed           int                 `json:"failed"`
		Pending          int                 `json:"pending"`
		Offline          int                 `json:"offline"`
		Stale            int                 `json:"stale"`
		ConvergedPercent float64             `json:"convergedPercent"`
		Sites            map[string][]string `json:"sites"`
	} `json:"revisions"`
}

// formatConvergence renders the convergence API response as a text table.
func formatConvergence(body []byte) (string, error) {
	var rep convergenceReport
	if err := json.Unmarshal(body, &rep); err != nil {
		return "", fmt.Errorf("unexpected convergence response: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d sites, %d without a desired revision\n", rep.TotalSites, rep.Unassigned)
	if len(rep.Revisions) == 0 {
		b.WriteString("no revision is targeted or running\n")
		return b.String(), nil
	}
	fmt.Fprintf(&b, "%-28s %8s %8s %7s %8s %8s %6s %9s\n", "REVISION", "TARGETED", "APPLIED", "FAILED", "PENDING", "OFFLINE", "STALE", "CONVERGED")
	for _, r := range rep.Revisions {
		label := r.Name
		if label == "" {
			label = r.RevisionID
		}
		if len(label) > 28 {
			label = label[:27] + "…"
		}
		fmt.Fprintf(&b, "%-28s %8d %8d %7d %8d %8d %6d %8.1f%%\n", label, r.Targeted, r.Applied, r.Failed, r.Pending, r.Offline, r.Stale, r.ConvergedPercent)
		kinds := make([]string, 0, len(r.Sites))
		for k := range r.Sites {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			if ids := r.Sites[k]; len(ids) > 0 {
				fmt.Fprintf(&b, "  %-8s %s\n", k, strings.Join(ids, " "))
			}
		}
	}
	return b.String(), nil
}
