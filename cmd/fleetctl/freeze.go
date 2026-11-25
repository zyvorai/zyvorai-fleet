// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const freezeUsage = "freeze-create requires NAME HOURS all|group:ID|sites:ID[,ID]|key=value[,key=value] [reason]"

// freezeCreateBody builds the request for
// `fleetctl freeze-create NAME HOURS SCOPE [reason]`. The scope is always
// explicit so a missing argument cannot freeze the whole fleet.
func freezeCreateBody(args []string) (map[string]any, error) {
	if len(args) < 3 {
		return nil, errors.New(freezeUsage)
	}
	hours, err := strconv.Atoi(args[1])
	if err != nil || hours < 1 {
		return nil, fmt.Errorf("HOURS must be a positive whole number, got %q", args[1])
	}
	body := map[string]any{"name": args[0], "durationHours": hours}
	switch scope := args[2]; {
	case scope == "all":
		body["allSites"] = true
	case strings.HasPrefix(scope, "group:") && len(scope) > len("group:"):
		body["groupId"] = strings.TrimPrefix(scope, "group:")
	case strings.HasPrefix(scope, "sites:"):
		ids := []string{}
		for _, id := range strings.Split(strings.TrimPrefix(scope, "sites:"), ",") {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
		if len(ids) == 0 {
			return nil, errors.New("sites: needs at least one site id")
		}
		body["siteIds"] = ids
	default:
		selector := parsePairs(scope)
		// Reject a partly parsed selector: dropping one term widens the freeze.
		if len(selector) == 0 || len(selector) != len(strings.Split(scope, ",")) {
			return nil, fmt.Errorf("invalid scope %q; %s", scope, freezeUsage)
		}
		body["selector"] = selector
	}
	if len(args) > 3 {
		body["reason"] = strings.Join(args[3:], " ")
	}
	return body, nil
}

// formatFreezes renders GET /api/v1/freezes as a text table.
func formatFreezes(body []byte) (string, error) {
	var freezes []struct {
		ID              string    `json:"id"`
		Name            string    `json:"name"`
		Reason          string    `json:"reason"`
		StartAt         time.Time `json:"startAt"`
		EndAt           time.Time `json:"endAt"`
		Active          bool      `json:"active"`
		Expired         bool      `json:"expired"`
		ResolvedSiteIDs []string  `json:"resolvedSiteIds"`
	}
	if err := json.Unmarshal(body, &freezes); err != nil {
		return "", fmt.Errorf("unexpected freeze response: %w", err)
	}
	if len(freezes) == 0 {
		return "no change freezes\n", nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-20s %-10s %-20s %-20s %5s  %s\n", "ID", "STATE", "START (UTC)", "END (UTC)", "SITES", "NAME")
	for _, f := range freezes {
		state := "pending"
		if f.Active {
			state = "active"
		} else if f.Expired {
			state = "expired"
		}
		name := f.Name
		if f.Reason != "" {
			name += " (" + f.Reason + ")"
		}
		fmt.Fprintf(&b, "%-20s %-10s %-20s %-20s %5d  %s\n", f.ID, state, f.StartAt.UTC().Format(time.RFC3339), f.EndAt.UTC().Format(time.RFC3339), len(f.ResolvedSiteIDs), name)
	}
	return b.String(), nil
}
