# Ninety jobs at a 45-worker cap

On 2026-11-01, the final synthetic burst completed **90/90 jobs on 90 distinct
runners**, held the provider/pool cap at **45 VMs**, preserved all 45 gated workers
across a GARM restart, and automatically returned to zero workers. The lab pool
was then disabled, its normal two-worker configuration restored, and GARM stopped.
The workspace had zero VMs and temporary SSH keys at final verification.

GARM created 100 VMs in total: 90 ran jobs and ten were surplus idle workers.
`allocation_exact` is false. This is the previously reproduced best-effort
allocation behavior; exact one-VM-per-job allocation is not a lifecycle guarantee.
The test still requires distinct runner identities, the cap, successful jobs,
restart recovery, and automatic deletion without emergency cleanup.

The run reached 45 simultaneously running gated jobs after 22.957 seconds.
All jobs finished 92.804 seconds after dispatch, and automatic scale-to-zero was
observed at 331.670 seconds. GARM halves surplus idle capacity on each one-minute
scale-down pass. These timings include an artificial gate and laptop SSH transport;
they are not production build latency measurements. Each VM used the tested
default resources and the small cached Alpine job image.

The first attempt exposed a separate GARM race: a job could finish while its VM
record was still `creating`. Completion marked the runner terminated, but the
transition to deletion was rejected. The eventual create result left ten
terminated VMs running. Emergency cleanup removed them; that attempt failed.
The [fix and deterministic regression](../../patches/garm-creation-completion-race.patch)
preserve the provider result/VM ID, then schedule deletion of a terminated runner.
The full GARM pool race suite and scoped lint passed.

An intermediate run with that fix completed all jobs and reaped completed VMs,
but its five-minute post-release deadline expired just before the final normal
idle scale-down pass. Its last idle VM needed harness cleanup, so that run also
remains failed. The final run uses a ten-minute deadline and passed without that
intervention. Earlier evidence is retained privately; results were not relabeled.

[Sanitized results](fortyfive-concurrency.json) record the final assertions.
A separate, concurrent [single-worker Tailscale probe](private-networking.md)
validated private networking. This burst used the existing SSH lab transport;
45 simultaneous Tailscale enrollments and production host1 activation are still
unvalidated. Wallet-based cost attribution is omitted because the two probes
overlapped. Account billing settings were not changed.
