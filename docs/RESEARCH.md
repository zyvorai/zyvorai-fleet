# Edge and fleet-management research

Scope: what comparable systems do about rollout safety, drift and update
integrity, what Zyvor Fleet lacked, and what was built from it. Date of
research: 2026-11-23. Every statement below was read from a fetched page unless
it is marked **[U]** (unverified: recalled from general knowledge or a search
snippet only, not read from the page).

## Boundaries that filtered the ideas

From ARCHITECTURE.md and CONTRIBUTING.md: sites initiate all communication; no
arbitrary shell primitive; the edge keeps working through WAN loss; the console
needs no CDN or third-party JavaScript; no new dependencies (standard library
only); Fleet is not a data/event plane, a Kubernetes distribution or a
hypervisor; persistence is honestly single-writer. Ideas needing a registry, a
signing PKI, inbound connections or multi-writer storage were rejected for now.

## What the field does

| System | Relevant behaviour | Source |
|---|---|---|
| OpenGitOps | Four principles: declarative, versioned and immutable, pulled automatically, continuously reconciled | https://opengitops.dev/ |
| Rancher Fleet | GitRepo scans produce Bundles; a BundleDeployment is the per-cluster instance with that cluster's customisation; targeting by cluster labels | https://fleet.rancher.io/explanations/concepts (rollout strategy and drift pages listed in its nav, not read) |
| Argo CD | Phases (PreSync, Sync, PostSync) and integer sync waves; a failure in a phase fails the whole operation | https://argo-cd.readthedocs.io/en/stable/user-guide/sync-waves/ |
| Eclipse hawkBit | Cascading groups: success threshold starts the next group, error threshold triggers emergency shutdown; approval workflow is opt-in. Release notes record that a group can be stuck when too many targets are offline, fixed by a manual trigger | https://eclipse.dev/hawkbit/concepts/rollout-management (search snippets only; page fetch redirected) |
| AWS IoT Jobs | Constant or exponential rollout rate, abort criteria by failure percentage, in-progress timers that mark an execution TIMED_OUT, bounded retries (max 10), scheduling windows | https://docs.aws.amazon.com/iot/latest/developerguide/jobs-configurations-details.html |
| AWS IoT Greengrass | Per-deployment failure policy `ROLLBACK` or `DO_NOTHING`; the same job abort, rate and timeout configuration; warns that a deployment can uninstall components absent from the new one | https://docs.aws.amazon.com/greengrass/v2/developerguide/create-deployments.html |
| Azure IoT Edge | Automatic deployments with continuous target conditions, integer priority, layered deployments, built-in Targeted/Applied/Success/Failure metrics, phased rollout by widening the target condition | https://learn.microsoft.com/en-us/azure/iot-edge/module-deployment-monitoring |
| Uptane | Director versus image repository cross-check; version numbers must not decrease and metadata must not expire; image hash and length must match metadata | https://uptane.org/docs/2.0.0/standard/uptane-standard (first 100k of 103k characters read) |
| Mender | Phased rollouts with time-delayed phases and per-phase device share, per-device retries, manual abort via API. No fleet-level automatic abort threshold found | https://docs.mender.io/overview/deployment, https://mender.io/blog/managing-fleets-of-connected-devices-with-phased-rollout (search snippets only) |
| Open Horizon, Balena, KubeEdge, Akri, Flux | Not researched beyond name recognition. Open Horizon: agreement-based policy placement **[U]**. Balena: delta image updates and release pinning **[U]**. KubeEdge: edge autonomy for Kubernetes nodes **[U]**. Akri: leaf-device discovery for Kubernetes **[U]**. Flux: Git/OCI pull reconciliation **[U]** | none fetched |

## What Fleet already did well

Pull-only agents, offline autonomy from a cached revision, strict waves with
approval, windows, pause/resume/abort/retry and rollback, failure budgets,
site cordons, scoped API tokens, signed webhooks. That matches or exceeds the
simple tiers of hawkBit, Mender and Greengrass on rollout control, and its
offline posture is stronger than any cloud-hub design above.

