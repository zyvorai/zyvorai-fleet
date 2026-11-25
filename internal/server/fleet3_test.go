// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/fleet/internal/model"
)

var t0 = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func soakFixture(soak int) model.State {
	return model.State{
		Sites:    []model.Site{{ID: "a", LastSeen: t0}, {ID: "b", LastSeen: t0}},
		Rollouts: []model.Rollout{{ID: "r", Name: "r", RevisionID: "v", Status: "running", SiteIDs: []string{"a", "b"}, WaveSize: 1, MinHealthySeconds: soak}},
	}
}

// report mimics a heartbeat from site i at the given time.
func report(st *model.State, i int, at time.Time, applied, failed string) {
	st.Sites[i].LastSeen, st.Sites[i].AppliedRevision, st.Sites[i].FailedRevision = at, applied, failed
	reconcileRolloutsAt(st, at)
}

func TestSoakHoldsNextWaveUntilConfirmedHealthy(t *testing.T) {
	st := soakFixture(60)
	reconcileRolloutsAt(&st, t0)
	ro := &st.Rollouts[0]
	report(&st, 0, t0.Add(time.Second), "v", "")
	if len(ro.CompletedSites) != 0 || !reflect.DeepEqual(ro.SoakingSites, []string{"a"}) || st.Sites[1].DesiredRevision != "" {
		t.Fatalf("applied site must soak and block the next wave: %+v", ro)
	}
	// Time passing without a report proves nothing.
	reconcileRolloutsAt(&st, t0.Add(10*time.Minute))
	if len(ro.CompletedSites) != 0 || st.Sites[1].DesiredRevision != "" {
		t.Fatalf("a silent site must not be counted healthy: %+v", ro)
	}
	report(&st, 0, t0.Add(60*time.Second), "v", "") // 59s after first healthy
	if len(ro.CompletedSites) != 0 {
		t.Fatal("soak is not over yet")
	}
	report(&st, 0, t0.Add(61*time.Second), "v", "")
	if !reflect.DeepEqual(ro.CompletedSites, []string{"a"}) || len(ro.SoakingSites) != 0 || st.Sites[1].DesiredRevision != "v" {
		t.Fatalf("soak over: complete and start the next wave: %+v", ro)
	}
	report(&st, 1, t0.Add(70*time.Second), "v", "")
	report(&st, 1, t0.Add(131*time.Second), "v", "")
	if ro.Status != "completed" {
		t.Fatalf("status %s", ro.Status)
	}
}

func TestSoakFailureCountsAgainstBudget(t *testing.T) {
	st := soakFixture(60)
	reconcileRolloutsAt(&st, t0)
	report(&st, 0, t0.Add(time.Second), "v", "")
	// The agent keeps reporting the revision applied while its health check fails.
	report(&st, 0, t0.Add(30*time.Second), "v", "v")
	ro := st.Rollouts[0]
	if ro.Status != "failed" || !reflect.DeepEqual(ro.FailedSites, []string{"a"}) || st.Sites[1].DesiredRevision != "" {
		t.Fatalf("unhealthy during soak must fail: %+v", ro)
	}
	if countEvents(st, "rollout.site_unhealthy") != 1 {
		t.Fatalf("want one unhealthy event, got %d", countEvents(st, "rollout.site_unhealthy"))
	}
}

// Without a soak, a failure after the apply keeps the site completed, as before.
func TestNoSoakKeepsApplyAsCompletion(t *testing.T) {
	st := soakFixture(0)
	reconcileRolloutsAt(&st, t0)
	report(&st, 0, t0.Add(time.Second), "v", "")
	report(&st, 0, t0.Add(2*time.Second), "v", "v")
	if ro := st.Rollouts[0]; ro.Status != "running" || !reflect.DeepEqual(ro.CompletedSites, []string{"a"}) {
		t.Fatalf("%+v", ro)
	}
}

