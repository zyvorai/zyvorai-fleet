// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zyvorai/fleet/internal/model"
)

func digestServer(t *testing.T, rev model.Revision, heartbeat *struct {
	Events []model.Event `json:"events"`
}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/sync"):
			_ = json.NewEncoder(w).Encode(SyncResponse{DesiredRevision: rev.ID, Revision: &rev})
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			if heartbeat != nil {
				_ = json.NewDecoder(r.Body).Decode(heartbeat)
			}
			w.WriteHeader(200)
		}
	}))
}

func k3sRev(id, manifest string) model.Revision {
	w := []model.RuntimeSpec{{Kind: "k3s", Name: "app", State: "running", Manifest: manifest}}
	return model.Revision{ID: id, Name: id, Workloads: w, Digest: model.ComputeRevisionDigest(w)}
}

func TestCycle_DigestVerification(t *testing.T) {
	good := k3sRev("rev-good", "apiVersion: v1\nkind: Pod")
	corrupt := k3sRev("rev-bad", "apiVersion: v1\nkind: Pod")
	corrupt.Workloads[0].Manifest = "kind: Evil" // body changed after the digest was computed
	legacy := k3sRev("rev-legacy", "apiVersion: v1\nkind: Pod")
	legacy.Digest = ""

	t.Run("matching digest applies", func(t *testing.T) {
		srv := digestServer(t, good, nil)
		defer srv.Close()
		r, st := newTestRunner(t, srv.URL)
		_ = st.Update(func(s *LocalState) { s.SiteID, s.AgentToken = "s", "t" })
		r.cycle(context.Background())
		if got := st.Snapshot().AppliedRevision; got != good.ID {
			t.Fatalf("applied %q", got)
		}
	})
	t.Run("legacy revision without digest still applies", func(t *testing.T) {
		srv := digestServer(t, legacy, nil)
		defer srv.Close()
		r, st := newTestRunner(t, srv.URL)
		_ = st.Update(func(s *LocalState) { s.SiteID, s.AgentToken = "s", "t" })
		r.cycle(context.Background())
		if got := st.Snapshot().AppliedRevision; got != legacy.ID {
			t.Fatalf("applied %q", got)
		}
	})
	t.Run("corrupt body is refused, not cached, reported", func(t *testing.T) {
		var hb struct {
			Events []model.Event `json:"events"`
		}
		srv := digestServer(t, corrupt, &hb)
		defer srv.Close()
		r, st := newTestRunner(t, srv.URL)
		_ = st.Update(func(s *LocalState) { s.SiteID, s.AgentToken = "s", "t" })
		dir := r.runtime.K3sManifestDir
		r.cycle(context.Background())
		snap := st.Snapshot()
		if snap.AppliedRevision != "" || snap.CachedRevision != nil {
			t.Fatalf("corrupt revision applied or cached: %+v", snap)
		}
		if snap.FailedRevision != corrupt.ID || !strings.Contains(snap.RevisionError, "digest") {
			t.Fatalf("failure not recorded: %q %q", snap.FailedRevision, snap.RevisionError)
		}
		if !hasEvent(hb.Events, "revision.digest_mismatch") {
			t.Fatalf("no mismatch event in heartbeat: %+v", hb.Events)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Fatalf("adapter touched the filesystem: %v", entries)
		}
		if _, err := os.Stat(filepath.Join(dir, "zyvor-fleet-app.yaml")); err == nil {
			t.Fatal("manifest written")
		}
	})
	t.Run("good cache is kept when a corrupt body arrives", func(t *testing.T) {
		srv := digestServer(t, corrupt, nil)
		defer srv.Close()
		r, st := newTestRunner(t, srv.URL)
		cached := good
		_ = st.Update(func(s *LocalState) { s.SiteID, s.AgentToken = "s", "t"; s.CachedRevision = &cached })
		r.cycle(context.Background())
		if c := st.Snapshot().CachedRevision; c == nil || c.ID != good.ID {
			t.Fatalf("good cache replaced: %+v", c)
		}
	})
	t.Run("tampered local cache is refused offline", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(503) }))
		defer srv.Close()
		r, st := newTestRunner(t, srv.URL)
		cached := corrupt
		_ = st.Update(func(s *LocalState) { s.SiteID, s.AgentToken = "s", "t"; s.CachedRevision = &cached })
		r.cycle(context.Background())
		if st.Snapshot().AppliedRevision != "" {
			t.Fatal("tampered cache was applied offline")
		}
	})
}