## Gaps found

1. A strict wave waits on a site forever if that site is offline or hung. hawkBit
   documents the same failure mode; AWS IoT closes it with execution timeouts.
   Fleet had no per-site deadline.
2. Nothing told an operator what a rollout would change before starting it.
   Argo CD and Fleet (Rancher) are built around showing a diff.
3. Revisions were mutable-by-trust: no content identity, so neither the agent,
   a pipeline nor an auditor could assert that "what was tested is what runs".
   OpenGitOps asks for versioned and immutable desired state; Uptane verifies
   hashes before install.
4. The first wave was the same size as every other wave. A real canary needs a
   smaller first step (AWS exponential rate, Mender phase shares).
5. Not built (see below): layered deployments, priority-based overlays,
   exponential wave growth, success-percentage thresholds, scheduling
   recurrence windows, signed metadata, fleet convergence metrics.

## Ranked ideas

| Rank | Idea | Rationale | Decision |
|---|---|---|---|
| 1 | Per-site apply timeout | Fixes a real stall, mirrors AWS IoT and hawkBit lessons, fits the failure-budget model, no edge change | Built |
| 2 | Revision content digest, agent-verified, rollout pin | Gives immutability and integrity per OpenGitOps and Uptane's hash check; small, dependency-free | Built, labelled integrity-only |
| 3 | Revision diff and plan change summary | Operators see the blast radius before activation; values never echoed | Built |
| 4 | Canary wave size | Cheap, matches AWS and Mender phased models | Built |
| 5 | Fleet convergence report (targeted, applied, failed, stale; Azure style metrics) | Useful, mostly a read model over existing fields | Not built; next |
| 6 | Success-percentage group thresholds (hawkBit) | Needs a decision on offline sites that Fleet now partly answers with timeouts | Not built |
| 7 | Layered or priority deployments (Azure IoT Edge) | Large model change, conflicts with one revision per site | Rejected for now |
| 8 | Signed revisions (Uptane-style roles) | Needs key management and a trust root; belongs with the signed-artifacts milestone, would be dishonest to fake with a bare hash | Rejected for now |
| 9 | Recurring maintenance windows (AWS) | Existing start/end window covers one-shot use; recurrence needs timezone handling | Not built |
| 10 | Remote shell or push-based agent control | Violates the security model | Rejected |

## Round 2 (2026-11-24)

Pages fetched and read: Azure IoT Edge "Monitor IoT Edge deployments", AWS IoT
Jobs "How job configurations work" (rollout rates, abort thresholds, recurring
maintenance windows, retries capped at 10 per device), Mender's "deployment
automation" post (per-device retry count, retries tracked per device) and balena's
release-policy page (fleet versus device pinning). hawkBit pages redirected or
returned 404, so its threshold and manual-trigger behaviour is taken from search
results only **[U]**.

| Idea | Source | Decision |
|---|---|---|
| Fleet convergence report (targeted, applied, failed, stale) | Azure IoT Edge metrics | Built (`/api/v1/convergence`, `fleetctl convergence`) |
| Percentage failure budget | AWS abort config, hawkBit error threshold | Built (`maxFailurePercent`) |
| Bounded per-site retries with delay | AWS (max 10), Mender | Built (`siteRetries`, `retryDelaySeconds`) |
| Recurring window | AWS recurring maintenance window | Built as a UTC daily window; no cron, no timezones |
| Manual next-wave trigger | hawkBit **[U]** | Built (`advance`) |
| Release pinning per device | balena | Not built: Fleet already pins per site through cordons and explicit site lists |
| Exponential rollout rate | AWS | Not built |
| Success-percentage trigger thresholds | hawkBit | Not built; strict waves plus `advance` cover the stall case |

## Sources not read

Open Horizon, Balena, KubeEdge, Akri, Flux and Mender's own documentation
pages (404 on one path) were not read. Treat all claims about them as
unverified.