func TestSoakRecoveryNeedsAFullNewPeriod(t *testing.T) {
	st := soakFixture(60)
	st.Rollouts[0].MaxFailures = 1
	st.Rollouts[0].WaveSize = 2
	reconcileRolloutsAt(&st, t0)
	report(&st, 0, t0.Add(time.Second), "v", "")
	report(&st, 0, t0.Add(30*time.Second), "v", "v")
	report(&st, 0, t0.Add(40*time.Second), "v", "") // healthy again
	ro := &st.Rollouts[0]
	if !reflect.DeepEqual(ro.FailedSites, []string{"a"}) || len(ro.CompletedSites) != 0 {
		t.Fatalf("recovered site stays failed until it soaks again: %+v", ro)
	}
	report(&st, 0, t0.Add(99*time.Second), "v", "")
	if len(ro.CompletedSites) != 0 {
		t.Fatal("the period restarts after a failure")
	}
	report(&st, 0, t0.Add(100*time.Second), "v", "")
	if !reflect.DeepEqual(ro.CompletedSites, []string{"a"}) || len(ro.FailedSites) != 0 {
		t.Fatalf("%+v", ro)
	}
}

func TestSoakTimeoutForSilentSite(t *testing.T) {
	st := soakFixture(60)
	st.Rollouts[0].SiteTimeoutSeconds = 30
	st.Rollouts[0].MaxFailures = 1
	st.Rollouts[0].WaveSize = 2
	reconcileRolloutsAt(&st, t0)
	report(&st, 0, t0.Add(20*time.Second), "v", "")
	report(&st, 1, t0.Add(20*time.Second), "v", "")
	report(&st, 1, t0.Add(85*time.Second), "v", "")
	ro := &st.Rollouts[0]
	// Past the apply timeout, but a soaking site has soak+timeout to confirm.
	reconcileRolloutsAt(&st, t0.Add(100*time.Second))
	if len(ro.FailedSites) != 0 {
		t.Fatalf("too early: %+v", ro)
	}
	for _, after := range []time.Duration{111, 112, 113} {
		reconcileRolloutsAt(&st, t0.Add(after*time.Second))
	}
	if !reflect.DeepEqual(ro.FailedSites, []string{"a"}) || !reflect.DeepEqual(ro.TimedOutSites, []string{"a"}) || countEvents(st, "rollout.site_timeout") != 1 {
		t.Fatalf("silent site should time out exactly once: %+v events=%d", ro, countEvents(st, "rollout.site_timeout"))
	}
}

func TestSoakRetryWaitsForSyncBeforeTrustingReports(t *testing.T) {
	st := soakFixture(60)
	st.Rollouts[0].SiteRetries = 1
	reconcileRolloutsAt(&st, t0)
	report(&st, 0, t0.Add(time.Second), "v", "")
	report(&st, 0, t0.Add(2*time.Second), "v", "v")
	ro := &st.Rollouts[0]
	if ro.Retries["a"] != 1 || !st.Sites[0].AwaitingSync {
		t.Fatalf("%+v %+v", ro, st.Sites[0])
	}
	report(&st, 0, t0.Add(100*time.Second), "v", "")
	if len(ro.CompletedSites) != 0 || len(ro.HealthySince) != 0 {
		t.Fatalf("reports before the post-retry sync must not count as healthy: %+v", ro)
	}
	st.Sites[0].AwaitingSync = false
	report(&st, 0, t0.Add(101*time.Second), "v", "")
	report(&st, 0, t0.Add(161*time.Second), "v", "")
	if !reflect.DeepEqual(ro.CompletedSites, []string{"a"}) {
		t.Fatalf("%+v", ro)
	}
}

func freezeFixture() model.State {
	return model.State{
		Sites:    []model.Site{{ID: "a", Labels: map[string]string{"env": "prod"}}, {ID: "b"}, {ID: "c"}},
		Rollouts: []model.Rollout{{ID: "r", Name: "r", RevisionID: "v", Status: "running", SiteIDs: []string{"a", "b", "c"}, WaveSize: 1, PauseSeconds: 0}},
		Freezes:  []model.Freeze{{ID: "f", Name: "f", StartAt: t0.Add(-time.Hour), EndAt: t0.Add(time.Hour), Selector: map[string]string{"env": "prod"}}},
	}
}

