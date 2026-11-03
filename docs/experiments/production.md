# Production acceptance and first burst measurements

Production routing was verified on **2026-11-02**. This is a trusted-repository
pilot using fresh Linux amd64 VMs, one job per VM, a 45-worker limit and zero
minimum idle workers. It is not evidence for untrusted forks or VM reuse.

## Production acceptance

- A 45-job canary reached **45 simultaneously executing jobs**, all successful.
  Both GARM and cloud inventories returned to zero after automatic cleanup.
- Private clone/API access, ephemeral enrollment, authenticated callbacks,
  default/custom Docker networks, BuildKit, artifacts, and registry push/pull
  passed through the Tailscale gateway. Canary package tags were removed.
- The post-merge canary passed, and the final activation check found the enabled
  pool at 45/0 with zero GARM records and zero owned cloud VMs.
- Normal checks, image builds, publication and digest automation now use the
  pool. The Renovate bot and Claude agent invocations stay on existing runners;
  their configuration/unit tests can use Agent37.

Tested controller source: `e32fb37673b3b0fcee71fd5d4e69f3a448bb1600`.
Controller image: `ghcr.io/jtatum/garm-agent37-controller:e32fb37673b3b0fcee71fd5d4e69f3a448bb1600@sha256:824b8f8c11127da33ad1f32b27c3764579aee2fe93ade3308fc068df2e476e68`.
Workers: 4 vCPU, 8 GB RAM, 20 GB disk, region `us`, pinned network-enabled
workspace template, cached Ubuntu job image, auto-sleep disabled.

## First ordinary-workload sample

Window: **2026-11-02 02:52 through 2026-11-03 10:50 UTC** (31 h 58 m).
The window starts after activation verification. It contains 111 workflow runs
and 318 executed Agent37 job attempts: 316 successes and two failures. Excluding
three canary/artifact-canary executions leaves **315 ordinary attempts**.

| Ordinary workload | Observed value |
| --- | ---: |
| Successful / failed attempts | 313 / 2 |
| Renovate-related attempts | 233 |
| Execution duration, median / p95 | 6 s / 34 s |
| Job creation-to-start, median / p95 | 56 s / 230 s |
| Peak simultaneously executing jobs | 19 |
| Peak tracked worker records, sampled every 30 s | 45 |

One failure was a real generated-deployment/template image-pin mismatch detected
by a test. The other Flux check reported failure after 644 seconds; all its steps
have the same terminal timestamp and its original log is unavailable from Gitea.
Its retry passed. **The second failure's cause remains unclassified**; this report
does not claim flawless runner reliability or omit failed attempts from timing.

The largest ordinary burst created 174 jobs on Renovate branches around
11:19–11:20 UTC on November 2; the last finished at 11:25:38.
It reached the 45-record capacity cap and 19 simultaneous executing jobs.
Creation-to-start p95 was 237 seconds. The 45-job acceptance canary was a separate,
deliberately overlapping workload; short real checks need not all execute together.

| GARM histogram observation | Mean | Approximate p95 |
| --- | ---: | ---: |
| Record creation → provider reports running | 48.4 s | 112.3 s |
| Record creation → runner ready | 45.2 s | 106.2 s |
| First GARM job record → job starts | 98.1 s | 275.7 s |
| Departing state → record deleted | 11.0 s | 27.6 s |

These are reset-aware Prometheus estimates across the full sample, including
canaries. Readiness and provisioning use independent event streams: a runner can
be observed ready before the provider call returns. Histogram p95 values interpolate
coarse buckets and are not exact per-VM percentiles. Job timing histograms have
owner scope, without repository or provider labels.

## Cache and template decision

Keep fresh VMs and the current template for now. The dominant opportunity in this
sample is provisioning/queue overhead around very short checks. First compare
bundling a few related tiny validations into one job while retaining independent
failures in the test output. Keep the cap at 45; this sample does not model what
another cap would have done.

- The cached default job container initialized in **0.28 s** in a representative
  Flux job. Its full successful execution took 29 s; successful Flux executions
  in this sample ranged from 21 to 53 s. One separate failed attempt took 644 s,
  so successful timing alone does not justify changing the ten-minute timeout.
- The current Renovate 44.194.0 grouping test passed in all 14 executions:
  median 33 s, maximum 128 s. Two sampled logs spent about **12–13 s** initializing
  the uncached Renovate container. The 128 s run enabled additional resident-release
  tests; the difference was not a 100 s image pull. Retain its five-minute timeout.
  Baking this frequently changing image into every worker is not yet justified.
- A representative publishing job spent about **2.1 s** pulling BuildKit,
  **17 s** on its first image build, and **8 s** building/publishing afterward in
  the same builder. An OpenShell smoke build took 53 s. This is a small, mixed
  sample, not a cold/warm-cache experiment.
- If builds become the bottleneck, test a separate registry-backed BuildKit cache
  per application/platform: controlled main-branch writes, read-only consumption
  for PR builds, and no concurrent writers to one cache ref. Measure total job
  time including cache import/export, not only cached build steps.
  Docker documents [registry cache import/export](https://docs.docker.com/build/cache/backends/registry/)
  and [cache scoping and overwrite behavior](https://docs.docker.com/build/cache/backends/).

## Measurement limits and reproduction

Query Gitea workflow runs created inside the window, then fetch each run's jobs
and **every attempt** via `/actions/runs/{run}/attempts/{attempt}/jobs`. Select the
`garm-agent37` label, exclude skipped jobs, and retain failures. A partial rerun
copies successful jobs with a new creation time but the old execution timestamps;
exclude those carry-forward records and count their original attempt instead.
The source sample has no remaining executions crossing the window boundary.

Execution is `completed_at - started_at`. API creation-to-start is
`started_at - created_at` and **includes workflow dependency waiting**; it is not
pure runner queue time. Percentiles use nearest rank. Peak execution uses half-open
start/end intervals, with completion processed before a simultaneous start.
Burst groups split after a gap of more than five minutes between job creations.
Renovate-related means the triggering actor is Renovate or the PR branch starts
with `renovate/`; this includes build-generated digest PRs.

Prometheus uses 30-second samples, deduplicated by worker name, and `increase`
per counter series before aggregation. Counters can miss their first event and
transitions across a controller restart; they are not exact audit counts. Every
30-second evaluation point had a successful scrape series in this window, but
short-lived worker states can still be missed.

The sampled integral was **3.66 provider-running record-hours** and **9.83 total
tracked record-hours**, versus 1.14 hours of summed job execution including
canaries. Neither integral is billed VM time: creation/cleanup state transitions
and short lifetimes differ from Agent37's billing boundaries. Do not price this
sample from these values or extrapolate a monthly bill from two days of bursts.
About 43 idle-scale-down decisions were observed; these are not duplicate-create
proof or a ledger of completed deletions.

Private API payloads, raw logs, identities and credentials are not published here.
The accompanying [aggregate JSON](production.json) retains the window, denominators
and calculations. Long-term reliability, cache A/B testing, live quota exhaustion,
missed-webhook recovery, multi-controller coordination and untrusted forks remain
unvalidated.
