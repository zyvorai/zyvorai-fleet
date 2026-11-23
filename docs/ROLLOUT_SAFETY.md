# Rollout safety: digests, diffs, canaries and site timeouts

These four controls make a rollout inspectable before it starts and bounded
while it runs. They add no new agent capability: the agent still applies only
typed runtime specs, and nothing here opens an inbound port or a shell.

## Revision content digest

Every revision carries `digest`: `sha256:` plus the hex SHA-256 of the
canonical JSON of its ordered `workloads`. Name, notes, id and timestamps are
excluded, so two revisions that declare the same workloads share a digest.

- The control plane computes it at creation and fills it in for revisions
  created before digests existed (stored state is not rewritten).
- The agent recomputes it on every sync and before every reconcile. A body that
  does not match is **not cached over a good copy and not handed to an
  adapter**. The agent records `failedRevision` plus an error, and queues a
  `revision.digest_mismatch` event once per distinct failure. The rollout engine
  then counts the site as failed against the failure budget.
- This also covers the offline path: a cached revision that was edited on disk
  is refused during autonomous reconciliation.
- A rollout request may carry `revisionDigest`. It is rejected with `409`
  unless it equals the revision digest, so CI can promote exactly what it tested.

!!! warning "Integrity, not authenticity"
    The digest detects corruption and accidental or partial modification in
    transit or in a cached file. It is **not a signature**. Anyone who can
    rewrite both a revision and its digest, including a compromised control
    plane, can forge a matching pair. Signed artifacts remain on the roadmap
    (see the product plan) and are not implemented.

## Revision diff and plan change summary

`GET /api/v1/revisions/diff?from=REV&to=REV` (any authenticated role) returns
added, removed and changed workloads keyed by `kind` + `name`. Changed fields
are listed **by name only** (for example `image`, `env.LOG_LEVEL`,
`manifest`); values are never echoed. Omit `from` (CLI: `-`) to compare with an
empty site.

`POST /api/v1/rollouts/plan` with a `revisionId` adds `changes`: one entry per
distinct current revision among the targets (at most 20, `changesTruncated`
says if more exist) with the site count and the added/removed/changed workload
counts. The console shows this in **Preview plan**.

```bash
fleetctl revision-diff REV_OLD REV_NEW
fleetctl revision-diff - REV_NEW          # versus an empty site
fleetctl rollout-plan GROUP_ID REV_NEW
```

## Canary wave

`canarySize` sizes the first wave only; later waves use `waveSize`. The usual
strict-wave rules apply: the next wave does not start until every activated
site is terminal, then `pauseSeconds` elapses. It must be between 0 and the
target count and cannot be combined with `strategy: all-at-once`. The plan
response reports the resulting `waves` count.

## Per-site apply timeout

Without a timeout, one offline site in a strict wave holds the rollout forever.
`siteTimeoutSeconds` (0 disables, otherwise 30 to 604800) fails an activated
site that has not applied the revision that long after its wave started:

- the site is added to `failedSites` and `timedOutSites`, counts against
  `maxFailures`, and a `rollout.site_timeout` event is recorded;
- with the budget intact the rollout moves on; if the budget is exceeded the
  rollout fails and `autoRollback` applies as usual;
- a site that later applies the revision is removed from both lists;
- the clock restarts on resume and retry, so time spent paused or awaiting an
  operator is not charged to a site.

A timed-out site keeps its cached desired state and keeps reconciling locally;
the timeout is a control-plane accounting decision, not a command to the edge.

## Limits

| Input | Bound |
|---|---|
| `canarySize` | 0 to target count |
| `siteTimeoutSeconds` | 0 or 30 to 604800 |
| diff `from`/`to` | 128 characters each |
| plan `changes` | 20 distinct current revisions |
| revision | 100 workloads, 1 MiB body (existing) |