func TestFreezeSkipsThenHoldsThenReleases(t *testing.T) {
	st := freezeFixture()
	reconcileRolloutsAt(&st, t0)
	ro := &st.Rollouts[0]
	if st.Sites[0].DesiredRevision != "" || st.Sites[1].DesiredRevision != "v" || ro.HeldByFreeze || !reflect.DeepEqual(ro.HeldSites, []string{"a"}) {
		t.Fatalf("frozen site skipped, next one takes the slot: %+v", ro)
	}
	report(&st, 1, t0.Add(time.Second), "v", "")
	report(&st, 2, t0.Add(2*time.Second), "v", "")
	for i := 3; i < 6; i++ {
		reconcileRolloutsAt(&st, t0.Add(time.Duration(i)*time.Second))
	}
	if ro.Status != "running" || !ro.HeldByFreeze || st.Sites[0].DesiredRevision != "" || countEvents(st, "rollout.held") != 1 {
		t.Fatalf("rollout should wait, announcing once: %+v events=%d", ro, countEvents(st, "rollout.held"))
	}
	reconcileRolloutsAt(&st, t0.Add(time.Hour)) // end is exclusive
	if st.Sites[0].DesiredRevision != "v" || ro.HeldByFreeze || len(ro.HeldSites) != 0 {
		t.Fatalf("freeze over: %+v", ro)
	}
}

func TestFreezeTimingScopeAndOverride(t *testing.T) {
	st := freezeFixture()
	if got := frozenSites(&st, t0.Add(-2*time.Hour)); len(got) != 0 {
		t.Fatalf("not started: %v", got)
	}
	if got := frozenSites(&st, t0.Add(-time.Hour)); !got["a"] || got["b"] {
		t.Fatalf("start is inclusive and selector scoped: %v", got)
	}
	st.SiteGroups = []model.SiteGroup{{ID: "g", SiteIDs: []string{"c"}}}
	st.Freezes = []model.Freeze{{StartAt: t0, EndAt: t0.Add(time.Hour), GroupID: "g", SiteIDs: []string{"b"}}}
	if got := frozenSites(&st, t0); got["a"] || !got["b"] || !got["c"] {
		t.Fatalf("union of group and ids: %v", got)
	}
	st.Freezes = []model.Freeze{{StartAt: t0, EndAt: t0.Add(time.Hour), AllSites: true}}
	st.Rollouts[0].OverrideFreeze = true
	reconcileRolloutsAt(&st, t0)
	if st.Sites[0].DesiredRevision != "v" {
		t.Fatal("override must ignore freezes")
	}
}

func ringFixture() model.State {
	site := func(id, ring string) model.Site { return model.Site{ID: id, Labels: map[string]string{"ring": ring}} }
	return model.State{Sites: []model.Site{site("p1", "prod"), site("c1", "canary"), {ID: "x"}, site("c2", "canary"), site("p2", "prod")}}
}

func TestAssignRingsAndWaveCount(t *testing.T) {
	st := ringFixture()
	ordered, rings, summary := assignRings(st, []string{"p1", "c1", "x", "c2", "p2"}, "ring", []string{"canary", "empty", "prod"})
	if !reflect.DeepEqual(ordered, []string{"c1", "c2", "p1", "p2", "x"}) {
		t.Fatal(ordered)
	}
	if rings["c1"] != 0 || rings["p1"] != 2 || rings["x"] != 3 {
		t.Fatal(rings)
	}
	if len(summary) != 4 || summary[1].Value != "empty" || len(summary[1].SiteIDs) != 0 || !summary[3].Unlisted {
		t.Fatalf("%+v", summary)
	}
	if got := ringWaveCount(summary, 0, 2); got != 3 {
		t.Fatalf("waves %d", got)
	}
	if got := ringWaveCount(summary, 1, 2); got != 4 {
		t.Fatalf("canary only sizes the first wave: %d", got)
	}
	if got, r, s := assignRings(st, []string{"p1"}, "", nil); len(got) != 1 || r != nil || s != nil {
		t.Fatal("no ring label means no rings")
	}
}

