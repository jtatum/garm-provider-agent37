# Private networking probe

On 2026-11-01, one default Agent37 VM reached LAN-only Gitea through a temporary,
restricted Tailscale gateway. The provider performed actual VM creation,
readiness, OAuth exchange, ephemeral enrollment, guest DNS setup, logout, and
deletion. Only the final Gitea runner bootstrap was replaced with a harmless
marker; production runner registration was not exercised.

Certificate-verified Gitea HTTPS and private DNS passed from the VM, Docker's
default bridge, a custom Docker network, and a Docker-container BuildKit builder.
The worker metadata route reached the gateway's unconfigured backend (expected
503), while its administrative API route returned 404. An independent offline
HAProxy test verified allowed worker paths and denied admin/traversal paths.

Kernel Tailscale enrollment and DNS took 1.55 seconds in this single probe.
This excludes VM startup and cached-image loading and is not a latency benchmark.
The VM used 4 vCPU, 8 GiB RAM, and 20 GiB disk. The network-enabled template
contained pinned Tailscale and the same cached Ubuntu job image as the trusted
workload experiment; it contained no identity or credentials.

Logout succeeded. The provider reported zero probe-owned VMs, the gateway's peer
list no longer contained the worker, and the temporary Kubernetes namespace and
gateway were removed. Other concurrently running lab workers were untouched.
Raw account details, private hostnames, keys, and logs stay outside the public repo.
[Sanitized results](private-networking.json) record the checks.

This establishes single-worker private connectivity. Production GARM callbacks,
repository authentication, registry publication, and a large simultaneous
Tailscale enrollment burst remain activation checks. The separate 45-worker
lifecycle experiment uses the existing lab SSH transport.
