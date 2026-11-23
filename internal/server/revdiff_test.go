// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package server

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/fleet/internal/model"
)

func TestDiffWorkloads(t *testing.T) {
	a := model.RuntimeSpec{Kind: "container", Name: "api", State: "running", Image: "x:1", Env: map[string]string{"K": "secret1"}, Ports: []string{"80:80"}}
	cases := []struct {
		name                    string
		from, to                []model.RuntimeSpec
		added, removed, changed int
		fields                  []string
	}{
		{"identical", []model.RuntimeSpec{a}, []model.RuntimeSpec{a}, 0, 0, 0, nil},
		{"both empty", nil, nil, 0, 0, 0, nil},
		{"all added from nothing", nil, []model.RuntimeSpec{a}, 1, 0, 0, nil},
		{"all removed", []model.RuntimeSpec{a}, nil, 0, 1, 0, nil},
		{"image and state", []model.RuntimeSpec{a}, []model.RuntimeSpec{{Kind: "container", Name: "api", State: "stopped", Image: "x:2", Env: a.Env, Ports: a.Ports}}, 0, 0, 1, []string{"image", "state"}},
		{"env key added changed removed, no values", []model.RuntimeSpec{a}, []model.RuntimeSpec{{Kind: "container", Name: "api", State: "running", Image: "x:1", Ports: a.Ports, Env: map[string]string{"K": "secret2", "N": "v"}}}, 0, 0, 1, []string{"env.K", "env.N"}},
		{"same name different kind is distinct", []model.RuntimeSpec{{Kind: "systemd", Name: "api", State: "running"}}, []model.RuntimeSpec{{Kind: "container", Name: "api", State: "running", Image: "i"}}, 1, 1, 0, nil},
		{"nil vs empty slices equal", []model.RuntimeSpec{{Kind: "systemd", Name: "u", State: "running", Args: []string{}}}, []model.RuntimeSpec{{Kind: "systemd", Name: "u", State: "running"}}, 0, 0, 0, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			added, removed, changed := DiffWorkloads(tc.from, tc.to)
			if len(added) != tc.added || len(removed) != tc.removed || len(changed) != tc.changed {
				t.Fatalf("got +%d -%d ~%d", len(added), len(removed), len(changed))
			}
			if tc.fields != nil && !reflect.DeepEqual(changed[0].Fields, tc.fields) {
				t.Fatalf("fields %v want %v", changed[0].Fields, tc.fields)
			}
			for _, c := range changed {
				for _, f := range c.Fields {
					if strings.Contains(f, "secret") {
						t.Fatalf("diff leaked a value: %q", f)
					}
				}
			}
		})
	}
}

func TestDiffWorkloadsLargeInputIsBounded(t *testing.T) {
	var from, to []model.RuntimeSpec
	for i := 0; i < 100; i++ {
		from = append(from, model.RuntimeSpec{Kind: "systemd", Name: fmt.Sprintf("u%03d", i), State: "running"})
		to = append(to, model.RuntimeSpec{Kind: "systemd", Name: fmt.Sprintf("u%03d", i), State: "stopped"})
	}
	_, _, changed := DiffWorkloads(from, to)
	if len(changed) != 100 || changed[0].Name != "u000" || changed[99].Name != "u099" {
		t.Fatalf("expected 100 sorted changes, got %d", len(changed))
	}
}