func TestRingsNeverShareAWaveAndWaitForFrozenRing(t *testing.T) {
	st := ringFixture()
	ordered, rings, _ := assignRings(st, []string{"p1", "c1", "x", "c2", "p2"}, "ring", []string{"canary", "prod"})
	st.Rollouts = []model.Rollout{{ID: "r", Name: "r", RevisionID: "v", Status: "running", SiteIDs: ordered, SiteRings: rings, WaveSize: 5, MaxFailures: 1}}
	st.Freezes = []model.Freeze{{StartAt: t0, EndAt: t0.Add(time.Hour), SiteIDs: []string{"c2"}}}
	ro := &st.Rollouts[0]
	desired := func() string {
		out := []string{}
		for _, s := range st.Sites {
			if s.DesiredRevision == "v" {
				out = append(out, s.ID)
			}
		}
		return strings.Join(out, ",")
	}
	finish := func(at time.Time, ids ...string) {
		for i := range st.Sites {
			if contains(ids, st.Sites[i].ID) {
				st.Sites[i].AppliedRevision = "v"
			}
		}
		reconcileRolloutsAt(&st, at)
	}
	reconcileRolloutsAt(&st, t0)
	if desired() != "c1" {
		t.Fatal("first ring only: " + desired())
	}
	finish(t0.Add(time.Second), "c1")
	if desired() != "c1" || !ro.HeldByFreeze {
		t.Fatalf("a frozen site must hold later rings: %s %+v", desired(), ro)
	}
	finish(t0.Add(2 * time.Hour))
	if desired() != "c1,c2" {
		t.Fatal(desired())
	}
	finish(t0.Add(2*time.Hour+time.Second), "c2")
	if desired() != "p1,c1,c2,p2" {
		t.Fatal("second ring: " + desired())
	}
	finish(t0.Add(2*time.Hour+2*time.Second), "p1", "p2")
	if desired() != "p1,c1,x,c2,p2" {
		t.Fatal("unlisted sites last: " + desired())
	}
	finish(t0.Add(2*time.Hour+3*time.Second), "x")
	if ro.Status != "completed" || ro.CurrentWave != 4 {
		t.Fatalf("%s wave %d", ro.Status, ro.CurrentWave)
	}
}

func TestFindConflictsWindows(t *testing.T) {
	at := func(h int) *time.Time { v := t0.Add(time.Duration(h) * time.Hour); return &v }
	st := model.State{Rollouts: []model.Rollout{
		{ID: "done", Status: "completed", SiteIDs: []string{"a"}},
		{ID: "ends", Status: "running", SiteIDs: []string{"a", "b"}, EndAt: at(1)},
		{ID: "later", Status: "scheduled", SiteIDs: []string{"a"}, StartAt: at(10), EndAt: at(12)},
		{ID: "other", Status: "paused", SiteIDs: []string{"z"}},
	}}
	ids := func(start, end *time.Time, exclude string) []string {
		out := []string{}
		for _, c := range findConflicts(st, []string{"a"}, start, end, exclude, t0) {
			out = append(out, c.RolloutID)
		}
		return out
	}
	for _, tc := range []struct {
		start, end *time.Time
		exclude    string
		want       []string
	}{
		{nil, nil, "", []string{"ends", "later"}},
		{nil, at(10), "", []string{"ends"}},
		{at(1), at(10), "", []string{}},
		{at(11), nil, "", []string{"later"}},
		{at(12), nil, "", []string{}},
		{nil, nil, "ends", []string{"later"}},
	} {
		if got := ids(tc.start, tc.end, tc.exclude); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("start=%v end=%v: got %v want %v", tc.start, tc.end, got, tc.want)
		}
	}
}

