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

## Failure budgets, retries and windows

Four further controls, researched against AWS IoT Jobs, Eclipse hawkBit, Mender and
Azure IoT Edge (see `docs/RESEARCH.md`). All are optional and none gives the agent a
new capability.

- **`maxFailurePercent`** (1..99): a second failure budget as a share of *all*
  target sites. The rollout fails when failures exceed `maxFailures` **or** this
  percentage. 2 of 10 sites is exactly 20 percent, not over a 20 percent budget.
- **`siteRetries`** (0..10) and **`retryDelaySeconds`** (0..3600, requires
  `siteRetries`): a site that reports failure or times out is held, not counted,
  for the delay; then its failure is cleared, its timeout clock re-armed, and
  `rollout.site_retry` is emitted. The agent re-applies on its next cycle. Once
  retries are used up the failure counts against the budgets. Retry state is
  reported in `retries`; a manual `retry` resets it.
- **`dailyWindow`** (`HH:MM-HH:MM`, UTC, may wrap midnight, e.g. `22:00-02:00`):
  new waves only start inside the window; sites already activated keep going. The
  pause between waves starts counting once the window is open. Not valid with
  `all-at-once`. Always evaluated in UTC; there is no DST handling.
- **Manual advance**: `POST /api/v1/rollouts/{id}/advance` (CLI
  `fleetctl rollout-advance ID`, console *Advance wave*) starts the next wave now,
  ignoring the pause, the window and still-running sites. hawkBit added the same
  manual trigger because a wave can wait forever on offline devices. Only valid for
  a running rollout with sites left; the failure budgets still apply afterwards.

## Convergence report

`GET /api/v1/convergence[?revisionId=REV]` (any authenticated role) answers "is the
fleet where we wanted it?", using Azure IoT Edge's Targeted / Applied / Reporting
Success / Reporting Failure vocabulary. Per revision: `targeted` (sites desiring it),
`applied`, `failed`, `pending`, `offline` (targeted, not applied, offline), `stale`
(sites still running it while desiring something else) and `convergedPercent`. With
`revisionId`, site ids per bucket are included. Sites with no desired revision are
counted in `unassigned`.

```bash
fleetctl convergence                 # table for every revision in use
fleetctl convergence REV_ID          # adds the site ids per bucket
fleetctl convergence REV_ID json     # raw JSON
```

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
