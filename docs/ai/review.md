# AI review (fleet)

_Generated 2026-10-20 by claude-opus-5-5 from a snapshot of tracked sources; verify before acting._

# Code Review: `fleet` (Zyvor Fleet v0.3)

## Summary

Fleet is a Go, offline-first, pull-based edge control plane with three binaries:

- **`fleetd`**: control plane with embedded web UI, REST API, RBAC, rollouts, webhooks, audit, and an OTA device contract. Storage is a single-writer JSON file store.
- **`fleet-agent`**: enrolls the site, caches the desired revision, and reconciles it through typed adapters (systemd, container, k3s, qemu), including while the WAN is down.
- **`fleetctl`**: admin CLI.

Deployment assets cover Compose, raw Kustomize, Helm, systemd, and an SSH deploy script. CI is extensive, but several "qualification" scripts prove less than they claim.

## Bugs and correctness risks

### Deploy script

- **`scripts/deploy-remote.sh`, `patch_unit_listen` reads its own script.**
  - `grep -qE '^ExecStart=.*--listen'` has no file argument.
  - The remote script is fed to `bash` via a heredoc on stdin, so this `grep` consumes the remainder of the script from stdin.
  - Result: on the existing-unit redeploy path, the later steps (`daemon-reload`, `restart`, …) likely never execute.
  - Fix: pass `"$unit"` to `grep`.

### OTA ACK handling (`internal/server/ota_contract.go`, `otaPostEvents`)

- **The ACK can move backwards.**
  - The code computes the contiguous `acked` value, then clamps it to `events[len(events)-1].Sequence`.
  - Replaying an older batch therefore lowers the ACK. Example: `AckedSeq=5`, device replays `[1,2]` → `AckedSeq` becomes `2`.
  - The