func rolloutBody(revID string, sites []string, extra map[string]any) map[string]any {
	m := map[string]any{"revisionId": revID, "siteIds": sites, "waveSize": 1}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestOverlappingRolloutAPI(t *testing.T) {
	e := newTestEnv(t)
	defer e.close()
	loginTestAdmin(t, e)
	rev := createTestRevision(t, e, "r")
	a := registerSite(t, e, "a", nil).SiteID
	b := registerSite(t, e, "b", nil).SiteID
	create := func(sites []string, extra map[string]any, want int) []byte {
		t.Helper()
		code, body := e.req("POST", "/api/v1/rollouts", rolloutBody(rev.ID, sites, extra), true)
		if code != want {
			t.Fatalf("%v %v: got %d want %d: %s", sites, extra, code, want, body)
		}
		return body
	}
	first := decode[model.Rollout](t, create([]string{a}, nil, 201))
	if first.ConflictPolicy != "reject" {
		t.Fatalf("default policy: %q", first.ConflictPolicy)
	}
	conflict := decode[struct {
		Conflicts []rolloutConflict `json:"conflicts"`
	}](t, create([]string{a, b}, nil, 409))
	if len(conflict.Conflicts) != 1 || conflict.Conflicts[0].RolloutID != first.ID || !reflect.DeepEqual(conflict.Conflicts[0].SharedSites, []string{a}) {
		t.Fatalf("%+v", conflict)
	}
	for _, s := range e.store.Snapshot().Sites {
		if s.ID == b && s.DesiredRevision != "" {
			t.Fatal("a rejected rollout must change nothing")
		}
	}
	code, body := e.req("POST", "/api/v1/rollouts/plan", rolloutBody(rev.ID, []string{a, b}, nil), true)
	if code != 200 || !strings.Contains(string(body), first.ID) {
		t.Fatalf("plan should list conflicts: %d %s", code, body)
	}
	create([]string{b}, nil, 201) // disjoint sites are fine
	create([]string{a}, map[string]any{"conflictPolicy": "bogus"}, 400)
	create([]string{a}, map[string]any{"conflictPolicy": "supersede", "approvalRequired": true}, 400)
	create([]string{a}, map[string]any{"conflictPolicy": "supersede", "startAt": time.Now().Add(time.Hour)}, 400)
	next := decode[model.Rollout](t, create([]string{a}, map[string]any{"conflictPolicy": "supersede"}, 201))
	byID := map[string]model.Rollout{}
	for _, ro := range e.store.Snapshot().Rollouts {
		byID[ro.ID] = ro
	}
	if byID[first.ID].Status != "aborted" || byID[first.ID].SupersededBy != next.ID || byID[next.ID].Status != "running" {
		t.Fatalf("%+v", byID[first.ID])
	}
	if countEvents(e.store.Snapshot(), "rollout.superseded") != 1 {
		t.Fatal("missing superseded event")
	}
	create([]string{a}, map[string]any{"conflictPolicy": "allow"}, 201)
}

func TestRetryRefusesToClobberNewerRollout(t *testing.T) {
	e := newTestEnv(t)
	defer e.close()
	loginTestAdmin(t, e)
	rev := createTestRevision(t, e, "r")
	a := registerSite(t, e, "a", nil).SiteID
	_, body := e.req("POST", "/api/v1/rollouts", rolloutBody(rev.ID, []string{a}, nil), true)
	old := decode[model.Rollout](t, body)
	if err := e.store.Update(func(st *model.State) error { st.Rollouts[0].Status = "failed"; return nil }); err != nil {
		t.Fatal(err)
	}
	if code, b := e.req("POST", "/api/v1/rollouts", rolloutBody(rev.ID, []string{a}, nil), true); code != 201 {
		t.Fatalf("a failed rollout does not block: %d %s", code, b)
	}
	code, b := e.req("POST", "/api/v1/rollouts/"+old.ID+"/retry", nil, true)
	if code != 409 || !strings.Contains(string(b), "conflicts") {
		t.Fatalf("retry: %d %s", code, b)
	}
}

func TestFreezeAPI(t *testing.T) {
	e := newTestEnv(t)
	defer e.close()
	loginTestAdmin(t, e)
	rev := createTestRevision(t, e, "r")
	a := registerSite(t, e, "a", map[string]string{"env": "prod"}).SiteID
	past := time.Now().Add(-time.Hour)
	for name, body := range map[string]map[string]any{
		"no name":           {"durationHours": 1, "allSites": true},
		"no end":            {"name": "x", "allSites": true},
		"both ends":         {"name": "x", "allSites": true, "durationHours": 1, "endAt": time.Now().Add(time.Hour)},
		"negative duration": {"name": "x", "allSites": true, "durationHours": -1},
		"too long":          {"name": "x", "allSites": true, "durationHours": 9000},
		"long via endAt":    {"name": "x", "allSites": true, "endAt": time.Now().Add(400 * 24 * time.Hour)},
		"ended":             {"name": "x", "allSites": true, "startAt": past.Add(-time.Hour), "endAt": past},
		"end before start":  {"name": "x", "allSites": true, "startAt": time.Now().Add(2 * time.Hour), "endAt": time.Now().Add(time.Hour)},
		"no scope":          {"name": "x", "durationHours": 1},
		"empty selector":    {"name": "x", "durationHours": 1, "selector": map[string]string{}},
		"all and scope":     {"name": "x", "durationHours": 1, "allSites": true, "siteIds": []string{a}},
		"unknown site":      {"name": "x", "durationHours": 1, "siteIds": []string{"nope"}},
		"unknown group":     {"name": "x", "durationHours": 1, "groupId": "nope"},
		"unknown field":     {"name": "x", "durationHours": 1, "allSites": true, "siteId": a},
	} {
		if code, b := e.req("POST", "/api/v1/freezes", body, true); code != 400 {
			t.Errorf("%s: got %d %s", name, code, b)
		}
	}
	code, b := e.req("POST", "/api/v1/site-groups", map[string]any{"name": "prod", "selector": map[string]string{"env": "prod"}}, true)
	if code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	group := e.store.Snapshot().SiteGroups[0].ID
	code, b = e.req("POST", "/api/v1/freezes", map[string]any{"name": "holiday", "reason": "peak season", "durationHours": 2, "groupId": group}, true)
	if code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	f := decode[freezeView](t, b)
	if !f.Active || f.CreatedBy != "admin@example.test" || !reflect.DeepEqual(f.ResolvedSiteIDs, []string{a}) {
		t.Fatalf("%+v", f)
	}
	if code, b = e.req("DELETE", "/api/v1/site-groups/"+group, nil, true); code != 409 {
		t.Fatalf("deleting a frozen group would lift the freeze: %d %s", code, b)
	}

	// Plan reports it; a rollout is accepted but waits.
	code, b = e.req("POST", "/api/v1/rollouts/plan", rolloutBody(rev.ID, []string{a}, nil), true)
	plan := decode[map[string]any](t, b)
	if code != 200 || len(plan["frozenSites"].([]any)) != 1 {
		t.Fatalf("%d %s", code, b)
	}
	code, b = e.req("POST", "/api/v1/rollouts/plan", rolloutBody(rev.ID, []string{a}, map[string]any{"startAt": time.Now().Add(3 * time.Hour)}), true)
	if plan = decode[map[string]any](t, b); code != 200 || len(plan["frozenSites"].([]any)) != 0 {
		t.Fatalf("a rollout starting after the freeze is unaffected: %s", b)
	}
	for name, tc := range map[string]struct {
		extra map[string]any
		want  int
	}{
		"override without reason": {map[string]any{"overrideFreeze": true}, 400},
		"blank reason":            {map[string]any{"overrideFreeze": true, "overrideReason": "  "}, 400},
		"reason without override": {map[string]any{"overrideReason": "x"}, 400},
	} {
		if code, b := e.req("POST", "/api/v1/rollouts", rolloutBody(rev.ID, []string{a}, tc.extra), true); code != tc.want {
			t.Errorf("%s: %d %s", name, code, b)
		}
	}
	code, b = e.req("POST", "/api/v1/rollouts", rolloutBody(rev.ID, []string{a}, nil), true)
	held := decode[model.Rollout](t, b)
	if code != 201 || held.CurrentWave != 0 || !held.HeldByFreeze || !reflect.DeepEqual(held.HeldSites, []string{a}) {
		t.Fatalf("%d %s", code, b)
	}
	if code, b = e.req("POST", "/api/v1/rollouts/"+held.ID+"/advance", nil, true); code != 409 {
		t.Fatalf("advance must respect a freeze: %d %s", code, b)
	}
	code, b = e.req("POST", "/api/v1/rollouts", rolloutBody(rev.ID, []string{a}, map[string]any{"conflictPolicy": "supersede", "overrideFreeze": true, "overrideReason": "CVE fix"}), true)
	forced := decode[model.Rollout](t, b)
	if code != 201 || forced.CurrentWave != 1 || forced.OverrideReason != "CVE fix" {
		t.Fatalf("%d %s", code, b)
	}
	if countEvents(e.store.Snapshot(), "rollout.freeze_override") != 1 {
		t.Fatal("override must be recorded")
	}

	code, b = e.req("GET", "/api/v1/freezes", nil, false)
	if list := decode[[]freezeView](t, b); code != 200 || len(list) != 1 {
		t.Fatalf("%d %s", code, b)
	}
	if code, _ := anonymousRequest(t, e, "GET", "/api/v1/freezes"); code != 401 {
		t.Fatalf("anonymous list: %d", code)
	}
	if code, b = e.req("DELETE", "/api/v1/freezes/"+f.ID, nil, false); code != 403 {
		t.Fatalf("delete without the request marker: %d %s", code, b)
	}
	if code, b = e.req("DELETE", "/api/v1/freezes/"+f.ID, nil, true); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if code, _ = e.req("DELETE", "/api/v1/freezes/"+f.ID, nil, true); code != 404 {
		t.Fatalf("second delete: %d", code)
	}
	if code, b = e.req("DELETE", "/api/v1/site-groups/"+group, nil, true); code != 200 {
		t.Fatalf("group free again: %d %s", code, b)
	}
}

func TestFreezeRolePermissionsAndRetry(t *testing.T) {
	e := newTestEnv(t)
	defer e.close()
	loginTestAdmin(t, e)
	rev := createTestRevision(t, e, "r")
	a := registerSite(t, e, "a", nil).SiteID
	code, b := e.req("POST", "/api/v1/api-tokens", map[string]any{"name": "op", "role": "operator", "scopes": []string{"read", "rollouts:write"}}, true)
	if code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	token := decode[struct {
		Token string `json:"token"`
	}](t, b).Token
	client := e.server.Client()
	if code, b = apiReq(t, client, e.server.URL, token, "POST", "/api/v1/freezes", map[string]any{"name": "x", "durationHours": 1, "allSites": true}); code != 403 {
		t.Fatalf("operators cannot create freezes: %d %s", code, b)
	}
	if code, b = apiReq(t, client, e.server.URL, token, "POST", "/api/v1/rollouts", rolloutBody(rev.ID, []string{a}, map[string]any{"overrideFreeze": true, "overrideReason": "x"})); code != 403 {
		t.Fatalf("operators cannot override: %d %s", code, b)
	}
	if code, b = apiReq(t, client, e.server.URL, token, "GET", "/api/v1/freezes", nil); code != 200 {
		t.Fatalf("read: %d %s", code, b)
	}

	// A failed, rolled-back rollout cannot be retried into a freeze.
	_, b = e.req("POST", "/api/v1/rollouts", rolloutBody(rev.ID, []string{a}, nil), true)
	ro := decode[model.Rollout](t, b)
	if err := e.store.Update(func(st *model.State) error {
		st.Rollouts[0].Status, st.Rollouts[0].RolledBack = "failed", true
		st.Sites[0].DesiredRevision = ""
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, b = e.req("POST", "/api/v1/freezes", map[string]any{"name": "x", "durationHours": 1, "allSites": true}, true)
	if code != 201 {
		t.Fatalf("%d %s", code, b)
	}
	if code, b = e.req("POST", "/api/v1/rollouts/"+ro.ID+"/retry", nil, true); code != 409 {
		t.Fatalf("retry during freeze: %d %s", code, b)
	}
	if e.store.Snapshot().Sites[0].DesiredRevision != "" {
		t.Fatal("refused retry changed the site")
	}
}

func TestRingAndSoakAPIValidationAndSiteReport(t *testing.T) {
	e := newTestEnv(t)
	defer e.close()
	loginTestAdmin(t, e)
	rev := createTestRevision(t, e, "r")
	prod := registerSite(t, e, "prod", map[string]string{"ring": "prod"}).SiteID
	canary := registerSite(t, e, "canary", map[string]string{"ring": "canary"}).SiteID
	ids := []string{prod, canary}
	for name, extra := range map[string]map[string]any{
		"soak negative":      {"minHealthySeconds": -1},
		"soak too long":      {"minHealthySeconds": 86401},
		"label only":         {"ringLabel": "ring"},
		"order only":         {"ringOrder": []string{"a"}},
		"bad label":          {"ringLabel": "bad label", "ringOrder": []string{"a"}},
		"empty value":        {"ringLabel": "ring", "ringOrder": []string{""}},
		"duplicate":          {"ringLabel": "ring", "ringOrder": []string{"a", "a"}},
		"too many":           {"ringLabel": "ring", "ringOrder": strings.Split("abcdefghijklmnopqrstu", "")},
		"rings, all at once": {"ringLabel": "ring", "ringOrder": []string{"a"}, "strategy": "all-at-once"},
	} {
		for _, path := range []string{"/api/v1/rollouts", "/api/v1/rollouts/plan"} {
			if code, b := e.req("POST", path, rolloutBody(rev.ID, ids, extra), true); code != 400 {
				t.Errorf("%s %s: %d %s", name, path, code, b)
			}
		}
	}
	rings := map[string]any{"ringLabel": "ring", "ringOrder": []string{"canary", "prod"}, "waveSize": 5, "minHealthySeconds": 3600}
	code, b := e.req("POST", "/api/v1/rollouts/plan", rolloutBody(rev.ID, ids, rings), true)
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	var plan struct {
		Waves             int
		MinHealthySeconds int
		SiteIDs           []string `json:"siteIds"`
		Rings             []ringSummary
	}
	if err := json.Unmarshal(b, &plan); err != nil || plan.Waves != 2 || plan.MinHealthySeconds != 3600 || !reflect.DeepEqual(plan.SiteIDs, []string{canary, prod}) || len(plan.Rings) != 2 {
		t.Fatalf("%v %s", err, b)
	}
	code, b = e.req("POST", "/api/v1/rollouts", rolloutBody(rev.ID, ids, rings), true)
	ro := decode[model.Rollout](t, b)
	if code != 201 || !reflect.DeepEqual(ro.ActivatedSites, []string{canary}) || ro.MinHealthySeconds != 3600 {
		t.Fatalf("%d %s", code, b)
	}
	code, b = e.req("GET", "/api/v1/rollouts/"+ro.ID+"/sites", nil, false)
	rep := decode[RolloutSitesReport](t, b)
	if code != 200 || len(rep.Sites) != 2 || rep.Sites[0].State != "applying" || rep.Sites[0].Ring != "canary" || rep.Sites[0].ActivatedAt == nil || rep.Sites[1].State != "waiting" || rep.Counts["waiting"] != 1 {
		t.Fatalf("%d %s", code, b)
	}
	if code, _ = e.req("GET", "/api/v1/rollouts/nope/sites", nil, false); code != 404 {
		t.Fatal(code)
	}
	if code, _ := anonymousRequest(t, e, "GET", "/api/v1/rollouts/"+ro.ID+"/sites"); code != 401 {
		t.Fatalf("anonymous: %d", code)
	}
}

func TestRolloutSiteStates(t *testing.T) {
	ids := []string{"done", "bad", "slow", "retry", "soak", "apply", "held", "wait"}
	ro := model.Rollout{ID: "r", RevisionID: "v", Status: "running", SiteIDs: ids,
		ActivatedSites: ids[:6], CompletedSites: []string{"done"}, FailedSites: []string{"bad", "slow"}, TimedOutSites: []string{"slow"},
		RetryAt: map[string]time.Time{"retry": t0}, SoakingSites: []string{"soak"}, HeldSites: []string{"held"},
		RingOrder: []string{"one"}, SiteRings: map[string]int{"done": 0, "wait": 1}}
	st := model.State{Sites: []model.Site{{ID: "bad", FailedRevision: "v", RevisionError: "boom"}, {ID: "done", RevisionError: "stale"}}}
	rep := buildRolloutSites(st, ro)
	want := []string{"completed", "failed", "timed_out", "retry_wait", "soaking", "applying", "held", "waiting"}
	for i, s := range rep.Sites {
		if s.State != want[i] {
			t.Errorf("%s: %s want %s", s.SiteID, s.State, want[i])
		}
	}
	if rep.Sites[1].Error != "boom" || rep.Sites[0].Error != "" || rep.Sites[0].Ring != "one" || rep.Sites[7].Ring != "(unlisted)" {
		t.Fatalf("%+v", rep.Sites)
	}
	ro.Status = "aborted"
	if got := buildRolloutSites(st, ro).Sites[7].State; got != "not_started" {
		t.Fatal(got)
	}
}