func TestRevisionDiffAPIAndPlanChanges(t *testing.T) {
	e := newTestEnv(t)
	defer e.close()
	loginTestAdmin(t, e)
	v1 := createTestRevision(t, e, "v1")
	if !strings.HasPrefix(v1.Digest, model.DigestPrefix) {
		t.Fatalf("revision has no digest: %+v", v1)
	}
	code, body := e.req("POST", "/api/v1/revisions", map[string]any{"name": "v2", "workloads": []map[string]any{
		{"kind": "k3s", "name": "edge-config", "state": "present", "manifest": "changed"},
		{"kind": "systemd", "name": "chronyd", "state": "running"},
	}}, true)
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	v2 := decode[model.Revision](t, body)

	code, body = e.req("GET", "/api/v1/revisions/diff?from="+v1.ID+"&to="+v2.ID, nil, false)
	if code != 200 {
		t.Fatalf("diff %d %s", code, body)
	}
	d := decode[RevisionDiff](t, body)
	if d.Identical || len(d.Added) != 1 || len(d.Changed) != 1 || d.Changed[0].Fields[0] != "manifest" || d.FromDigest != v1.Digest {
		t.Fatalf("unexpected diff %+v", d)
	}
	code, body = e.req("GET", "/api/v1/revisions/diff?to="+v1.ID, nil, false)
	if d := decode[RevisionDiff](t, body); code != 200 || len(d.Added) != 1 {
		t.Fatalf("diff against nothing: %d %+v", code, d)
	}
	for name, q := range map[string]string{
		"missing to":   "from=" + v1.ID,
		"unknown to":   "to=nope",
		"unknown from": "from=nope&to=" + v1.ID,
		"oversize id":  "to=" + url.QueryEscape(strings.Repeat("a", 129)),
	} {
		if code, _ := e.req("GET", "/api/v1/revisions/diff?"+q, nil, false); code != 400 && code != 404 {
			t.Fatalf("%s: expected 4xx, got %d", name, code)
		}
	}

	// Plan: one site already on v1, one fresh.
	a := registerSite(t, e, "a", nil)
	registerSite(t, e, "b", nil)
	heartbeatRegistration(t, e, a, v1.ID, "", "")
	code, body = e.req("POST", "/api/v1/rollouts/plan", map[string]any{"revisionId": v2.ID, "waveSize": 1, "canarySize": 1}, true)
	if code != 200 {
		t.Fatalf("plan %d %s", code, body)
	}
	plan := decode[struct {
		Waves     int          `json:"waves"`
		Changes   []planChange `json:"changes"`
		Digest    string       `json:"revisionDigest"`
		Truncated bool         `json:"changesTruncated"`
	}](t, body)
	if plan.Waves != 2 || len(plan.Changes) != 2 || plan.Digest != v2.Digest || plan.Truncated {
		t.Fatalf("unexpected plan %+v", plan)
	}
	for _, c := range plan.Changes {
		if c.FromRevision == v1.ID && (c.Changed != 1 || c.Added != 1 || c.Sites != 1) {
			t.Fatalf("v1 change summary %+v", c)
		}
		if c.FromRevision == "" && (c.Added != 2 || c.Sites != 1) {
			t.Fatalf("fresh site summary %+v", c)
		}
	}
}

func TestRolloutRequestValidationAndDigestPin(t *testing.T) {
	e := newTestEnv(t)
	defer e.close()
	loginTestAdmin(t, e)
	rev := createTestRevision(t, e, "r")
	a := registerSite(t, e, "a", nil)
	b := registerSite(t, e, "b", nil)
	base := func(extra map[string]any) map[string]any {
		m := map[string]any{"revisionId": rev.ID, "siteIds": []string{a.SiteID, b.SiteID}, "waveSize": 1, "approvalRequired": true}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	for _, tc := range []struct {
		name string
		body map[string]any
		want int
	}{
		{"negative canary", base(map[string]any{"canarySize": -1}), 400},
		{"canary above targets", base(map[string]any{"canarySize": 3}), 400},
		{"canary with all-at-once", base(map[string]any{"canarySize": 1, "strategy": "all-at-once"}), 400},
		{"timeout too small", base(map[string]any{"siteTimeoutSeconds": 5}), 400},
		{"timeout too large", base(map[string]any{"siteTimeoutSeconds": 8 * 86400}), 400},
		{"negative timeout", base(map[string]any{"siteTimeoutSeconds": -1}), 400},
		{"wrong digest pin", base(map[string]any{"revisionDigest": "sha256:" + strings.Repeat("0", 64)}), 409},
		{"hostile digest pin", base(map[string]any{"revisionDigest": strings.Repeat("A", 100000)}), 409},
		{"valid with correct pin", base(map[string]any{"canarySize": 1, "siteTimeoutSeconds": 600, "revisionDigest": rev.Digest}), 201},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, body := e.req("POST", "/api/v1/rollouts", tc.body, true); code != tc.want {
				t.Fatalf("got %d want %d: %s", code, tc.want, body)
			}
		})
	}
}

func TestWaveCount(t *testing.T) {
	for _, tc := range []struct{ n, canary, wave, want int }{
		{0, 0, 5, 0}, {10, 0, 5, 2}, {11, 0, 5, 3}, {10, 1, 5, 3}, {10, 10, 5, 1}, {10, 2, 20, 2}, {1, 1, 1, 1}, {5, 0, 0, 0},
	} {
		if got := waveCount(tc.n, tc.canary, tc.wave); got != tc.want {
			t.Fatalf("waveCount(%d,%d,%d)=%d want %d", tc.n, tc.canary, tc.wave, got, tc.want)
		}
	}
}

