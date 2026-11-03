# Live acceptance results

Verified on **2026-11-01** against the real Agent37 API, the real GARM external-provider protocol, and Docker Gitea's private `lab-admin/runner-proof` lab repository. These lab results describe provider behavior under the stated conditions; validate networking, job images and capacity for each deployment.

## Initial lab: passed end to end

| Case | Gitea run | Job outcomes | Fresh VMs / runner identities | Maximum observed VMs | Automatic scale to zero |
| --- | --- | --- | --- | --- | --- |
| Successful burst | 10 | 3 successes | 3 / 3 | 2 | Passed |
| Deliberate job failure | 11 | 2 successes, 1 intentional failure | 3 / 3 | 2 | Passed |

Both runs began with no workers. The harness observed two running jobs and a third queued, restarted GARM while those jobs were gated, and verified the same workers survived. After release, GARM deleted finished workers and provisioned the third fresh VM. Each job cloned the private fixture repository using its workflow token and checked workspace/container canaries. No manual runner registration or external scale-down step was used to satisfy the passing test assertions.

The harness observed two running jobs after 6.94 seconds in the success case and 5.50 seconds in the failure case. Dispatch through verified automatic scale-to-zero took 29.64 and 34.78 seconds respectively. These are two small gated lab runs with polling and laptop reverse tunnels, **not** latency percentiles or representative application build benchmarks. The deliberate failure is an expected workflow conclusion, not a failed acceptance run.

Final verification of that initial lab found **zero workspace VMs, zero temporary SSH keys, zero Gitea runner registrations, and zero GARM instance records**. The lab pool was then disabled and the native GARM test process stopped. The local Gitea data and reusable workspace template were retained for further tests.

## Required GARM compatibility patch

The first run (Gitea run 9) exposed an upstream issue: Gitea's completed-job event retained the runner ID and start time but had a null runner name after ephemeral deregistration. GARM ignored that completion, leaving two workers active and the third job queued. Emergency cleanup removed those two VMs and the temporary key; that failed run is not counted as acceptance.

The included [patch](../patches/garm-gitea-ephemeral-completion.patch) restores a missing completion name from the recorded assignment only when the entity, repository, job ID, run ID, runner ID, and nonzero start time match. Mismatches reject before persistence. Existing pool ownership checks remain in effect. GARM does not currently persist `run_attempt`; matching runner ID and execution start is the conservative check used here. The fix is submitted upstream as [this pull request](https://github.com/cloudbase/garm/pull/939); it is not yet merged.

The tested controller is base commit `33ec0921418a470ca7e00dfb20ae111595da24e1` plus that patch. Patch SHA256: `089a4e80fda3d8a99399eaae68118ef2989ab739aeba0a5a0777b4d129e01a34`. For a clean checkout at the exact baseline:

```sh
git -C /path/to/garm apply --check /path/to/garm-provider-agent37/patches/garm-gitea-ephemeral-completion.patch
git -C /path/to/garm apply /path/to/garm-provider-agent37/patches/garm-gitea-ephemeral-completion.patch
```

The bootstrap was also corrected after the first test: GARM deliberately rejects instance callbacks once installation reaches idle. Idle is now the final callback. Runtime completion comes from Gitea webhooks, and local terminal markers retain diagnostics. GARM authentication was not weakened.

## Versions and artifacts

- Provider dependency: `v0.1.10-0.20261010221645-ec82f622d2d4`.
- Gitea: `28.2.0-rootless`, container digest `sha256:94807314454f6525994a3b76286ba12bc7e7f8cd14f61d9e67431767f8103306`.
- Custom workspace image: `garm-gitea-runner@1`, digest `sha256:fa7d2c2afd50d8be68a3c2b92de4e957475d06bdc8a15543aeef26bae281f495`.
- Linux amd64, Ubuntu 24.04, Gitea runner 3.5.0, Docker 29.1.3, containerd 2.1.5, Compose 2.40.3, Buildx 0.30.1, cached Alpine 3.22.
- VM size: 4 vCPU, 8 GB RAM, 20 GB disk, region `us`; auto-sleep disabled. Pool/provider cap two, minimum idle zero.
- Local toolchain: Go 1.27.3 on macOS arm64, Python 3.13; image Python 3.12.

[Sanitized machine-readable results](results.json) record run/job/VM identities, timings, version pins and cleanup. Private operational state stays in ignored `lab/.state/`. [Lab instructions](../lab/README.md) cover rebuilding the image, reloading the template, running both tests, and cleanup. [Operations](OPERATIONS.md) covers journal recovery and production networking.

## Verification and remaining work

Provider race tests, bootstrap Python tests and Go vet passed via `make check`. GARM's focused regression and full pool race suite passed; the full pool race run took 57.542 seconds. Formatting, GARM package vet and whitespace checks passed. The provider also cross-built successfully as a static Linux amd64 executable; its live controller-side execution was on macOS arm64. Regression coverage includes duplicate create recovery, ambiguous create quarantine, capacity coordination, ownership refusal, transient readiness, cancellation/bootstrap-failure deletion, redacted errors, and one-shot bootstrap behavior.

A subsequent [twenty-job burst at ten concurrent VMs](experiments/ten-concurrency.md) also passed, including restart recovery and automatic cleanup.

A subsequent [37-check trusted workload](experiments/trusted-workload.md) passed all jobs using a cached Ubuntu job image. GARM requested one surplus idle worker, so the exact-allocation assertion remains failed; automatic cleanup and the cap of ten passed. A [deterministic reproduction](experiments/garm-surplus-reproduction.md) confirmed this is anticipated best-effort scheduling behavior, not a provider duplicate-create defect. Private source and raw logs are retained outside this repository.

A [90-job, 45-worker burst](experiments/fortyfive-concurrency.md) passed after fixing a separate create/completion race. A [single-worker private-networking probe](experiments/private-networking.md) passed host, Docker, and BuildKit DNS/HTTPS checks.

The lab evidence does not establish long-term workload reliability or a generally optimal BuildKit cache strategy. Remaining validation includes account quota/funding exhaustion under live load; missed or reordered forge-event recovery; multiple controllers; untrusted-fork policy; production release/upgrade procedures. VM reuse, cooldown and spending enforcement are not implemented. Account billing settings are operator-managed. The configured VM cap is not a billing cap.
