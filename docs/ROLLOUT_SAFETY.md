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
  `rollout.site_retry` is emitted. The agent re-applies on its next cycle, and
  failure reports are ignored until it has synced again. Once
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

## Healthy soak

Without a soak a site completes the moment it reports the revision applied, and a
health check that starts failing afterwards does not affect the rollout.
`minHealthySeconds` (0 disables, up to 86400) changes that for sites the rollout
activates:

- the period starts at the first report of "applied, not failed";
- the site completes only when a heartbeat at least that long after the start
  still says so. A silent or offline site is never counted healthy;
- a failure report during the period emits `rollout.site_unhealthy`, counts
  against the failure budgets (or consumes a retry), and the period restarts;
- while soaking, a site keeps its wave open and is listed in `soakingSites`;
- with `siteTimeoutSeconds`, a soaking site has the soak plus the timeout to
  confirm before it is failed.

`pauseSeconds` is separate: it is the gap after a wave has finished.

## Change freezes

A freeze stops rollouts from moving sites to a new revision between `startAt`
(default now, inclusive) and `endAt` (exclusive, at most 366 days later).

```bash
fleetctl freeze-create "Peak season" 72 env=production "retail peak"
fleetctl freeze-create "Audit" 8 all
fleetctl freeze-create "Line 3" 4 sites:site_abc,site_def
fleetctl freezes
fleetctl freeze-delete frz_abc
```

- Scope is explicit: `allSites`, or any mix of `siteIds`, `groupId` and a label
  `selector` (union). Group and label membership is evaluated live. A group used
  by an unexpired freeze cannot be deleted. Relabelling a site can take it out
  of a freeze, and that is allowed for operators.
- Only admins create and delete freezes. Rollouts are still accepted. Frozen
  sites are skipped; when nothing else can start, the rollout shows
  `heldByFreeze` and emits `rollout.held`. `advance` returns `409`.
- With rings, a frozen site holds the later rings.
- Not blocked: rollback (manual and automatic), abort, sites already activated,
  automatic per-site retries, and the edge's own drift reconciliation. A manual
  `retry` that would change a frozen site's desired revision returns `409`.
- Break glass: an admin may create a rollout with `overrideFreeze` and
  `overrideReason`; it is recorded as `rollout.freeze_override`.
- A held rollout continues at the next control-plane reconcile after the freeze
  ends (any agent sync or heartbeat); the pause between waves then applies.
- Freezes do not extend `endAt`: a rollout can expire while it waits.

## Ring-ordered waves

`ringLabel` plus `ringOrder` (up to 20 label values) orders targets by ring:

```json
{"ringLabel": "ring", "ringOrder": ["canary", "pilot", "production"], "waveSize": 20}
```

A wave never spans two rings, and the next ring starts only when no activated
site is still in progress. `canarySize` applies to the very first wave. Sites
with no listed value form a last, unlisted ring, so none is silently dropped.
Membership is fixed when the rollout is created. `advance` overrides the wait
for running sites but still fills one ring per wave.

## One rollout per site

A site has one desired revision, so two unfinished rollouts sharing it overwrite
each other. Creating such a rollout returns `409` with `conflicts` (also listed
by the plan). `conflictPolicy`:

| Value | Effect |
|---|---|
| `reject` (default) | Refuse. |
| `supersede` | Abort every conflicting rollout (all of its sites, not only the shared ones) and start. Not allowed with approval or a future start. |
| `allow` | Create anyway. |

Rollouts whose time windows cannot overlap (one has `endAt` before the other's
`startAt`) do not conflict. A paused or never-approved rollout still counts:
abort it or supersede it.

## Per-site status

```bash
fleetctl rollout-sites rol_abc          # table
fleetctl rollout-sites rol_abc json
```

`GET /api/v1/rollouts/{id}/sites` reports one state per target: `completed`,
`soaking`, `applying`, `retry_wait`, `failed`, `timed_out`, `held`, `waiting`,
or `not_started` (the rollout ended first), with ring, retries, timestamps and
the agent's error text.

## Limits

| Input | Bound |
|---|---|
| `canarySize` | 0 to target count |
| `siteTimeoutSeconds` | 0 or 30 to 604800 |
| diff `from`/`to` | 128 characters each |
| plan `changes` | 20 distinct current revisions |
| revision | 100 workloads, 1 MiB body (existing) |
| `minHealthySeconds` | 0 to 86400 |
| `ringOrder` | 20 values, 128 characters each |
| freezes | 200 stored (expired ones dropped first), 366 days each |
