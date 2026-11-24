// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package main

import (
	"strings"
	"testing"
)

func TestConvergencePath(t *testing.T) {
	if got := convergencePath(""); got != "/api/v1/convergence" {
		t.Fatal(got)
	}
	if got := convergencePath("rev a&b"); got != "/api/v1/convergence?revisionId=rev+a%26b" {
		t.Fatal(got)
	}
}

func TestFormatConvergence(t *testing.T) {
	body := []byte(`{"totalSites":4,"unassigned":1,"revisions":[{"revisionId":"rev_1","name":"v1","targeted":3,"applied":1,"failed":1,"pending":1,"offline":1,"stale":0,"convergedPercent":33.33,"sites":{"failed":["site_b"],"applied":[]}}]}`)
	out, err := formatConvergence(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"4 sites, 1 without", "TARGETED", "v1", "33.3%", "failed   site_b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "applied  ") {
		t.Fatalf("empty buckets must be omitted:\n%s", out)
	}
	empty, err := formatConvergence([]byte(`{"totalSites":0,"unassigned":0,"revisions":[]}`))
	if err != nil || !strings.Contains(empty, "no revision") {
		t.Fatalf("%q %v", empty, err)
	}
	if _, err := formatConvergence([]byte(`not json`)); err == nil {
		t.Fatal("bad body must error")
	}
}
