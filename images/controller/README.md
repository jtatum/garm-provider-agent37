# Controller container

This image packages the GARM server, CLI, built web UI, Agent37 provider, and the Gitea install template. It targets `linux/amd64`. The Go, Node and Debian base images are digest-pinned. GARM is pinned to upstream `33ec0921418a470ca7e00dfb20ae111595da24e1` plus two checksum-verified lifecycle patches retained in this repository: Gitea completion events that omit the runner name, and jobs that finish before the provider create call returns. The latter preserves the VM ID and schedules deletion after creation completes. The image is an independent deployment package, not an official upstream GARM release.

From the repository root:

```sh
docker buildx build --platform linux/amd64 --load \
  -f images/controller/Dockerfile \
  --build-arg PROVIDER_REVISION="$(git rev-parse HEAD)" \
  -t garm-agent37-controller:local .
python3 images/controller/smoke.py garm-agent37-controller:local
```

The Dockerfile-specific ignore file allows only provider source, the selected template, the lifecycle patches, license and recipe into the local build context. GARM is fetched independently by exact public commit. No private lab state, key, job image archive, host1 source or Docker credentials enter that context. Npm uses GARM's lockfile, Go uses the vendored GARM dependencies and the provider's module sums. Apt dependencies resolve at build time; the runtime package inventory is `/opt/garm/packages.txt`. Publish and deploy the resulting image by digest.

The build runs compilers on the build host architecture and uses an explicit amd64 cross-libc toolchain for GARM's SQLite support. The resulting controller runs on amd64; the local smoke test can use Docker's amd64 emulation on an ARM Mac.

## Runtime contract

- User/group: `10001:10001`; `HOME` and working directory `/var/lib/garm`.
- Entrypoint: `/usr/local/bin/garm --config /etc/garm/config.toml`.
- Server and CLI: `/usr/local/bin/garm`, `/usr/local/bin/garm-cli`.
- External provider: `/opt/garm/providers.d/garm-provider-agent37`.
- Install template: `/opt/garm/templates/gitea-agent37.py.tmpl`.
- HTTPS CA bundle and Python 3 are included; no Docker socket or container privileges are needed.
- Mount a persistent volume at `/var/lib/garm`, writable by UID/GID 10001. Store the SQLite database and provider journal there. Run one replica with a non-overlapping replacement strategy; the provider's journal lock is local filesystem coordination.
- Mount GARM's secret-bearing TOML at `/etc/garm/config.toml` read-only. The provider configuration and API-key file are separate mounts. The pinned GARM release does not implement `GARM_DATABASE_PASSPHRASE` or `GARM_JWT_AUTH_SECRET` environment overrides; use a Secret mount or a deployment-specific renderer. Preserve database encryption and JWT secrets across restarts.
- A read-only root filesystem works with a writable `/tmp` and the state volume. Drop Linux capabilities and disable privilege escalation. The controller listens on the configured API port; examples use 9997. This revision has no dedicated health endpoint; use TCP startup/readiness probes.
- Worker metadata/callback URLs and forge/webhook URLs are deployment configuration. The image does not establish production routing or configure a runner pool.

The smoke test creates an isolated temporary Docker volume and configuration with throwaway credentials, launches the image without any external network, initializes the API, verifies the web UI and provider protocol, and restarts the container to verify SQLite identity persistence. It runs as the image's nonroot user with a read-only root filesystem and removes its container and volume afterward. No cloud credentials or VMs are used.
