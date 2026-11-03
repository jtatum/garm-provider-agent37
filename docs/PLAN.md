# Agent37 provider plan

**Status: lab acceptance passed on 2026-11-01; trusted production activated on 2026-11-02.** See [results](RESULTS.md), including the required local GARM compatibility patch.

Build a GARM external provider for Linux amd64 Gitea runners on fresh Agent37 VMs. Keep the controller responsible for demand, registration metadata, webhooks, and runner reconciliation. Each VM serves at most one job and is then deleted. Trusted repositories are the initial deployment scope.

## Implementation

- API client: separate control/exec authentication, bounded requests, sanitized errors, no blind create retries.
- Provider: stable controller/provider/pool ownership metadata, name-based recovery, private durable creation journal, cross-process capacity lock, create/readiness/bootstrap/delete, scoped inventory and cleanup.
- Bootstrap: authenticated GARM install-template download, non-systemd supervisor, ephemeral registration, capacity one, per-job exit, callbacks, and no re-registration after completion or ambiguous interruption.
- Image: preserve the tested clean template recipe and immutable revision. No registration or repository secrets in image builds.
- Packaging: configuration examples, reproducible build commands, unit tests, lab scripts, operational recovery and cleanup instructions.

## Acceptance and test plan

1. Unit and protocol checks: separate API auth, redirects, malformed/error responses, context cancellation, ownership refusal, pool filters, capacity concurrency, duplicate create recovery, bootstrap failure cleanup, and JSON-only stdout.
2. Bootstrap checks: token files private and removed, terminal markers, callback order, identity scoped to one worker, repeated requests cannot register a second runner, process restart fails closed.
3. Local integration: pinned GARM and Docker Gitea with synthetic private repository, custom install template, min idle zero, max two VMs; no manual runner registration.
4. Dispatch three gated jobs. Observe exactly two running jobs and a third queued; restart the GARM controller while those jobs run. Release the gate and require three successes on three distinct runner and VM identities.
5. Confirm automatic deletion and scale to zero, then disable the lab pool and verify no owned cloud VMs, Gitea runners, or temporary SSH keys remain. Preserve the local Gitea data.
6. Record failures honestly and resolve implementation defects before marking acceptance complete. Unit tests cover failure modes not deliberately induced against paid infrastructure.

A later [twenty-job, ten-VM acceptance run](experiments/ten-concurrency.md) passed with the same lifecycle assertions. A [90-job burst at a 45-worker cap](experiments/fortyfive-concurrency.md) also passed. The harness supports up to 45 concurrent VMs and 100 jobs, with exact allocation reported separately.

## Initial boundaries

- Gitea/Linux/amd64 custom template; GitHub, Windows, ARM, agent mode and arbitrary cloud-init provisioning are not v1 acceptance claims.
- VM stop/resume is not a supported job-reuse mechanism. A stopped/crashed/completed worker is replaced, not re-registered.
- The provider's configured cap is an allocation limit, not a billing cap. Keep the account quota and wallet separate; never cancel a running job to satisfy a spending estimate.
- Single GARM installation with shared provider state directory. Multiple controllers or independent state directories require their own distributed admission design.
- The lab uses temporary authenticated SSH reverse forwards for laptop reachability. Production requires routable Gitea/GARM endpoints or a deliberate private-network solution.
- The trusted production rollout is now active; see [production scope and limitations](experiments/production.md).

## Follow-on work

- Completed: [37 selected trusted checks](experiments/trusted-workload.md), including application builds and artifact transfer, with a pinned cached Ubuntu job image.
- Completed: [deterministic surplus-allocation reproduction](experiments/garm-surplus-reproduction.md). GARM explicitly anticipates unused workers under job reassignment; pool-cap enforcement and idle-only scale-down passed. Keep start overhead visible as an efficiency metric.
- Completed: [first production burst measurements](experiments/production.md). Startup/queueing dominate the many short checks. Keep fresh VMs and the current template; cache A/B testing and a matched Kubernetes comparison remain future work.
- Completed: optional Tailscale networking and a single-worker private DNS/HTTPS probe across host, Docker, and BuildKit.
- Completed: published/pinned controller image, production credentials/private networking, registration/callback/artifact/registry canaries, 45 simultaneous jobs, and post-merge automatic cleanup.
- Publish release binaries after the real-workload pilot; track the [GARM completion fix](https://github.com/cloudbase/garm/pull/939) upstream.
- Expand failure/reconciliation testing for missed or reordered webhooks, longer jobs and live capacity refusals before a wider rollout.
