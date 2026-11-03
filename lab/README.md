# Local Gitea / GARM integration lab

This lab runs the real GARM external-provider protocol, real Agent37 VMs, and a synthetic private Gitea repository. Use a dedicated test repository and pool, separate from production workloads. Each test VM hosts at most one ephemeral job and is then deleted; GARM may also create and retire an unused idle worker. Cloud creation spends account credit.

The current workspace reuses the Docker Gitea lab in the sibling `garm-provider-boat` repository. That name is historical; Boat is not involved in these tests. Gitea listens on `127.0.0.1:13000`, native GARM on `127.0.0.1:19997`. Keep `.state/` private: it contains credentials, tokens, database, and diagnostic logs. It is ignored by Git.

## Prepare the existing workspace

Prerequisites: Go 1.26.2+ for the pinned GARM build (the provider itself needs 1.25+), a C compiler for SQLite, Python 3.11+ for the lab, Docker, OpenSSH, Node.js 20+, an Agent37 workspace with the VM runtime enabled, and an API key stored in a private file. Use the matching GARM source and compatibility patch described in the project README.

From this repository:

```sh
make build
mkdir -p lab/.state/bin
chmod 700 lab/.state
(cd ../garm && go build -mod=vendor -tags osusergo,netgo,sqlite_omit_load_extension -o ../garm-provider-agent37/lab/.state/bin/garm ./cmd/garm)
docker compose -f ../garm-provider-boat/lab/compose.yaml up -d --wait
(cd ../garm-provider-boat && python3 lab/bootstrap.py && python3 lab/proof.py setup)
python3 lab/manage.py configure --key-file /absolute/path/to/private-agent37-key
python3 lab/manage.py start
python3 lab/manage.py setup
```

The existing lab is already initialized. Do not overwrite its private credentials or remove its Docker volumes. `configure` defaults to the sibling lab's credentials file; `--gitea-credentials` overrides it. Setup creates the repository credential, GARM repository association, and `workflow_job` webhook. It reconciles an existing hook if GARM reports an error after Gitea has already created it.

Install the pinned Agent37 CLI in `lab/.state/agent37-cli/package` for the SSH proxy. The tested version is `agent37@0.4.1`; its executable must be `package/bin/agent37.js`. Use an isolated npm package download/extraction, not a globally replaced CLI. It receives the API key only in its scoped environment; the secret is never an SSH argument or sent to the VM.

## Custom image

The workspace already has `garm-gitea-runner@1`. This is a workspace-owned template, not a public built-in template. A different workspace must build its own.

The image source is `images/runner/`. It boots Docker without systemd, imports a public Alpine job image, probes Docker, and writes `/root/garm-image/ready.json`. No registration or repository credentials are baked in. The provider waits for this readiness marker before bootstrap.

To recreate the excluded image archive:

```sh
docker pull --platform linux/amd64 alpine@sha256:2e0b81d60597f449baa56dc25d355367869d2dca4cd57e866f1e2b52af3eb55f
docker tag alpine@sha256:2e0b81d60597f449baa56dc25d355367869d2dca4cd57e866f1e2b52af3eb55f garm-template-cache/alpine:3.22
docker image save --platform linux/amd64 -o images/runner/alpine.tar garm-template-cache/alpine:3.22
shasum -a 256 images/runner/alpine.tar
```

The tested archive SHA256 is `3c1b545ef346526439a40ab87bf252f21c0e5591e3d181d63bd0a6436a745bb3`. Serialization can differ by Docker version. If it does, inspect the image's amd64 architecture and expected rootfs layer (`be64570a34e4c5e361d1f5d9cb42b851e7f9a33a61f91c335b287c1c7868f921`) before reviewing/updating the archive checksum in the Dockerfile. The entrypoint checks the architecture/layer again at boot.

```sh
python3 lab/image.py build --name garm-gitea-runner
python3 lab/image.py status
```

`build` uploads only five allowlisted files, and does not pass the API key to the build. `status` saves the returned revision/digest privately. Inspect the successful revision and pin it in the pool; `lab/manage.py pool` currently selects `garm-gitea-runner@1`. Rebuilding creates a new revision, so deliberately update that pin. Ubuntu base, Docker components, and runner versions are pinned; other apt dependencies resolve at build time and are recorded in `/opt/garm/packages.txt`. A published template revision freezes the resulting image. Preloaded Docker job images do not populate a separate Buildx builder's cache.

## Cache a larger job image

The build helper can add one digest-pinned Linux amd64 job image to the reusable VM image:

```sh
python3 lab/image.py build --name garm-gitea-ubuntu \
  --job-image REGISTRY/IMAGE@sha256:DIGEST
python3 lab/image.py status
```

This option requires local Docker Buildx to inspect the public registry manifest. The cloud builder fetches the image by its platform digest with Skopeo; the upload remains the same five-file allowlist. No repository source or credentials enter the reusable image. The build verifies the archive configuration against the registry configuration digest. At boot, Docker loads the archive and verifies its platform, filesystem layers, and runtime configuration before the readiness marker is written. This works with both classic and containerd image stores, whose image IDs differ. Large archives increase readiness time and disk usage; measure both before setting bootstrap deadlines.

