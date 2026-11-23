// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package model

import (
	"strings"
	"testing"
)

func TestComputeRevisionDigest(t *testing.T) {
	base := []RuntimeSpec{{Kind: "systemd", Name: "chronyd", State: "running"}, {Kind: "container", Name: "api", State: "running", Image: "x:1", Env: map[string]string{"B": "2", "A": "1"}}}
	same := []RuntimeSpec{{Kind: "systemd", Name: "chronyd", State: "running"}, {Kind: "container", Name: "api", State: "running", Image: "x:1", Env: map[string]string{"A": "1", "B": "2"}}}
	cases := []struct {
		name      string
		other     []RuntimeSpec
		wantEqual bool
	}{
		{"map order is irrelevant", same, true},
		{"image change", []RuntimeSpec{base[0], {Kind: "container", Name: "api", State: "running", Image: "x:2", Env: base[1].Env}}, false},
		{"env value change", []RuntimeSpec{base[0], {Kind: "container", Name: "api", State: "running", Image: "x:1", Env: map[string]string{"A": "1", "B": "3"}}}, false},
		{"workload order matters", []RuntimeSpec{base[1], base[0]}, false},
		{"workload removed", base[:1], false},
		{"empty differs", nil, false},
	}
	want := ComputeRevisionDigest(base)
	if !strings.HasPrefix(want, DigestPrefix) || len(want) != len(DigestPrefix)+64 {
		t.Fatalf("unexpected digest shape %q", want)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ComputeRevisionDigest(tc.other); (got == want) != tc.wantEqual {
				t.Fatalf("equal=%v want %v", got == want, tc.wantEqual)
			}
		})
	}
	if ComputeRevisionDigest(nil) != ComputeRevisionDigest([]RuntimeSpec{}) {
		t.Fatal("nil and empty workloads must hash identically")
	}
}

func TestRevisionVerifyDigest(t *testing.T) {
	w := []RuntimeSpec{{Kind: "systemd", Name: "chronyd", State: "running"}}
	good := Revision{ID: "r", Name: "n", Workloads: w, Digest: ComputeRevisionDigest(w)}
	renamed := good
	renamed.Name, renamed.Notes = "other", "notes"
	tampered := good
	tampered.Workloads = []RuntimeSpec{{Kind: "systemd", Name: "evil", State: "running"}}
	for _, tc := range []struct {
		name        string
		rev         Revision
		present, ok bool
	}{
		{"legacy without digest", Revision{ID: "r", Workloads: w}, false, true},
		{"valid", good, true, true},
		{"renaming does not change digest", renamed, true, true},
		{"tampered workloads", tampered, true, false},
		{"garbage digest", Revision{Workloads: w, Digest: "sha256:zz"}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if p, ok := tc.rev.VerifyDigest(); p != tc.present || ok != tc.ok {
				t.Fatalf("got present=%v ok=%v", p, ok)
			}
		})
	}
}
