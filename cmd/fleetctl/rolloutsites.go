// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// formatRolloutSites renders GET /api/v1/rollouts/{id}/sites as a text table.
func formatRolloutSites(body []byte) (string, error) {
	var rep struct {
		RolloutID string         `json:"rolloutId"`
		Name      string         `json:"name"`
		Status    string         `json:"status"`
		Counts    map[string]int `json:"counts"`
		Sites     []struct {
			SiteID     string `json:"siteId"`
			Name       string `json:"name"`
			SiteStatus string `json:"siteStatus"`
			State      string `json:"state"`
			Ring       string `json:"ring"`
			Retries    int    `json:"retries"`
			Error      string `json:"error"`
		} `json:"sites"`
	}
	if err := json.Unmarshal(body, &rep); err != nil {
		return "", fmt.Errorf("unexpected rollout sites response: %w", err)
	}
	var b strings.Builder
	states := make([]string, 0, len(rep.Counts))
	for state := range rep.Counts {
		states = append(states, state)
	}
	sort.Strings(states)
	for i, state := range states {
		states[i] = fmt.Sprintf("%d %s", rep.Counts[state], state)
	}
	fmt.Fprintf(&b, "%s [%s] %s\n", rep.Name, rep.Status, strings.Join(states, ", "))
	fmt.Fprintf(&b, "%-24s %-12s %-9s %-12s %7s  %s\n", "SITE", "STATE", "AGENT", "RING", "RETRIES", "ERROR")
	for _, s := range rep.Sites {
		name := s.Name
		if name == "" {
			name = s.SiteID
		}
		ring := s.Ring
		if ring == "" {
			ring = "-"
		}
		fmt.Fprintf(&b, "%-24s %-12s %-9s %-12s %7d  %s\n", printable(name), s.State, s.SiteStatus, printable(ring), s.Retries, printable(s.Error))
	}
	return b.String(), nil
}

// printable drops control characters: site names and errors come from agents
// and must not drive the operator's terminal.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return ' '
		}
		return r
	}, s)
}
