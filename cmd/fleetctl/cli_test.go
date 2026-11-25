// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zyvorai/fleet/internal/auth"
	"github.com/zyvorai/fleet/internal/model"
	"github.com/zyvorai/fleet/internal/server"
	"github.com/zyvorai/fleet/internal/store"
)

// TestMain lets the test binary act as fleetctl, so commands are exercised end
// to end (flags, login, request, output) against the real HTTP handler.
func TestMain(m *testing.M) {
	if os.Getenv("FLEETCTL_TEST_RUN_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestFreezeCreateBody(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want map[string]any
	}{
		{[]string{"holiday", "48", "all"}, map[string]any{"name": "holiday", "durationHours": 48, "allSites": true}},
		{[]string{"g", "1", "group:grp_1", "year", "end"}, map[string]any{"name": "g", "durationHours": 1, "groupId": "grp_1", "reason": "year end"}},
		{[]string{"s", "2", "sites:a, b"}, map[string]any{"name": "s", "durationHours": 2, "siteIds": []string{"a", "b"}}},
		{[]string{"l", "3", "env=prod,class=retail"}, map[string]any{"name": "l", "durationHours": 3, "selector": map[string]string{"env": "prod", "class": "retail"}}},
	} {
		got, err := freezeCreateBody(tc.args)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%v: got %v, %v", tc.args, got, err)
		}
	}
	for _, bad := range [][]string{nil, {"a", "1"}, {"a", "0", "all"}, {"a", "x", "all"}, {"a", "1", "prod"}, {"a", "1", "sites:"}, {"a", "1", "group:"}, {"a", "1", "env=prod,oops"}} {
		if body, err := freezeCreateBody(bad); err == nil {
			t.Errorf("%v should be rejected, got %v", bad, body)
		}
	}
}

func TestFormatFreezesAndRolloutSites(t *testing.T) {
	out, err := formatFreezes([]byte(`[]`))
	if err != nil || out != "no change freezes\n" {
		t.Fatalf("%q %v", out, err)
	}
	out, err = formatFreezes([]byte(`[{"id":"frz_1","name":"a","reason":"why","active":true,"startAt":"2026-01-01T00:00:00Z","endAt":"2026-01-02T00:00:00Z","resolvedSiteIds":["x","y"]},{"id":"frz_2","name":"b","expired":true},{"id":"frz_3","name":"c"}]`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"active", "expired", "pending", "a (why)", "2026-01-02T00:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %s", want, out)
		}
	}
	out, err = formatRolloutSites([]byte(`{"name":"r","status":"running","counts":{"soaking":1,"completed":2},"sites":[{"siteId":"site_x","state":"soaking","retries":2,"error":"bad\u001b[2Jthing\nrow"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "r [running] 2 completed, 1 soaking") || !strings.Contains(out, "site_x") {
		t.Fatal(out)
	}
	if strings.Contains(out, "\x1b") || strings.Count(out, "\n") != 3 {
		t.Fatalf("control characters from an agent must not reach the terminal: %q", out)
	}
	for _, f := range []func([]byte) (string, error){formatFreezes, formatRolloutSites} {
		if _, err := f([]byte("nope")); err == nil {
			t.Fatal("bad body must error")
		}
	}
}

func TestCLIAgainstRealHandler(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword("cli-test-password")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := st.Update(func(s *model.State) error {
		s.Users = append(s.Users, model.User{ID: "u1", Email: "admin@zyvor.local", Role: model.RoleAdmin, PasswordHash: hash, CreatedAt: now})
		s.Sites = append(s.Sites, model.Site{ID: "site_a", Name: "alpha", Status: "online", LastSeen: now})
		s.Revisions = append(s.Revisions, model.Revision{ID: "rev_1", Name: "one", CreatedAt: now})
		s.Rollouts = append(s.Rollouts, model.Rollout{ID: "rol_1", Name: "first", RevisionID: "rev_1", Status: "running", WaveSize: 1, SiteIDs: []string{"site_a"}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sm, err := auth.NewSessionManager([]byte(strings.Repeat("c", 32)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(server.New(st, sm, server.Config{SiteTimeout: time.Hour}).Handler())
	defer srv.Close()

	run := func(args ...string) (string, error) {
		cmd := exec.CommandContext(t.Context(), os.Args[0], append([]string{"--server", srv.URL, "--password", "cli-test-password"}, args...)...)
		cmd.Env = append(os.Environ(), "FLEETCTL_TEST_RUN_MAIN=1", "ZYVOR_FLEET_API_TOKEN=", "ZYVOR_FLEET_ADMIN_EMAIL=admin@zyvor.local")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	must := func(args ...string) string {
		t.Helper()
		out, err := run(args...)
		if err != nil {
			t.Fatalf("fleetctl %v: %v\n%s", args, err, out)
		}
		return out
	}

	if out := must("freezes"); out != "no change freezes\n" {
		t.Fatal(out)
	}
	var created struct {
		ID     string `json:"id"`
		Active bool   `json:"active"`
	}
	if err := json.Unmarshal([]byte(must("freeze-create", "launch", "2", "sites:site_a", "launch", "week")), &created); err != nil || !created.Active {
		t.Fatalf("%+v %v", created, err)
	}
	if out := must("freezes"); !strings.Contains(out, created.ID) || !strings.Contains(out, "launch (launch week)") {
		t.Fatal(out)
	}
	if out := must("freezes", "json"); !strings.Contains(out, `"resolvedSiteIds"`) {
		t.Fatal(out)
	}
	if out := must("rollout-sites", "rol_1"); !strings.Contains(out, "alpha") || !strings.Contains(out, "1 held") {
		t.Fatal(out)
	}
	if out := must("rollout-sites", "rol_1", "json"); !strings.Contains(out, `"state": "held"`) {
		t.Fatal(out)
	}
	must("freeze-delete", created.ID)
	if out := must("rollout-sites", "rol_1"); !strings.Contains(out, "1 applying") {
		t.Fatal("lifting the freeze should activate the held site: " + out)
	}
	if out, err := run("freeze-delete", created.ID); err == nil || !strings.Contains(out, "404") {
		t.Fatalf("deleting twice: %v %s", err, out)
	}
	if out, err := run("rollout-sites", "nope"); err == nil || !strings.Contains(out, "404") {
		t.Fatalf("unknown rollout: %v %s", err, out)
	}
	for _, args := range [][]string{{"freeze-create", "x", "1"}, {"freeze-delete"}, {"rollout-sites"}} {
		if out, err := run(args...); err == nil || !strings.Contains(out, "requires") {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
}
