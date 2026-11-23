// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: LicenseRef-Zyvor-Production-2.0

package server

import (
	"net/http"
	"reflect"
	"sort"

	"github.com/zyvorai/fleet/internal/model"
)

// maxDiffGroups bounds how many distinct "from" revisions a rollout plan
// summarises, so a plan over a very mixed fleet stays a small response.
const maxDiffGroups = 20

type workloadRef struct {
	Kind  string `json:"kind"`
	Name  string `json:"name"`
	State string `json:"state,omitempty"`
}

type workloadChange struct {
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	Fields []string `json:"fields"`
}

// RevisionDiff describes what an agent would do differently under "to" than
// under "from". Changed fields are reported by name only: values (env vars,
// manifests) are never echoed, so a diff cannot leak more than field names.
type RevisionDiff struct {
	From       string           `json:"from"`
	To         string           `json:"to"`
	FromDigest string           `json:"fromDigest,omitempty"`
	ToDigest   string           `json:"toDigest,omitempty"`
	Identical  bool             `json:"identical"`
	Added      []workloadRef    `json:"added"`
	Removed    []workloadRef    `json:"removed"`
	Changed    []workloadChange `json:"changed"`
}

// DiffWorkloads compares two ordered workload lists keyed by kind+name. When a
// revision lists the same kind+name twice, the last entry wins (the same order
// an agent would reconcile them in).
func DiffWorkloads(from, to []model.RuntimeSpec) (added, removed []workloadRef, changed []workloadChange) {
	added, removed, changed = []workloadRef{}, []workloadRef{}, []workloadChange{}
	index := func(in []model.RuntimeSpec) map[string]model.RuntimeSpec {
		m := make(map[string]model.RuntimeSpec, len(in))
		for _, w := range in {
			m[w.Kind+"\x00"+w.Name] = w
		}
		return m
	}
	a, b := index(from), index(to)
	for k, w := range b {
		old, ok := a[k]
		if !ok {
			added = append(added, workloadRef{Kind: w.Kind, Name: w.Name, State: w.State})
			continue
		}
		if f := changedFields(old, w); len(f) > 0 {
			changed = append(changed, workloadChange{Kind: w.Kind, Name: w.Name, Fields: f})
		}
	}
	for k, w := range a {
		if _, ok := b[k]; !ok {
			removed = append(removed, workloadRef{Kind: w.Kind, Name: w.Name, State: w.State})
		}
	}
	sort.Slice(added, func(i, j int) bool { return refLess(added[i], added[j]) })
	sort.Slice(removed, func(i, j int) bool { return refLess(removed[i], removed[j]) })
	sort.Slice(changed, func(i, j int) bool {
		if changed[i].Kind != changed[j].Kind {
			return changed[i].Kind < changed[j].Kind
		}
		return changed[i].Name < changed[j].Name
	})
	return
}

func refLess(x, y workloadRef) bool {
	if x.Kind != y.Kind {
		return x.Kind < y.Kind
	}
	return x.Name < y.Name
}

func changedFields(a, b model.RuntimeSpec) []string {
	out := []string{}
	add := func(name string, equal bool) {
		if !equal {
			out = append(out, name)
		}
	}
	add("state", a.State == b.State)
	add("image", a.Image == b.Image)
	add("args", reflect.DeepEqual(nilSlice(a.Args), nilSlice(b.Args)))
	add("ports", reflect.DeepEqual(nilSlice(a.Ports), nilSlice(b.Ports)))
	add("cpus", a.CPUs == b.CPUs)
	add("memoryMiB", a.MemoryMiB == b.MemoryMiB)
	add("disk", a.Disk == b.Disk)
	add("manifest", a.Manifest == b.Manifest)
	add("health", reflect.DeepEqual(a.Health, b.Health))
	keys := map[string]bool{}
	for k := range a.Env {
		keys[k] = true
	}
	for k := range b.Env {
		keys[k] = true
	}
	for k := range keys {
		av, aok := a.Env[k]
		bv, bok := b.Env[k]
		if aok != bok || av != bv {
			out = append(out, "env."+k)
		}
	}
	sort.Strings(out)
	return out
}

func nilSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return in
}

func findRevision(st model.State, id string) (model.Revision, bool) {
	for _, r := range st.Revisions {
		if r.ID == id {
			return withDigest(r), true
		}
	}
	return model.Revision{}, false
}

// withDigest fills the digest of a revision created before digests existed so
// every consumer sees one. The stored state is not modified.
func withDigest(r model.Revision) model.Revision {
	if r.Digest == "" {
		r.Digest = model.ComputeRevisionDigest(r.Workloads)
	}
	return r
}

func diffRevisions(from, to model.Revision) RevisionDiff {
	added, removed, changed := DiffWorkloads(from.Workloads, to.Workloads)
	return RevisionDiff{From: from.ID, To: to.ID, FromDigest: from.Digest, ToDigest: to.Digest,
		Identical: len(added)+len(removed)+len(changed) == 0, Added: added, Removed: removed, Changed: changed}
}

// GET /api/v1/revisions/diff?from=REV&to=REV. "from" may be empty to diff
// against an empty site (everything is added).
func (s *Server) diffRevisionsHandler(w http.ResponseWriter, r *http.Request) {
	st := s.store.Snapshot()
	toID, fromID := r.URL.Query().Get("to"), r.URL.Query().Get("from")
	if toID == "" || len(toID) > 128 || len(fromID) > 128 {
		writeError(w, 400, "to is required and ids must be at most 128 characters")
		return
	}
	to, ok := findRevision(st, toID)
	if !ok {
		writeError(w, 404, "revision not found: to")
		return
	}
	var from model.Revision
	if fromID != "" {
		if from, ok = findRevision(st, fromID); !ok {
			writeError(w, 404, "revision not found: from")
			return
		}
	}
	writeJSON(w, 200, diffRevisions(from, to))
}

type planChange struct {
	FromRevision string `json:"fromRevision"`
	Sites        int    `json:"sites"`
	Identical    bool   `json:"identical"`
	Added        int    `json:"added"`
	Removed      int    `json:"removed"`
	Changed      int    `json:"changed"`
}

// planChanges summarises, per current revision, what the rollout would change
// for the target sites. Sites with no known revision group under "".
func planChanges(st model.State, to model.Revision, targets []string) ([]planChange, bool) {
	counts := map[string]int{}
	order := []string{}
	for _, id := range targets {
		for _, site := range st.Sites {
			if site.ID != id {
				continue
			}
			cur := site.AppliedRevision
			if cur == "" {
				cur = site.DesiredRevision
			}
			if _, seen := counts[cur]; !seen {
				order = append(order, cur)
			}
			counts[cur]++
			break
		}
	}
	truncated := len(order) > maxDiffGroups
	if truncated {
		order = order[:maxDiffGroups]
	}
	out := make([]planChange, 0, len(order))
	for _, from := range order {
		var fr model.Revision
		if from != "" {
			fr, _ = findRevision(st, from)
		}
		d := diffRevisions(fr, to)
		out = append(out, planChange{FromRevision: from, Sites: counts[from], Identical: d.Identical, Added: len(d.Added), Removed: len(d.Removed), Changed: len(d.Changed)})
	}
	return out, truncated
}
