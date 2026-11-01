# Optional Tailscale worker networking

Tailscale networking is disabled by default. Enable it for a pool whose Gitea
and GARM worker endpoints are private, using a separate runner template built
with `GARM_TAILSCALE_ENABLED=1`:

```sh
python3 lab/image.py build --name garm-private-runner --tailscale
```

Add `--job-image <registry-image@sha256:digest>` to cache a job image in that
template. The image installs checksum-pinned Tailscale 1.104.4 and dnsmasq,
but contains no tailnet identity, enrollment key, or OAuth credential. Pair it
with this provider configuration and pin the resulting template revision:

```toml
[tailscale]
enabled = true
oauth_client_id = "your-dedicated-client-id"
oauth_secret_file = "/run/secrets/tailscale-oauth-secret"
tag = "tag:ci-worker"
gateway_dns = "ci-gateway.example.ts.net"
private_aliases = ["git.internal.example"]
```

Create a dedicated [Tailscale OAuth client](https://tailscale.com/docs/features/oauth-clients)
with the `auth_keys` scope and permission to mint only the configured worker
tag. Mount its secret on the controller. The provider exchanges that credential
on the controller and sends each VM only a newly minted, preauthorized,
single-use, ephemeral auth key with a ten-minute expiry. Neither OAuth
credentials nor enrollment keys enter the creation journal or command output.

Expose a gateway as a tagged tailnet endpoint. Its listeners should relay only
the required Gitea HTTPS and GARM metadata/callback traffic, preserving the
original Gitea hostname and certificate. Allow the worker tag to reach only
those gateway ports in tailnet policy; audit broader existing grants as well.
This provider does not configure the gateway or tailnet policy. It accepts no
subnet routes or tailnet DNS settings, advertises no routes, disables Tailscale
SSH, and blocks unsolicited incoming tailnet connections. Existing tailnet
policy remains the authority for which peers workers can reach.

After the VM and Docker are ready, the provider enrolls a kernel Tailscale
client and finds the gateway's IPv4 address by its exact MagicDNS peer name.
Private aliases and the gateway name resolve to that address through a local
dnsmasq instance; all other lookups use the VM's original non-loopback
resolvers. The image fixes Docker's default bridge at `172.17.0.1/16` and its
DNS server at `172.17.0.1`. dnsmasq listens on loopback and Docker bridges,
including subsequently created `br-*` interfaces. This supplies DNS to the
host, Docker jobs, and Docker-based BuildKit builders. Custom networking or
DNS overrides need their own validation.

Enrollment completes before GARM bootstrap downloads metadata. The provider
journals its enrollment attempt before minting a key; recovery verifies the
existing guest network rather than minting another key. Incomplete enrollment
causes VM deletion. Already-running workers are never reenrolled, and changing
the gateway, aliases, or worker tag cannot silently reuse their creation intent.

On deletion, the provider attempts a bounded `tailscale logout` and always
continues VM cleanup even if logout fails. An unreachable VM may remain visible
as an ephemeral device until Tailscale removes it; [offline cleanup is not
immediate](https://tailscale.com/docs/features/ephemeral-nodes). VM reuse and
untrusted fork workloads remain outside this provider's scope.

Before enabling a production pool, verify that the chosen gateway policy
allows its necessary traffic and denies unrelated tailnet/LAN access; exercise
Gitea checkout, registry authentication, and a BuildKit build using the original
private hostname. Test controller restart during enrollment, VM deletion, and
absence of residual running VMs. Unit tests use mocked credentials and never
join a tailnet.
