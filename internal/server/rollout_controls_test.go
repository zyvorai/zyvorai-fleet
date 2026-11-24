// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package server

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/fleet/internal/model"
)

func TestParseAndEvaluateDailyWindow(t *testing.T) {
	for _, bad := range []string{"", "1:00-2:00", "01:00", "01:00-01:00", "24:00-05:00", "01:60-02:00", "aa:bb-cc:dd", "01:00-02:00-03:00"} {
		if _, _, err := parseDailyWindow(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	at := func(h, m int) time.Time { return time.Date(2026, 11, 1, h, m, 0, 0, time.UTC) }
	for _, tc := range []struct {
		spec string
		now  time.Time
		want bool
	}{
		{"", at(12, 0), true},
		{"garbage", at(12, 0), true}, // never freeze a rollout on a bad stored value
		{"01:00-05:00", at(0, 59), false},
		{"01:00-05:00", at(1, 0), true},
		{"01:00-05:00", at(4, 59), true},
		{"01:00-05:00", at(5, 0), false},
		{"22:00-02:00", at(23, 30), true},
		{"22:00-02:00", at(1, 59), true},
		{"22:00-02:00", at(2, 0), false},
		{"22:00-02:00", at(12, 0), false},
		{"01:00-05:00", at(3, 0).In(time.FixedZone("x", 5*3600)), true}, // evaluated in UTC
	} {
		if got := inDailyWindow(tc.spec, tc.now); got != tc.want {
			t.Errorf("inDailyWindow(%q,%s)=%v want %v", tc.spec, tc.now.Format("15:04"), got, tc.want)
		}
	}
}

func TestFailurePercentBudget(t *testing.T) {
	ro := &model.Rollout{SiteIDs: make([]string, 10), MaxFailures: 5, MaxFailurePercent: 20}
	if failureBudgetExceeded(ro, 2) {
		t.Fatal("2 of 10 is exactly 20%, not over")
	}
	if !failureBudgetExceeded(ro, 3) {
		t.Fatal("3 of 10 exceeds 20%")
	}
	ro.MaxFailurePercent = 0
	if failureBudgetExceeded(ro, 5) || !failureBudgetExceeded(ro, 6) {
		t.Fatal("absolute budget alone must still apply")
	}
}

func percentFixture() (model.State, time.Time) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	ids := []string{"a", "b", "c", "d", "e"}
	var sites []model.Site
	for _, id := range ids {
		sites = append(sites, model.Site{ID: id})
	}
	return model.State{Sites: sites, Rollouts: []model.Rollout{{ID: "r", Name: "r", RevisionID: "v", Status: "running", SiteIDs: ids, WaveSize: 5, MaxFailures: 4, MaxFailurePercent: 30}}}, t0
}

func TestPercentBudgetFailsRolloutBeforeAbsoluteBudget(t *testing.T) {
	st, t0 := percentFixture()
	reconcileRolloutsAt(&st, t0)
	st.Sites[0].FailedRevision = "v"
	reconcileRolloutsAt(&st, t0.Add(time.Second))
	if st.Rollouts[0].Status != "running" {
		t.Fatalf("1 of 5 is 20%%, rollout should continue: %s", st.Rollouts[0].Status)
	}
	st.Sites[1].FailedRevision = "v"
	reconcileRolloutsAt(&st, t0.Add(2*time.Second))
	if st.Rollouts[0].Status != "failed" {
		t.Fatalf("2 of 5 is 40%% > 30%%, expected failed, got %s", st.Rollouts[0].Status)
	}
}

func retryFixture(retries, delay int) (model.State, time.Time) {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	return model.State{
		Sites:    []model.Site{{ID: "a"}, {ID: "b"}},
		Rollouts: []model.Rollout{{ID: "r", Name: "r", RevisionID: "v", Status: "running", SiteIDs: []string{"a", "b"}, WaveSize: 2, MaxFailures: 0, SiteRetries: retries, RetryDelaySeconds: delay}},
	}, t0
}

func countEvents(st model.State, kind string) int {
	n := 0
	for _, e := range st.Events {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

func TestSiteRetriesAreBoundedAndDelayed(t *testing.T) {
	st, t0 := retryFixture(2, 30)
	reconcileRolloutsAt(&st, t0)
	now := t0
	for attempt := 1; attempt <= 2; attempt++ {
		st.Sites[0].FailedRevision, st.Sites[0].RevisionError = "v", "boom"
		now = now.Add(time.Second)
		reconcileRolloutsAt(&st, now)
		if st.Rollouts[0].Status != "running" || len(st.Rollouts[0].FailedSites) != 0 {
			t.Fatalf("attempt %d: failure should be held for retry: %+v", attempt, st.Rollouts[0])
		}
		if st.Sites[0].FailedRevision == "" {
			t.Fatalf("attempt %d: retried before the delay elapsed", attempt)
		}
		now = now.Add(31 * time.Second)
		reconcileRolloutsAt(&st, now)
		if st.Rollouts[0].Retries["a"] != attempt || st.Sites[0].FailedRevision != "" || st.Sites[0].DesiredRevision != "v" {
			t.Fatalf("attempt %d: expected retry to clear the failure: %+v site=%+v", attempt, st.Rollouts[0], st.Sites[0])
		}
	}
	if countEvents(st, "rollout.site_retry") != 2 {
		t.Fatalf("want 2 retry events, got %d", countEvents(st, "rollout.site_retry"))
	}
	st.Sites[0].FailedRevision = "v"
	reconcileRolloutsAt(&st, now.Add(time.Second))
	if st.Rollouts[0].Status != "failed" || len(st.Rollouts[0].FailedSites) != 1 {
		t.Fatalf("third failure exhausts retries and must count: %+v", st.Rollouts[0])
	}
}

func TestSiteRetryRecoveryClearsSchedule(t *testing.T) {
	st, t0 := retryFixture(3, 60)
	reconcileRolloutsAt(&st, t0)
	st.Sites[0].FailedRevision = "v"
	reconcileRolloutsAt(&st, t0.Add(time.Second))
	if _, ok := st.Rollouts[0].RetryAt["a"]; !ok {
		t.Fatal("retry should be scheduled")
	}
	st.Sites[0].FailedRevision, st.Sites[0].AppliedRevision = "", "v"
	reconcileRolloutsAt(&st, t0.Add(2*time.Second))
	if len(st.Rollouts[0].RetryAt) != 0 || st.Rollouts[0].Retries["a"] != 0 {
		t.Fatalf("a recovered site must leave no retry state: %+v", st.Rollouts[0])
	}
}

func TestSiteRetryAfterTimeoutRearmsClockWithoutDuplicates(t *testing.T) {
	st, t0 := retryFixture(1, 0)
	st.Rollouts[0].SiteTimeoutSeconds = 60
	st.Rollouts[0].MaxFailures = 1
	reconcileRolloutsAt(&st, t0)
	reconcileRolloutsAt(&st, t0.Add(61*time.Second)) // a and b time out, each retried at once
	ro := st.Rollouts[0]
	if ro.Retries["a"] != 1 || ro.Retries["b"] != 1 || len(ro.FailedSites) != 0 || len(ro.TimedOutSites) != 0 {
		t.Fatalf("timeouts should be retried, not failed: %+v", ro)
	}
	reconcileRolloutsAt(&st, t0.Add(100*time.Second)) // clock re-armed at 61s, 39s elapsed
	if len(st.Rollouts[0].FailedSites) != 0 {
		t.Fatal("re-armed clock must not time out again early")
	}
	reconcileRolloutsAt(&st, t0.Add(125*time.Second))
	ro = st.Rollouts[0]
	if len(ro.FailedSites) != 2 || len(ro.TimedOutSites) != 2 {
		t.Fatalf("second timeout exhausts the single retry: %+v", ro)
	}
}

func TestDailyWindowGatesWaves(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	st := model.State{
		Sites:    []model.Site{{ID: "a"}, {ID: "b"}},
		Rollouts: []model.Rollout{{ID: "r", Name: "r", RevisionID: "v", Status: "running", SiteIDs: []string{"a", "b"}, WaveSize: 1, DailyWindow: "01:00-05:00"}},
	}
	reconcileRolloutsAt(&st, t0)
	if st.Sites[0].DesiredRevision != "" || st.Rollouts[0].CurrentWave != 0 {
		t.Fatal("window closed: nothing may activate")
	}
	night := time.Date(2026, 11, 2, 2, 0, 0, 0, time.UTC)
	reconcileRolloutsAt(&st, night)
	if st.Sites[0].DesiredRevision != "v" || st.Sites[1].DesiredRevision != "" {
		t.Fatal("window open: first wave only")
	}
	st.Sites[0].AppliedRevision = "v"
	reconcileRolloutsAt(&st, time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC))
	if st.Sites[1].DesiredRevision != "" {
		t.Fatal("window closed again: second wave must wait")
	}
	reconcileRolloutsAt(&st, time.Date(2026, 11, 3, 1, 30, 0, 0, time.UTC))
	if st.Sites[1].DesiredRevision != "v" {
		t.Fatal("next night the second wave starts")
	}
}

func TestAdvanceRolloutStartsNextWaveEarly(t *testing.T) {
	t0 := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	st := model.State{
		Sites:    []model.Site{{ID: "a"}, {ID: "b"}, {ID: "c"}},
		Rollouts: []model.Rollout{{ID: "r", Name: "r", RevisionID: "v", Status: "running", SiteIDs: []string{"a", "b", "c"}, WaveSize: 1, PauseSeconds: 3600, DailyWindow: "01:00-02:00"}},
	}
	ro := &st.Rollouts[0]
	if err := advanceRollout(&st, ro, t0); err != nil {
		t.Fatal(err)
	}
	if st.Sites[0].DesiredRevision != "v" || ro.CurrentWave != 1 {
		t.Fatal("advance must ignore a closed window and start wave 1")
	}
	if err := advanceRollout(&st, ro, t0); err != nil || st.Sites[1].DesiredRevision != "v" || ro.CurrentWave != 2 {
		t.Fatalf("advance must not wait for the still-active first wave: %v", err)
	}
	if err := advanceRollout(&st, ro, t0); err != nil || ro.CurrentWave != 3 {
		t.Fatal(err)
	}
	if err := advanceRollout(&st, ro, t0); err == nil {
		t.Fatal("nothing left to advance should be an error")
	}
	ro.Status = "paused"
	if err := advanceRollout(&st, ro, t0); err == nil {
		t.Fatal("paused rollouts cannot be advanced")
	}
}

func TestRolloutPolicyValidationAndPlanEcho(t *testing.T) {
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
		{"percent negative", base(map[string]any{"maxFailurePercent": -1}), 400},
		{"percent 100", base(map[string]any{"maxFailurePercent": 100}), 400},
		{"retries too many", base(map[string]any{"siteRetries": 11}), 400},
		{"retries negative", base(map[string]any{"siteRetries": -1}), 400},
		{"delay without retries", base(map[string]any{"retryDelaySeconds": 5}), 400},
		{"delay too long", base(map[string]any{"siteRetries": 1, "retryDelaySeconds": 3601}), 400},
		{"window malformed", base(map[string]any{"dailyWindow": "1am-5am"}), 400},
		{"window empty span", base(map[string]any{"dailyWindow": "03:00-03:00"}), 400},
		{"window with all-at-once", base(map[string]any{"dailyWindow": "01:00-05:00", "strategy": "all-at-once"}), 400},
		{"valid", base(map[string]any{"maxFailurePercent": 50, "siteRetries": 3, "retryDelaySeconds": 30, "dailyWindow": "22:00-02:00"}), 201},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code, body := e.req("POST", "/api/v1/rollouts", tc.body, true); code != tc.want {
				t.Fatalf("got %d want %d: %s", code, tc.want, body)
			}
			// The plan endpoint applies the same validation.
			code, body := e.req("POST", "/api/v1/rollouts/plan", tc.body, true)
			want := 200
			if tc.want == 400 {
				want = 400
			}
			if code != want {
				t.Fatalf("plan got %d want %d: %s", code, want, body)
			}
		})
	}
	code, body := e.req("POST", "/api/v1/rollouts/plan", base(map[string]any{"maxFailurePercent": 50, "siteRetries": 3, "dailyWindow": "22:00-02:00"}), true)
	if code != 200 {
		t.Fatal(code, string(body))
	}
	plan := decode[map[string]any](t, body)
	if plan["maxFailurePercent"] != float64(50) || plan["siteRetries"] != float64(3) || plan["dailyWindow"] != "22:00-02:00" {
		t.Fatalf("plan did not echo the policy: %v", plan)
	}
	rollouts := decode[[]model.Rollout](t, func() []byte { _, b := e.req("GET", "/api/v1/rollouts", nil, true); return b }())
	if len(rollouts) != 1 || rollouts[0].MaxFailurePercent != 50 || rollouts[0].SiteRetries != 3 || rollouts[0].RetryDelaySeconds != 30 || rollouts[0].DailyWindow != "22:00-02:00" {
		t.Fatalf("policy not persisted: %+v", rollouts)
	}
}

