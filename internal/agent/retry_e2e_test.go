// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyvorai/fleet/internal/auth"
	"github.com/zyvorai/fleet/internal/model"
	"github.com/zyvorai/fleet/internal/server"
	"github.com/zyvorai/fleet/internal/store"
)

// e2e drives the real control-plane handler and the real agent over HTTP.
type e2e struct {
	t        *testing.T
	store    *store.Store
	url      string
	operator *http.Client
	agent    *Runner
	state    *StateFile
	// beforeHeartbeat, when set, runs once just before the next heartbeat
	// reaches the control plane, to interleave an operator request.
	beforeHeartbeat atomic.Pointer[func()]
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("e2e-admin-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(s *model.State) error {
		s.Users = append(s.Users, model.User{ID: "u1", Email: "admin@example.test", Role: model.RoleAdmin, PasswordHash: hash, CreatedAt: time.Now().UTC()})
		s.EnrollmentTokens = append(s.EnrollmentTokens, model.EnrollmentToken{ID: "e1", TokenHash: auth.SHA256Token("enroll-token"), MaxUses: 1})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sm, err := auth.NewSessionManager([]byte(strings.Repeat("e", 32)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	e := &e2e{t: t, store: st}
	handler := server.New(st, sm, server.Config{SiteTimeout: time.Hour}).Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/agent/heartbeat" {
			if hook := e.beforeHeartbeat.Swap(nil); hook != nil {
				(*hook)()
			}
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	e.url, e.operator = srv.URL, &http.Client{Jar: jar}
	e.api("POST", "/api/v1/auth/login", map[string]string{"email": "admin@example.test", "password": "e2e-admin-password"}, nil)
	e.agent, e.state = newTestRunner(t, srv.URL)
	// Every request fsyncs state; the test deadline bounds a real hang.
	e.agent.client.HTTP.Timeout = 0
	if err := e.agent.ensureEnrollment(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *e2e) api(method, path string, body, out any) {
	e.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			e.t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(e.t.Context(), method, e.url+path, &buf)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("X-Zyvor-Request", "1")
	resp, err := e.operator.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var raw bytes.Buffer
	_, _ = raw.ReadFrom(resp.Body)
	if resp.StatusCode >= 300 {
		e.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, raw.String())
	}
	if out != nil {
		if err := json.Unmarshal(raw.Bytes(), out); err != nil {
			e.t.Fatal(err)
		}
	}
}

// startRollout creates a k3s revision and rolls it out to the one site.
func (e *e2e) startRollout(health *model.HealthProbe, extra map[string]any) (model.Revision, string) {
	e.t.Helper()
	var rev model.Revision
	e.api("POST", "/api/v1/revisions", map[string]any{"name": "r1", "workloads": []model.RuntimeSpec{{Kind: "k3s", Name: "app", State: "running", Manifest: "kind: ConfigMap", Health: health}}}, &rev)
	body := map[string]any{"revisionId": rev.ID, "waveSize": 1}
	for k, v := range extra {
		body[k] = v
	}
	var ro model.Rollout
	e.api("POST", "/api/v1/rollouts", body, &ro)
	return rev, ro.ID
}

func (e *e2e) rollout(id string) model.Rollout {
	e.t.Helper()
	for _, ro := range e.store.Snapshot().Rollouts {
		if ro.ID == id {
			return ro
		}
	}
	e.t.Fatalf("rollout %s missing", id)
	return model.Rollout{}
}

// breakRuntime makes every apply fail by putting a file where the manifest
// directory belongs; the returned function repairs it.
func (e *e2e) breakRuntime() (repair func()) {
	e.t.Helper()
	blocked := filepath.Join(e.t.TempDir(), "manifests")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		e.t.Fatal(err)
	}
	e.agent.runtime.K3sManifestDir = blocked
	return func() {
		e.t.Helper()
		if err := os.Remove(blocked); err != nil {
			e.t.Fatal(err)
		}
	}
}

func TestE2E_AgentReappliesAfterServerSideRetry(t *testing.T) {
	e := newE2E(t)
	repair := e.breakRuntime()
	rev, id := e.startRollout(nil, map[string]any{"siteRetries": 2})

	e.agent.cycle(context.Background())
	ro := e.rollout(id)
	if ro.Status != "running" || len(ro.Retries) != 1 || len(ro.FailedSites) != 0 {
		t.Fatalf("the first failure should be retried, not counted: %+v", ro)
	}
	if site := e.store.Snapshot().Sites[0]; site.FailedRevision != "" || site.DesiredRevision != rev.ID {
		t.Fatalf("retry should hand the revision back: %+v", site)
	}
	if got := e.state.Snapshot(); got.AppliedRevision != "" || got.FailedRevision != rev.ID {
		t.Fatalf("agent state %+v", got)
	}

	repair()
	e.agent.cycle(context.Background())

	if got := e.state.Snapshot(); got.AppliedRevision != rev.ID || got.FailedRevision != "" {
		t.Fatalf("agent must re-apply after the retry: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(e.agent.runtime.K3sManifestDir, "zyvor-fleet-app.yaml")); err != nil {
		t.Fatal(err)
	}
	ro = e.rollout(id)
	if ro.Status != "completed" || len(ro.CompletedSites) != 1 || ro.Retries[ro.SiteIDs[0]] != 1 {
		t.Fatalf("rollout should complete on the second attempt: %+v", ro)
	}
}

func TestE2E_RetriesExhaustOnPersistentFailure(t *testing.T) {
	e := newE2E(t)
	e.breakRuntime()
	_, id := e.startRollout(nil, map[string]any{"siteRetries": 2})
	for cycle := 1; cycle <= 2; cycle++ {
		e.agent.cycle(context.Background())
		if ro := e.rollout(id); ro.Status != "running" || ro.Retries[ro.SiteIDs[0]] != cycle {
			t.Fatalf("cycle %d: %+v", cycle, ro)
		}
	}
	e.agent.cycle(context.Background())
	if ro := e.rollout(id); ro.Status != "failed" || len(ro.FailedSites) != 1 {
		t.Fatalf("the third attempt uses up the budget: %+v", ro)
	}
}

// A retry triggered between the agent's apply and its heartbeat must not be
// consumed by that heartbeat, which still describes the replaced attempt.
func TestE2E_StaleFailureDoesNotConsumeRetry(t *testing.T) {
	e := newE2E(t)
	repair := e.breakRuntime()
	rev, id := e.startRollout(nil, map[string]any{"siteRetries": 1})
	// While the report of a failed apply is in flight, make the control plane
	// already hold that failure and let an operator list call perform the retry.
	inFlight := func() {
		if err := e.store.Update(func(s *model.State) error {
			s.Sites[0].FailedRevision = rev.ID
			return nil
		}); err != nil {
			t.Error(err)
		}
		e.api("GET", "/api/v1/rollouts", nil, nil)
	}
	e.beforeHeartbeat.Store(&inFlight)
	e.agent.cycle(context.Background()) // apply fails; retry lands before the report

	ro := e.rollout(id)
	if ro.Status != "running" || len(ro.FailedSites) != 0 || ro.Retries[ro.SiteIDs[0]] != 1 {
		t.Fatalf("stale report consumed the retry: %+v", ro)
	}
	repair()
	e.agent.cycle(context.Background())
	if ro := e.rollout(id); ro.Status != "completed" {
		t.Fatalf("%+v", ro)
	}
}

// The agent keeps reporting a revision applied when its health check starts to
// fail; minHealthySeconds is what turns that into a rollout failure.
func TestE2E_SoakCatchesLateHealthFailure(t *testing.T) {
	var sick atomic.Bool
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if sick.Load() {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer probe.Close()
	e := newE2E(t)
	rev, id := e.startRollout(&model.HealthProbe{Type: "http", URL: probe.URL, TimeoutSeconds: 30}, map[string]any{"minHealthySeconds": 3600})

	e.agent.cycle(context.Background())
	if ro := e.rollout(id); ro.Status != "running" || len(ro.SoakingSites) != 1 || len(ro.CompletedSites) != 0 {
		t.Fatalf("applied site should soak: %+v", ro)
	}
	sick.Store(true)
	e.agent.cycle(context.Background())
	if got := e.state.Snapshot(); got.AppliedRevision != rev.ID || got.FailedRevision != rev.ID {
		t.Fatalf("agent state %+v", got)
	}
	if ro := e.rollout(id); ro.Status != "failed" || len(ro.FailedSites) != 1 {
		t.Fatalf("late health failure must fail the rollout: %+v", ro)
	}
}
