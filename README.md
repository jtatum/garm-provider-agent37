# garm-provider-agent37

A GARM external provider for fresh Agent37 VM runners running Gitea Actions. GARM handles job demand and runner reconciliation; this provider creates a VM from a pinned workspace template, waits for Docker and the cached job image, launches GARM's runner installation script, and deletes the VM when GARM retires it. Each worker registers an ephemeral runner with capacity one and runs `daemon --once`.

**Experimental: Gitea runners for trusted repositories.** GitHub runners are not implemented yet. See [operations](docs/OPERATIONS.md) for setup and recovery, [acceptance results](docs/RESULTS.md) for tested behavior and limitations, and [the project/test plan](docs/PLAN.md) for the roadmap.

## Compatibility

- Provider interfaces: GARM `v0.1.0` and `v0.1.1`.
- Provider dependency: `garm-provider-common v0.1.10-0.20261010221645-ec82f622d2d4`, pinned in `go.mod` to match the tested GARM API, including proxy settings.
- Tested GARM controller and Gitea setup: see [integration notes](docs/RESULTS.md#required-garm-compatibility-patch) for the exact revision and setup requirements.
- Runner: Gitea runner `3.5.0`; guest OS/architecture: Linux amd64. The provider runs on Linux or macOS and uses local filesystem locks. The lab Gitea image is version `28.2.0`.

## Build and check

Use Go 1.25 or newer and Python 3.11+ for the lab and bootstrap tests. Building the pinned GARM baseline separately requires its own Go 1.26.2 minimum.

```sh
make help
make build
make check
```

The binary is `bin/garm-provider-agent37`. `make check` runs race-enabled Go tests, the Python bootstrap contract tests through Go, the lab helper tests, and `go vet`. GitHub Actions runs these checks and builds on Linux and macOS, checks Go formatting and Python syntax, and verifies a static Linux amd64 build. CI uses mock APIs and does not create cloud VMs or require credentials. The [controller image recipe and smoke test](images/controller/README.md) package GARM, this provider, and its runner template. After merge, the manual publication workflow tags images by source commit.

## Image contract

Build an Agent37 workspace template from [`images/runner`](images/runner), then pin the published revision in the GARM pool, for example `garm-gitea-runner@1`. Workspace images are private to your Agent37 workspace; that name is not a globally available image. Unpinned names and built-in `agent37-*` images are rejected by the provider.

The recipe contains Docker, Compose, Buildx, a checksum-verified Gitea runner, and an archived Alpine job image. It starts Docker with data under the persistent `/root` disk and writes `/root/garm-image/ready.json` only after its offline container test passes. The provider requires that marker, a working Docker daemon, and the runner executable before bootstrap. No runner identity, repository, or credentials are baked into the image.

The cache archive is generated locally and excluded from Git. See [image preparation](docs/OPERATIONS.md#image-preparation) for its pinned source and checksum, and [the lab guide](lab/README.md) for the build helper and integration commands. The recipe retains the tested versions; review version pins when preparing each new template revision. Docker job-image preloading does not populate a separate Buildx builder's cache.

## Private networking

Optional [Tailscale worker networking](tailscale/README.md) enrolls each VM with a single-use ephemeral key and supplies private DNS to the VM, Docker, and BuildKit. It requires a dedicated controller OAuth client, a restricted tailnet gateway, and the network-enabled runner image. Networking remains disabled by default.

## Configure GARM

Place the Agent37 API key in a private file readable only by the GARM service identity. Install the binary, copy [`examples/provider.toml`](examples/provider.toml), and create its persistent `state_dir` with mode `0700`. All invocations of this provider namespace must share that same directory.

Add this to GARM's configuration, adjusting paths:

```toml
[[provider]]
name = "agent37"
provider_type = "external"
description = "Fresh Agent37 Gitea workers"
disable_jit_config = true
[provider.external]
provider_executable = "/opt/garm/providers.d/garm-provider-agent37"
config_file = "/etc/garm/provider-agent37.toml"
exec_timeout_seconds = 300
```

If GARM runs in a container, mount the binary, key file, provider configuration, and durable state directory into that container. No environment-variable passthrough is required. The Agent37 API key stays on the controller; the guest receives only its GARM instance token and the registration credentials needed for its own runner.

Create the supplied GARM install template; the default systemd-based template is not suitable for these VMs:

```sh
garm-cli template create \
  --name agent37-gitea \
  --description "Fresh Agent37 VM Gitea runner" \
  --forge-type gitea --os-type linux \
  --path templates/gitea-agent37.py.tmpl
```

After configuring the Gitea endpoint, credentials, repository, and workflow-job webhook in GARM, create a disabled pool for initial verification:

```sh
garm-cli pool add \
  --repo OWNER/REPOSITORY \
  --provider-name agent37 \
  --runner-install-template agent37-gitea \
  --image garm-gitea-runner@1 \
  --flavor default --os-type linux --os-arch amd64 \
  --tags garm-agent37 \
  --min-idle-runners 0 --max-runners 2 \
  --runner-bootstrap-timeout 6 \
  --enabled=false
```

Enable the pool after following the [Gitea setup notes](docs/RESULTS.md#required-garm-compatibility-patch) and verifying the image revision, network reachability, and credentials. Workflows select it with `runs-on: garm-agent37`. The default Alpine image is a small shell-test environment; typical JavaScript actions need an appropriate Node-capable job image. Set `extra_context.agent37_default_image` in the pool's extra specs when using another image.

`default` allocates **4 vCPU, 8 GB RAM, 20 GB disk** with the example configuration. `small` allocates **2 vCPU, 4 GB RAM**, with disk capped at 12 GB. Extra spec `disk_gb` can choose a smaller supported disk. The provider always creates `type: vm`, mounts the durable disk at `/root`, and disables auto-sleep.

## Boundaries

The provider cap is separate from GARM pool limits, Agent37 account quotas, and the account wallet. It is not a spending limit. There is no warm-VM reuse, idle cooldown, automatic resume after interruption, or GitHub runner support in this first version. Stop is rejected; Start only verifies that the original worker supervisor remains alive. Completed, stopped, or interrupted workers must be deleted and replaced.

The local lab uses temporary authenticated reverse tunnels to reach Gitea and GARM on the laptop. Production workers require routable Gitea clone/API URLs and GARM metadata/callback URLs, including reachability from job containers. Configure the optional Tailscale integration above or supply another tested network path. Repository credentials, private lab state, and API keys must remain outside Git and image build contexts.

Licensed under [Apache-2.0](LICENSE). This is an independent provider project; upstream GARM acceptance is not implied.