func TestAdvanceRolloutAPI(t *testing.T) {
	e := newTestEnv(t)
	defer e.close()
	loginTestAdmin(t, e)
	rev := createTestRevision(t, e, "r")
	regs := []testRegistration{registerSite(t, e, "a", nil), registerSite(t, e, "b", nil)}
	code, body := e.req("POST", "/api/v1/rollouts", map[string]any{"revisionId": rev.ID, "siteIds": []string{regs[0].SiteID, regs[1].SiteID}, "waveSize": 1, "pauseSeconds": 3600}, true)
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	ro := decode[model.Rollout](t, body)
	if ro.CurrentWave != 1 || len(ro.ActivatedSites) != 1 {
		t.Fatalf("first wave only: %+v", ro)
	}
	code, body = e.req("POST", "/api/v1/rollouts/"+ro.ID+"/advance", nil, true)
	if code != 200 {
		t.Fatalf("advance %d %s", code, body)
	}
	ro = decode[model.Rollout](t, body)
	if ro.CurrentWave != 2 || len(ro.ActivatedSites) != 2 {
		t.Fatalf("advance should activate the second site now: %+v", ro)
	}
	if code, _ := e.req("POST", "/api/v1/rollouts/"+ro.ID+"/advance", nil, true); code != 400 {
		t.Fatalf("advance with nothing left: %d", code)
	}
	if code, _ := e.req("POST", "/api/v1/rollouts/nope/advance", nil, true); code != 404 {
		t.Fatalf("unknown rollout: %d", code)
	}
	foundEvent := false
	for _, ev := range e.store.Snapshot().Events {
		foundEvent = foundEvent || ev.Kind == "rollout.advance"
	}
	if !foundEvent {
		t.Fatal("no rollout.advance event")
	}
	if code, _ := anonymousRequest(t, e, "POST", "/api/v1/rollouts/x/advance"); code != 401 {
		t.Fatalf("anonymous advance: %d", code)
	}
	// A rollout created while its daily window is closed holds its first wave.
	now := time.Now().UTC()
	closed := fmt.Sprintf("%02d:00-%02d:00", (now.Hour()+2)%24, (now.Hour()+3)%24)
	code, body = e.req("POST", "/api/v1/rollouts", map[string]any{"revisionId": rev.ID, "siteIds": []string{regs[0].SiteID}, "waveSize": 1, "dailyWindow": closed}, true)
	if code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	held := decode[model.Rollout](t, body)
	if held.CurrentWave != 0 || len(held.ActivatedSites) != 0 || held.Status != "running" {
		t.Fatalf("closed window should hold the first wave: %+v", held)
	}
}

