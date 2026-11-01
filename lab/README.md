# Local Gitea / GARM integration lab

This lab runs the real GARM external-provider protocol, real Agent37 VMs, and a synthetic private Gitea repository. It does not deploy to host1. Every test VM has one ephemeral runner and is deleted after one job. Cloud creation spends account credit.

The current workspace reuses the Docker Gitea lab in the sibling `garm-provider-boat` repository. That name is historical; Boat is not involved in these tests. Gitea listens on `127.0.0.1:13000`, native GARM on `127.0.0.1:19997`. Keep `.state/` private: it contains credentials, tokens, database, and diagnostic logs. It is ignored by Git.

## Prepare the existing workspace

Prerequisites: Go 1.26.2+ for the pinned GARM build (the provider itself needs 1.25+), a C compiler for SQLite, Python 3, Docker, OpenSSH, Node.js 20+, an Agent37 workspace with the VM runtime enabled, and an API key stored in a private file. Use the matching GARM source and compatibility patch described in the project README.

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

`build` uploads only four allowlisted files, and does not pass the API key to the build. `status` saves the returned revision/digest privately. Inspect the successful revision and pin it in the pool; `lab/manage.py pool` currently selects `garm-gitea-runner@1`. Rebuilding creates a new revision, so deliberately update that pin. Ubuntu base, Docker components, and runner versions are pinned; other apt dependencies resolve at build time and are recorded in `/opt/garm/packages.txt`. A published template revision freezes the resulting image. Preloaded Docker job images do not populate a separate Buildx builder's cache.

## Run the proof

```sh
python3 lab/manage.py pool
python3 lab/integration.py
python3 lab/integration.py --expect-failure
```

`pool` loads the current custom install template and leaves the pool disabled. Each integration run:

1. Installs a synthetic three-job workflow and creates one temporary workspace SSH key.
2. Enables a pool with zero idle workers and a two-VM cap, then dispatches three jobs.
3. Establishes temporary SSH reverse forwards to the laptop for each owned VM. The lab uses host networking inside job containers so their loopback Gitea URL reaches the tunnel.
4. Waits for two running jobs and one queued, restarts GARM, and verifies the same two workers remain.
5. Creates a repository tag to release the job gate, then requires three distinct runner/VM identities and automatic scale-to-zero. The optional failure variant expects the third shard to fail and still requires VM deletion.
6. Disables the pool, verifies cleanup, revokes its temporary SSH key, and stops its tunnel watcher. On failure it also invokes the provider's scoped cleanup command.

This tunnel watcher is lab infrastructure, not part of the production provider. Production needs routable Gitea/GARM endpoints or an explicit private network. Use bridge networking in production unless a workload has a reviewed need for host networking.

Private results are written to `lab/.state/integration.json` and `cleanup.json`; subsequent runs overwrite those names. Preserve a named copy when comparing runs. The sanitized acceptance record is in [RESULTS](../docs/RESULTS.md).

```sh
python3 lab/manage.py status
python3 lab/manage.py stop
```

Stopping GARM is not cloud cleanup. If a test was interrupted before its `finally` cleanup, disable the pool and run `python3 -c 'import sys; sys.path.insert(0,"lab"); import integration; integration.cleanup()'`, then verify status. Inspect any cleanup error; do not assume a timeout means an instance was deleted. Leave Docker volumes intact. The controller normally stays running between tests; stop it when finished.
