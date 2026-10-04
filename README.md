<div align="center">

# Fleet

[![CI](https://github.com/zyvorai/zyvorai-fleet/actions/workflows/ci.yml/badge.svg)](https://github.com/zyvorai/zyvorai-fleet/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.27%2B-00ADD8.svg)](go.mod)
[![Version](https://img.shields.io/badge/version-0.3-informational)](docs/PRODUCT_PLAN.md)

[![Book a demo](https://img.shields.io/badge/Book_a_demo-0071e3?style=for-the-badge)](https://zyvor.dev/schedule?utm_source=github&utm_medium=fleet&utm_campaign=readme_hero)
[![30-day PoC](https://img.shields.io/badge/30--day_PoC-000000?style=for-the-badge)](https://zyvor.dev/poc?utm_source=github&utm_medium=fleet&utm_campaign=readme_hero)
[![Quickstart](https://img.shields.io/badge/Quickstart_one_Go_build-ffb340?style=for-the-badge)](#quickstart)

![Fleet — every site still running](docs/social/fleet-hero-dark.jpg)

### Every site. Still running.

**Offline-first edge fleet control plane for Linux, Kubernetes, containers and virtual machines.** The control plane declares what should run. A small `fleet-agent` pulls that desired state, caches the complete revision locally, and keeps reconciling it when the WAN disappears — with no arbitrary remote shell.

**Keeps reconciling offline** · **4 typed runtime adapters** · **No inbound port on the site** · **0 Go dependencies** · **Strict wave rollouts**

📖 **[Read the full docs](https://zyvorai.github.io/zyvorai-fleet/)** — tutorial, deployment, runtime adapters, and production runbooks.

</div>

---

```text
                       ZYVOR FLEET CONTROL PLANE
                     UI · API · RBAC · rollouts
                                  │
                      pull / heartbeat / replay
                                  │
              ┌───────────────────┼───────────────────┐
              │                   │                   │
          Site Pune           Site Delhi          Site Tokyo
          fleet-agent         fleet-agent         fleet-agent
              │                   │                   │
       ┌──────┼──────┐      ┌─────┼─────┐       ┌────┼─────┐
     systemd container k3s   k3s container      QEMU systemd GPU

        WAN down? Each agent continues from its cached desired revision.
```

## What's new

| Release | What landed |
|---|---|
| **Unreleased** | OTA devices roll out in canary waves instead of a single assignment, pausing when failure, rollback or offline thresholds trip, with a per-device timeline and recovery guidance |
| **Unreleased** | `fleetctl status`: logo, site and rollout health, feature rows; `fleetctl status json` for the raw dashboard |
| **Unreleased** | HA design doc and a short soak CI harness proving single-writer reconnect under WAN and disk pressure (not multi-writer HA) |
| **0.3.0** | Zyvor OTA contract: `/v1/devices/{device_id}/assignment` and `/events` with digest-bound device tokens |
| **0.3.0** | Site cordons, scoped API tokens, HMAC-SHA256 signed webhooks and a bounded mutation audit trail |
| **0.3.0** | State backup/restore drills, `make qualify`, and Cosign-signed release `SHA256SUMS` |

Full history: [CHANGELOG.md](CHANGELOG.md).

## Why Fleet

| When this happens… | Fleet gives you… |
|---|---|
| The WAN to a plant drops and nobody can say what the site is running | The agent caches the complete desired revision (`0600`) and keeps reconciling drift locally; events stay on disk and replay later |
| Managing remote sites means opening an inbound port or a remote shell | Pull-based agents with no inbound management port and no generic server-triggered shell |
| Every site mixes systemd services, containers, k3s and the odd VM | One revision across four typed adapters: `systemd`, `container` (Docker/Podman), `k3s` and opt-in `qemu` |
| A bad change hits every site at once | Strict wave rollouts with approval, windows, failure budgets, pause/resume/abort/retry and rollback |
| A site is being serviced and must not get the next rollout | Site cordons: the site keeps its last accepted state but is excluded from new rollout plans |
| CI needs to drive rollouts without a shared human password | Scoped API tokens (`read`, `sites:write`, `rollouts:write`, `admin`) stored only as SHA-256 digests, plus HMAC-signed webhooks |

![Capabilities at a glance: Declare, Roll out, Site, Operate](docs/ux/readme-capabilities.jpg)

## Capabilities

### Control plane

- Embedded Zyvor console (no CDN) · email/password + PBKDF2 · signed HttpOnly sessions · RBAC
- Enrollment tokens → independent per-site identities
- Live inventory, site cordons, scoped API tokens, HMAC-signed webhooks, bounded audit trail
- Declarative revisions, dynamic label groups, strict wave rollouts (approval, windows, failure budgets, pause/resume/abort/retry, rollback)
- Prometheus `/metrics` · single-writer atomic JSON persistence · one `fleetd` binary

### Offline-first agent

- Pull-based: no inbound management port on the site
- Complete desired revision cached locally (`0600`)
- Local reconciling continues through WAN loss; events queue and replay
- Per-site bearer identity (enrollment token is not reused)
- No generic server-triggered shell

### Typed runtime adapters

| Kind | Behavior | Safety boundary |
|---|---|---|
| `systemd` | Start/stop a named unit | Validated unit name; `systemctl` only |
| `container` | Run/replace/stop Docker or Podman | Declared image/env/ports/args only |
| `k3s` | Atomically maintain a k3s manifest | Validated name; explicit manifest content |
| `qemu` | Start/stop a basic KVM/QEMU VM | Disabled by default; absolute disk path required |

---

## Fleet vs balena

![Fleet vs balena: any Linux, any runtime, offline by design](docs/ux/readme-vs.jpg)

| | **Fleet** | **balena** |
|---|---|---|
| Device OS | The Linux you already run, with `fleet-agent` installed | balenaOS |
| Workload kinds | `systemd`, `container` (Docker/Podman), `k3s`, opt-in `qemu` | Containers |
| Control plane | `fleetd`, self-hosted (systemd, Compose or Helm) | balenaCloud (hosted), or self-hosted openBalena |
| Offline behavior | Cached revision stays authoritative; typed adapters keep reconciling drift; events replay on reconnect | A supervisor on each device runs the current release |
| Rollouts | Strict waves with approval, windows, failure budgets, rollback; OTA devices in canary waves | Release-based container updates |
| License | Apache-2.0 | Apache-2.0 agent and openBalena; balenaCloud is proprietary |
| HA control plane | Not yet — v0.3 is single-writer | Managed (balenaCloud) |
| **Choose balena when** | | Your fleet is containers on balenaOS and you want a managed cloud to run it |

## Is this for you?

Zyvor Fleet is a small, open-source (Apache-2.0) site lifecycle/desired-state control plane: declare what should run across systemd/container/k3s/QEMU targets, and a small on-site agent keeps reconciling — including fully offline — with no arbitrary remote shell. It is not a data/event plane (that's Nodra), not a general-purpose config management tool, and not a cloud-vendor device registry.

| | **Zyvor Fleet** | balena | Azure IoT Hub Device Mgmt | AWS IoT Device Mgmt | Rancher/Fleet (k8s) | Ansible/SaltStack |
|---|---|---|---|---|---|---|
| Primary scope | Cross-runtime site desired-state + offline reconciliation | Container fleet + OTA via balenaCloud | Cloud device twin/management | Cloud device fleet indexing/jobs | Kubernetes cluster/app fleet only | Generic config push, no offline reconciliation loop |
| Runtime targets | systemd, container (Docker/Podman), k3s, QEMU/KVM | balenaOS containers only | SDK-built | SDK-built | Kubernetes only | Anything reachable over SSH/agent |
| Offline autonomy | First-class — agent caches the full desired revision | Limited — designed around balenaCloud connectivity | Requires connectivity | Requires connectivity | Requires API server reachability | Push-based; no continuous offline loop |
| Cloud dependency | None required | balenaCloud (proprietary) | Azure IoT Hub | AWS IoT Core | Kubernetes API (self-hostable) | None, but no fleet control plane |
| License | Apache-2.0 | Apache-2.0 agent + proprietary cloud | Proprietary | Proprietary | Apache-2.0 | Open-source core + commercial tiers |
| HA control plane | Not yet — v0.3 is single-writer | Managed | Managed | Managed | Kubernetes-native HA | N/A |

*(General characterizations as of writing — verify against each project's own docs.)*

New here? [`docs/FAQ.md`](docs/FAQ.md) · [`docs/TROUBLESHOOTING.md`](docs/TROUBLESHOOTING.md)

---

## How it fits together

![The control plane declares; each site keeps going](docs/ux/readme-how-it-works.jpg)

```text
Browser
   │ HTTPS
   ▼
┌──────────────────────────────────────────────┐
│ fleetd                                       │
│ embedded web UI · REST API · rollout engine  │
│ auth / RBAC · single-writer store            │
└──────────────────┬───────────────────────────┘
                   │ outbound pull from sites
         ┌─────────┴─────────┐
         ▼                   ▼
    fleet-agent          fleet-agent
    typed adapters       typed adapters
```

v0.3 is deliberately a **single-writer** control plane. Kubernetes deploys one replica with a `ReadWriteOnce` volume. HA storage is a later milestone.

See [ARCHITECTURE.md](ARCHITECTURE.md) and [docs/V0.3_OPERATIONS.md](docs/V0.3_OPERATIONS.md).

### Offline autonomy

When sync fails:

1. The agent marks the connectivity transition once.
2. The cached revision remains authoritative locally.
3. Typed adapters continue drift reconciliation.
4. Events stay on disk.
5. When the control plane returns, queued events replay through the next heartbeat.
6. Any newer desired revision is then reconciled.

No cloud/control-plane call is necessary to keep the last accepted desired state alive.

---

## Quickstart

Requirements: Go 1.27+.

```bash
git clone https://github.com/zyvorai/zyvorai-fleet.git
cd zyvorai-fleet
make check && make build
```

### Start the control plane

```bash
export ZYVOR_FLEET_ADMIN_PASSWORD='zyvor-fleet-demo'
export ZYVOR_FLEET_SESSION_SECRET='local-demo-session-secret-change-me-1234567890'

./bin/fleetd --demo
```

Open **http://127.0.0.1:8080** — `admin@zyvor.local` / `zyvor-fleet-demo`. Demo enrollment token: `zf_enroll_demo-local-only`. `--demo` is not for production.

### Enroll a site

```bash
export ZYVOR_FLEET_ENROLLMENT_TOKEN='zf_enroll_demo-local-only'

./bin/fleet-agent \
  --server http://CONTROL_PLANE:8080 \
  --name factory-pune-01 \
  --region india-west \
  --labels class=factory,tier=production
```

### CLI

```bash
fleetctl status          # colorful logo; needs a running fleetd
fleetctl status json
fleetctl sites | events | revisions | rollouts | groups | audit | webhooks
fleetctl group-create "Production" env=production,class=factory
fleetctl rollout-plan GROUP_ID
fleetctl site-maintenance SITE_ID on "scheduled service"
fleetctl api-token-create github-ci operator read,rollouts:write
fleetctl enroll-token "Factory install"
```

Guided walkthrough: [docs/TUTORIAL.md](docs/TUTORIAL.md).

## Desired-state example

```json
{
  "name": "Factory stack 2026.09",
  "notes": "Promote edge API and maintain time sync",
  "workloads": [
    {"kind": "systemd", "name": "chronyd", "state": "running"},
    {
      "kind": "container",
      "name": "edge-api",
      "state": "running",
      "image": "ghcr.io/example/edge-api:1.4.0",
      "ports": ["8081:8080"]
    },
    {
      "kind": "k3s",
      "name": "local-inference",
      "state": "running",
      "manifest": "apiVersion: apps/v1\nkind: Deployment\n..."
    }
  ]
}
```

Create the revision in the UI, target sites or a dynamic group, preview the rollout plan, set wave/failure/approval/window policy and start. A strict wave does not advance until every site in the active wave has reported success or failure.

## Kubernetes

```bash
helm upgrade --install zyvor-fleet ./deploy/helm/zyvor-fleet \
  --namespace zyvor-fleet --create-namespace \
  --set admin.password='CHANGE_ME' \
  --set sessionSecret="$(openssl rand -base64 48)"

# Optional in-cluster node agent
helm upgrade --install zyvor-fleet ./deploy/helm/zyvor-fleet \
  --namespace zyvor-fleet --create-namespace \
  --set admin.password='CHANGE_ME' \
  --set sessionSecret="$(openssl rand -base64 48)" \
  --set agent.enabled=true \
  --set agent.enrollmentToken='YOUR_TOKEN'
```

Ordinary remote edge boxes: install `fleet-agent` on the site, not as a DaemonSet. Raw manifests: `kubectl apply -k deploy/kubernetes` (copy `secret.example.yaml` first).

Compose demo: `docker compose up --build` (control plane + two simulated sites).

## Security

- Explicit admin password for production bootstrap; session secret 32+ bytes
- HttpOnly + SameSite Strict cookies; scoped bearer tokens as SHA-256 digests only
- HMAC-SHA256 signed webhooks; bounded audit trail without request bodies
- State files `0600`; login throttling; CSP denies third-party scripts
- No arbitrary shell workload type; QEMU opt-in (`ZYVOR_FLEET_ALLOW_QEMU=1`)

See [SECURITY.md](SECURITY.md).

## Product plan

**v0.3** ships site cordons, scoped API tokens, signed webhooks, and a bounded audit trail. Next: signed artifacts, agent OTA, air-gap OCI bundles, enterprise identity, transactional HA storage — [docs/PRODUCT_PLAN.md](docs/PRODUCT_PLAN.md).

## Documentation

| Doc | Topic |
|---|---|
| [zyvorai.github.io/zyvorai-fleet](https://zyvorai.github.io/zyvorai-fleet/) | Product docs |
| [docs/TUTORIAL.md](docs/TUTORIAL.md) | Guided first rollout |
| [docs/FAQ.md](docs/FAQ.md) | Licensing, support, readiness |
| [docs/DEPLOYMENT.md](docs/DEPLOYMENT.md) | Systemd, Compose, Helm |
| [docs/RUNTIME_ADAPTERS.md](docs/RUNTIME_ADAPTERS.md) | Workload kinds |
| [ARCHITECTURE.md](ARCHITECTURE.md) | Protocol and failure model |
| [docs/PRODUCTION.md](docs/PRODUCTION.md) | Production runbook |
| [docs/OTA_CONTRACT.md](docs/OTA_CONTRACT.md) | OTA assignment + events |
| [docs/openapi.yaml](docs/openapi.yaml) | REST API |

Social assets: [docs/social/](docs/social/).

## Testing

```bash
make check && make test-race && make live-smoke && make qualify
```

CI: formatting, `go vet`, race tests, JS syntax, all binaries — plus rollout lifecycle, health gates, rollback/retry, and typed adapter drift.

---

## Maturity

> **Maturity (honest):** v0.3 is a serious single-writer release. A transactional HA storage adapter is a **future** milestone ([docs/PRODUCT_PLAN.md](docs/PRODUCT_PLAN.md)), alongside signed artifacts, agent OTA, and air-gap OCI bundles — not present yet. Device Agent and OTA contract surfaces are real (see below). Nodra relationship remains architectural — no code integration in this repository today.

**Integrations that ship today**

- **Device Agent** — opt-in `-device-agent-url` merges namespaced hardware metadata into heartbeats; failure never blocks a heartbeat.
- **Zyvor OTA** — `/v1/devices/{id}/assignment` and `/events` with digest-bound device tokens ([docs/OTA_CONTRACT.md](docs/OTA_CONTRACT.md)).

---

## Part of the Zyvor stack

Zyvor already has strong point products. Fleet is the **site lifecycle and desired-state layer** that connects them.

| Product | Role next to Fleet |
|---|---|
| **[Device Agent](https://github.com/zyvorai/zyvor-device-agent)** | Ships today: opt-in `-device-agent-url` merges hardware metadata into Fleet heartbeats |
| **[Zyvor OTA](https://github.com/zyvorai/ota)** | Ships today: Fleet serves the OTA device assignment and events contract |
| **[Nodra](https://github.com/zyvorai/nodra)** | Pairs with Fleet as the optional edge-data plane (architectural; no code integration in this repo) |
| **[Yard](https://github.com/zyvorai/yard)** | Ops console whose Fleet connector pulls sites, rollouts and OTA devices ([edge-stack suite](https://github.com/zyvorai/edge-stack)) |

The wider platform relationships, as Fleet sees them:

| Product | Owns | Fleet relationship |
|---|---|---|
| **Nodra** | MQTT/HTTP edge ingress, store-and-forward, twins, local routes | Optional edge-data plane |
| **Zyvor Relay** | Durable operational action/ack/verify workflow | Optional action bus |
| **PacketWolf** | eBPF network intelligence | Optional network health/policy evidence |
| **Argus** | Application assurance | Optional post-rollout verification |
| **Forge** | GPU/inference operations | Optional edge AI plane |
| **HyperCluster** | Kubernetes cluster lifecycle | Fleet can coordinate site-level promotion |
| **IronWolf** | Bare-metal lifecycle | Fleet can represent/target the resulting sites |
| **Zeus OS / Machina** | VM operations | Fleet handles cross-site desired-state rollout |

Fleet intentionally does **not** reimplement Nodra's device/event data plane or Fabric's private-cloud VM control plane.

→ [zyvor.dev](https://zyvor.dev)

---

## License and support

Fleet is **free and open source** under the [Apache License, Version 2.0](LICENSE). Personal, lab, and commercial production use at no charge, subject to Apache-2.0 (preserve notices / NOTICE where required). See [NOTICE](NOTICE).

**Zyvor Enterprise** adds what production teams ask for: supported releases, deployment and upgrade guidance, priority incident triage, a named technical contact and 24x7 critical intake. Plans and terms: [docs/SUBSCRIPTION-MODEL.md](docs/SUBSCRIPTION-MODEL.md) · [Pricing](https://zyvor.dev/pricing?utm_source=github&utm_medium=fleet&utm_campaign=readme_license) · [sales@zyvor.dev](mailto:sales@zyvor.dev).

See [SECURITY.md](SECURITY.md) and [CONTRIBUTING.md](CONTRIBUTING.md).

---

<div align="center">

### Keep every site running, even when the WAN is not

[![Book a demo](https://img.shields.io/badge/Book_a_demo-0071e3?style=for-the-badge)](https://zyvor.dev/schedule?utm_source=github&utm_medium=fleet&utm_campaign=readme_footer)
[![30-day PoC](https://img.shields.io/badge/Start_a_30--day_PoC-000000?style=for-the-badge)](https://zyvor.dev/poc?utm_source=github&utm_medium=fleet&utm_campaign=readme_footer)
[![Pricing](https://img.shields.io/badge/Pricing-1d1d1f?style=for-the-badge)](https://zyvor.dev/pricing?utm_source=github&utm_medium=fleet&utm_campaign=readme_footer)
[![Contact sales](https://img.shields.io/badge/Contact_sales-2997ff?style=for-the-badge)](mailto:sales@zyvor.dev?subject=Fleet)
[![Star on GitHub](https://img.shields.io/github/stars/zyvorai/zyvorai-fleet?style=for-the-badge&logo=github&label=Star&color=2997ff)](https://github.com/zyvorai/zyvorai-fleet)

</div>