// anonymousRequest sends a request with no session cookie.
func anonymousRequest(t *testing.T, e *testEnv, method, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, e.server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Zyvor-Request", "1")
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestConvergenceReport(t *testing.T) {
	e := newTestEnv(t)
	defer e.close()
	loginTestAdmin(t, e)
	v1 := createTestRevision(t, e, "v1")
	regs := []testRegistration{registerSite(t, e, "a", nil), registerSite(t, e, "b", nil), registerSite(t, e, "c", nil), registerSite(t, e, "d", nil)}
	if err := e.store.Update(func(st *model.State) error {
		for i := range st.Sites {
			switch st.Sites[i].ID {
			case regs[0].SiteID:
				st.Sites[i].DesiredRevision, st.Sites[i].AppliedRevision = v1.ID, v1.ID
			case regs[1].SiteID:
				st.Sites[i].DesiredRevision, st.Sites[i].FailedRevision = v1.ID, v1.ID
			case regs[2].SiteID:
				st.Sites[i].DesiredRevision, st.Sites[i].Status = v1.ID, "offline"
			case regs[3].SiteID:
				st.Sites[i].DesiredRevision, st.Sites[i].AppliedRevision = "rev_other", v1.ID
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, body := e.req("GET", "/api/v1/convergence", nil, true)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	rep := decode[ConvergenceReport](t, body)
	if rep.TotalSites != 4 || rep.Unassigned != 0 || len(rep.Revisions) != 2 {
		t.Fatalf("report %+v", rep)
	}
	var got ConvergenceBucket
	for _, b := range rep.Revisions {
		if b.RevisionID == v1.ID {
			got = b
		}
	}
	if got.Targeted != 3 || got.Applied != 1 || got.Failed != 1 || got.Pending != 1 || got.Offline != 1 || got.Stale != 1 || got.ConvergedPercent != 33.33 || got.Name != "v1" || got.Digest != v1.Digest {
		t.Fatalf("bucket %+v", got)
	}
	if got.Sites != nil {
		t.Fatal("site lists are only returned for a single revision")
	}
	code, body = e.req("GET", "/api/v1/convergence?revisionId="+v1.ID, nil, true)
	if code != 200 {
		t.Fatal(code)
	}
	rep = decode[ConvergenceReport](t, body)
	if len(rep.Revisions) != 1 || len(rep.Revisions[0].Sites["failed"]) != 1 || rep.Revisions[0].Sites["failed"][0] != regs[1].SiteID ||
		len(rep.Revisions[0].Sites["applied"]) != 1 || len(rep.Revisions[0].Sites["offline"]) != 1 || len(rep.Revisions[0].Sites["stale"]) != 1 {
		t.Fatalf("filtered report %+v", rep.Revisions)
	}
	if code, _ := e.req("GET", "/api/v1/convergence?revisionId=missing", nil, true); code != 404 {
		t.Fatalf("unknown revision: %d", code)
	}
	if code, _ := anonymousRequest(t, e, "GET", "/api/v1/convergence"); code != 401 {
		t.Fatalf("anonymous convergence: %d", code)
	}
}

func TestConvergenceEmptyAndUnassigned(t *testing.T) {
	rep := buildConvergence(model.State{Sites: []model.Site{{ID: "x"}}}, "", false, time.Unix(0, 0))
	if rep.Unassigned != 1 || rep.Revisions == nil || len(rep.Revisions) != 0 {
		t.Fatalf("%+v", rep)
	}
	if !strings.Contains(fmt.Sprint(rep.GeneratedAt.Year()), "19") {
		t.Fatal("timestamp passthrough")
	}
}