func TestCanaryFirstWaveThenNormalWaves(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	st := model.State{
		Sites:    []model.Site{{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "d"}, {ID: "e"}},
		Rollouts: []model.Rollout{{ID: "r", Name: "r", RevisionID: "v", Status: "running", SiteIDs: []string{"a", "b", "c", "d", "e"}, WaveSize: 2, CanarySize: 1}},
	}
	desired := func() string {
		out := ""
		for _, s := range st.Sites {
			if s.DesiredRevision == "v" {
				out += s.ID
			}
		}
		return out
	}
	reconcileRolloutsAt(&st, t0)
	if desired() != "a" {
		t.Fatalf("canary wave should be exactly one site, got %q", desired())
	}
	st.Sites[0].AppliedRevision = "v"
	reconcileRolloutsAt(&st, t0.Add(time.Second))
	if desired() != "abc" {
		t.Fatalf("second wave should add two sites, got %q", desired())
	}
	st.Sites[1].AppliedRevision, st.Sites[2].AppliedRevision = "v", "v"
	reconcileRolloutsAt(&st, t0.Add(2*time.Second))
	st.Sites[3].AppliedRevision, st.Sites[4].AppliedRevision = "v", "v"
	reconcileRolloutsAt(&st, t0.Add(3*time.Second))
	if st.Rollouts[0].Status != "completed" || st.Rollouts[0].CurrentWave != 3 {
		t.Fatalf("expected completed after 3 waves, got %s wave %d", st.Rollouts[0].Status, st.Rollouts[0].CurrentWave)
	}
}

func timeoutFixture() (model.State, time.Time) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	return model.State{
		Sites:    []model.Site{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Rollouts: []model.Rollout{{ID: "r", Name: "r", RevisionID: "v", Status: "running", SiteIDs: []string{"a", "b", "c"}, WaveSize: 1, SiteTimeoutSeconds: 60, MaxFailures: 1}},
	}, t0
}

func TestSiteTimeoutFailsStuckSiteAndAdvances(t *testing.T) {
	st, t0 := timeoutFixture()
	reconcileRolloutsAt(&st, t0)
	reconcileRolloutsAt(&st, t0.Add(59*time.Second))
	if len(st.Rollouts[0].FailedSites) != 0 || st.Sites[1].DesiredRevision != "" {
		t.Fatal("timed out too early")
	}
	reconcileRolloutsAt(&st, t0.Add(61*time.Second))
	ro := st.Rollouts[0]
	if !reflect.DeepEqual(ro.FailedSites, []string{"a"}) || !reflect.DeepEqual(ro.TimedOutSites, []string{"a"}) {
		t.Fatalf("site a should be failed+timed out: %+v", ro)
	}
	if st.Sites[1].DesiredRevision != "v" || ro.Status != "running" {
		t.Fatalf("budget of 1 failure should let the rollout advance: %s %+v", ro.Status, st.Sites[1])
	}
	foundEvent := false
	for _, ev := range st.Events {
		foundEvent = foundEvent || ev.Kind == "rollout.site_timeout"
	}
	if !foundEvent {
		t.Fatal("no timeout event")
	}
	// Late recovery: site a applies the revision after all.
	st.Sites[0].AppliedRevision = "v"
	reconcileRolloutsAt(&st, t0.Add(62*time.Second))
	if ro := st.Rollouts[0]; len(ro.FailedSites) != 0 || len(ro.TimedOutSites) != 0 {
		t.Fatalf("late apply should clear the timeout: %+v", ro)
	}
}

func TestSiteTimeoutExceedingBudgetFailsRolloutAndRollsBack(t *testing.T) {
	st, t0 := timeoutFixture()
	st.Rollouts[0].MaxFailures = 0
	st.Rollouts[0].AutoRollback = true
	st.Rollouts[0].PreviousRevisions = map[string]string{"a": "old", "b": "old", "c": "old"}
	reconcileRolloutsAt(&st, t0)
	reconcileRolloutsAt(&st, t0.Add(2*time.Minute))
	if st.Rollouts[0].Status != "failed" || !st.Rollouts[0].RolledBack || st.Sites[0].DesiredRevision != "old" {
		t.Fatalf("expected failed + rolled back: %+v site=%+v", st.Rollouts[0], st.Sites[0])
	}
}

func TestSiteTimeoutDisabledAndPausedClockRestart(t *testing.T) {
	st, t0 := timeoutFixture()
	st.Rollouts[0].SiteTimeoutSeconds = 0
	reconcileRolloutsAt(&st, t0)
	reconcileRolloutsAt(&st, t0.Add(24*time.Hour))
	if len(st.Rollouts[0].FailedSites) != 0 {
		t.Fatal("timeout 0 must never fail a site")
	}

	st, t0 = timeoutFixture()
	reconcileRolloutsAt(&st, t0)
	ro := &st.Rollouts[0]
	ro.Status = "paused"
	reconcileRolloutsAt(&st, t0.Add(time.Hour)) // paused: nothing happens
	restartSiteTimers(ro, t0.Add(time.Hour))
	ro.Status = "running"
	reconcileRolloutsAt(&st, t0.Add(time.Hour+30*time.Second))
	if len(ro.FailedSites) != 0 {
		t.Fatal("time spent paused must not count against a site")
	}
	reconcileRolloutsAt(&st, t0.Add(time.Hour+61*time.Second))
	if len(ro.FailedSites) != 1 {
		t.Fatal("site should time out 60s after the clock restarted")
	}
}

var _ = http.StatusOK
