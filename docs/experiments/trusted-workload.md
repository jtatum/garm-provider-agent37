# Trusted workload lab

On **2026-11-01**, all **37 selected checks passed** using local Docker Gitea, GARM, and fresh Agent37 VMs at a cap of ten. This included application unit tests, mocked agent unit tests, manifest and policy checks, two Docker image builds, and artifact upload/download. The strict allocation assertion **did not pass**: GARM requested 38 VMs for 37 successful jobs. One surplus idle worker registered, took no job, and was automatically deleted.

| Measurement | Observed result |
| --- | --- |
| Successful jobs / distinct runners | 37 / 37 |
| VMs created | 38, including one unused worker |
| Peak allocated VMs / running jobs | 10 / 7 |
| Dispatch through last job completion | 244.447 seconds |
| Dispatch through automatic scale-to-zero | 248.982 seconds |
| Wallet difference during full run | $0.024440, approximately 2.4 cents |
| Median cached job-image load | 15.353 seconds per VM |
| Maximum sampled guest memory used | 1.789 GiB |
| Maximum sampled guest disk used | 13.136 GiB |

[Interactive timeline](trusted-workload.html) and [sanitized aggregate observations](trusted-workload.json) are saved beside this report. There were 243 guest resource samples across 38 VMs. These are periodic observations, not guaranteed peaks. Wallet difference is the observation for this run only, not a monthly cost prediction; no other workspace VMs were present. Earlier image builds, debugging attempts and the smoke run are excluded from that cost. Account billing settings were not changed.

## Scope

The lab imported a private source snapshot and selected 37 of 44 jobs across 22 workflows. It preserved workflow source as inactive fixtures for contract tests. Only explicit dispatch workflows were active. Ordinary checks and builds ran; actual agent invocations, Renovate, operational alerts, registry publication, and automated follow-up PR creation were excluded. Image builds used local output with publication disabled. No production secrets or production deployment were involved. Source, transformation details and raw job logs remain private.

A preceding three-job smoke run passed at cap two in 127.912 seconds through cleanup. The full suite exercises dependency ordering and real action/dependency downloads. It does not establish a sustained ten-way CPU-heavy build throughput or a speedup relative to existing Kubernetes runners; there is no equivalent baseline measurement yet.

## Image and startup

Every VM used 4 vCPU, 8 GB RAM and a 20 GB disk in region `us`. The clean Ubuntu 24.04 template includes Docker 29.1.3, Gitea runner 3.5.0, and an archived public Ubuntu job image. No repository source or credentials enter the reusable template. The tested template digest is `sha256:56a4103a71ff42be66bcbce26cd9f0046f86a0a331dee40930ea32c8060bd502`; workspace template names are account-local. The source job-image pin is recorded in the aggregate JSON.

Loading the job image into the fresh Docker store adds approximately 15 seconds per VM at the observed median. A preloaded job image does not populate a separate Buildx builder cache. Dependency downloads and builds still perform their own work. The observed disk footprint supports retaining the 20 GB lab size pending further measurements.

An initial smoke attempt failed image identity verification: Docker's containerd image store reports a manifest digest where the old check expected a configuration digest. The corrected helper verifies the archived configuration against its pinned registry digest, then verifies the loaded platform, filesystem layers and runtime configuration. The failed workers and invalid template were cleaned up; both subsequent smoke and full workloads used the corrected image. Resource sampling also requires a unique API execution name per probe to avoid replaying cached results; the full run uses unique names, while the smoke resource time series is excluded.

## Surplus allocation finding

GARM logs show a runner created for a queued job taking a different job. GARM then requested another worker for the original job while other workers were still starting. That final worker registered idle and never received work. Creation began at 17:42:58.424 UTC, idle scale-down began at 17:43:18.422, and GARM removed its record at 17:43:25.015.

A subsequent [deterministic reproduction](garm-surplus-reproduction.md) confirmed this sequence on unchanged upstream GARM. Its best-effort webhook scheduler explicitly anticipates surplus workers; an exact one-start-per-job total is not guaranteed. The harness retains its failed exact-allocation assertion; all job conclusions, the ten-VM cap and automatic cleanup passed. This is an efficiency finding, and no runtime GARM patch is proposed.

## Cleanup and reproduction

Independent final verification found zero Agent37 VMs, temporary SSH keys, Gitea runners and GARM instance records. The pool is disabled, the original two-VM configuration is restored, and GARM is stopped. Local Gitea data and the verified template remain available.

See [the lab guide](../../lab/README.md#cache-a-larger-job-image) for building a template with a pinned job image and selecting it in the pool. Match provider and pool caps, set provider/GARM bootstrap timeouts for measured image startup, and install a reviewed dispatch-only workflow in the private lab repository. Then use:

```sh
python3 lab/workload_run.py --workflow reviewed-workload.yaml \
  --concurrency 10 --jobs 37 --record workload-full --timeout 1200
```

The generic harness records private evidence, downloads job logs, requires automatic scale-to-zero, and performs scoped cleanup on failure or interruption. It does not audit or rewrite workflows. Reproducing the exact private suite requires its private source snapshot and transformation manifest; the public aggregate data deliberately contains neither source nor job logs.