The private `image-build.json` record contains `cached_job_image.reference`, the local cache tag used by the runner. Select that tag and the published template revision explicitly:

```sh
python3 lab/manage.py pool --concurrency 2 \
  --image garm-gitea-ubuntu@REVISION --job-image garm-template-cache/job:CONFIG_PREFIX
```

A local cache tag is verified against the pinned source configuration on each boot. It avoids a new registry pull for the job container; it does not prewarm Buildx caches or dependencies downloaded by a workflow.

## Run the proof

```sh
python3 lab/manage.py pool
python3 lab/integration.py
python3 lab/integration.py --expect-failure
```

`pool` loads the current custom install template and leaves the pool disabled. Both `configure` and `pool` accept `--concurrency` (default 2, maximum 45); set the same value on `integration.py`. `--jobs` defaults to concurrency plus one and can be increased up to 100. The [ten-VM test](../docs/experiments/ten-concurrency.md) used 20 jobs. The harness verifies both caps and available workspace capacity before dispatch. Each integration run:

1. Installs a synthetic workflow with the requested job count and creates one temporary workspace SSH key.
2. Enables a pool with zero idle workers and the configured VM cap, then dispatches the jobs.
3. Establishes temporary SSH reverse forwards to the laptop for each owned VM. The lab uses host networking inside job containers so their loopback Gitea URL reaches the tunnel.
4. Waits for the configured number of running jobs and the remaining jobs queued, restarts GARM, and verifies the same workers remain.
5. Creates a repository tag to release the job gate, then requires one distinct runner/VM identity per job and automatic scale-to-zero. The optional failure variant expects the last shard to fail and still requires VM deletion.
6. Disables the pool, verifies cleanup, revokes its temporary SSH key, and stops its tunnel watcher. On failure it also invokes the provider's scoped cleanup command.

This tunnel watcher is lab infrastructure, not part of the production provider. Production needs routable Gitea/GARM endpoints or an explicit private network. Use bridge networking in production unless a workload has a reviewed need for host networking.

Private results are written to `lab/.state/integration.json` and `cleanup.json`; subsequent runs overwrite those names. Preserve a named copy when comparing runs. The sanitized acceptance record is in [RESULTS](../docs/RESULTS.md).

```sh
python3 lab/manage.py status
python3 lab/manage.py stop
```

Stopping GARM is not cloud cleanup. If a test was interrupted before its `finally` cleanup, disable the pool and run `python3 -c 'import sys; sys.path.insert(0,"lab"); import integration; integration.cleanup()'`, then verify status. Inspect any cleanup error; do not assume a timeout means an instance was deleted. Leave Docker volumes intact. The controller normally stays running between tests; stop it when finished.

## Measure reviewed workload jobs

For an already installed, reviewed dispatch workflow in the private lab repository, the workload harness records job status and timings, owned VM counts, guest CPU/memory/disk samples, image readiness, wallet observations, and private job logs:

```sh
python3 lab/workload_run.py --workflow reviewed-workload.yaml \
  --concurrency 2 --jobs 3 --record workload-smoke --timeout 1200
```

Match both pool/provider caps to `--concurrency` first. The harness checks available account slots and starts from a disabled zero-idle pool with no workers. It requires the declared number of jobs to finish and the pool to scale to zero, then disables the pool and revokes its temporary SSH key. Failed jobs or a mismatch between expected jobs, distinct runners and observed VMs produce a failed result even when cleanup succeeds. The exact VM-start total is deliberately strict: [GARM can create surplus idle workers](../docs/experiments/garm-surplus-reproduction.md), so inspect an allocation-only failure separately from job, identity, capacity or cleanup failures. On interruption it cancels the run and invokes scoped cleanup. Check the saved cleanup result and GARM records before another attempt.

This command executes the installed workflow; it does not rewrite or audit it. Use a private source snapshot and explicit dispatch triggers. Review agent calls, live operational actions, publication steps, and production credential requirements before importing an existing workflow into the lab. Keep source snapshots and raw logs in ignored private state. The public provider repository contains no private workload source.

Guest samples are periodic observations, so short-lived peaks may be missed. Cached job-image startup, action downloads, dependency downloads, and Buildx work are separate costs. Keep workload execution and queue/startup measurements separate when comparing runs.

At higher concurrency, GARM can provision surplus workers while queued jobs change ownership. The harness requires unique runner identities, the configured VM cap, restart recovery, successful jobs, and automatic cleanup. It records `allocation_exact` and `surplus_vm_count` separately; exact one-VM-per-job allocation is not guaranteed by GARM.
The post-release deadline is ten minutes: GARM halves surplus idle capacity on each one-minute scale-down pass, so a large burst can need several passes after the final job.
