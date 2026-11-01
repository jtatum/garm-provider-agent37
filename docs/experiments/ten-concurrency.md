# Ten-concurrency burst

Passed on 2026-11-01 using the real GARM/Agent37 lifecycle and local Docker Gitea run **12**. Dispatched **20 jobs** with both provider and pool capped at **10 VMs**. All 20 succeeded on 20 distinct ephemeral runners and 20 fresh VMs.

- Reached ten simultaneous running jobs at **9.367 s** after dispatch; the other ten were queued.
- Restarted GARM while the first ten jobs were gated. All ten VM identities survived, with no replacement or extra allocation.
- Released the gate at **9.835 s**. GARM retired the first workers and supplied fresh VMs to the queued jobs.
- Last Gitea job completion: **31.976 s** after dispatch (Gitea timestamps have one-second resolution).
- Confirmed automatic scale-to-zero at **36.848 s**. The observed peak was exactly ten VMs and ten running jobs.
- Wallet changed from **$9.979734** to **$9.968874**: an observed difference of **$0.010860**, approximately **1.1 cents**. No other workspace instances existed during the test. This is the wallet observation for this short run, not a general per-job cost prediction; billing settings were not changed.

Each VM used the same `garm-gitea-runner@1` image, 4 vCPU, 8 GB RAM, and 20 GB disk. Work consisted of a private synthetic-repository clone, freshness canaries, and a deterministic release gate. This establishes lifecycle and queue behavior at ten-way concurrency; it does not measure ten simultaneous CPU-heavy host1 builds or sustained load.

Afterward: zero Agent37 VMs, zero temporary SSH keys, zero Gitea registrations and zero GARM instance records. The pool is disabled, both caps are restored to two, and the native controller is stopped. Docker Gitea remains available with its data retained.

[Raw sanitized observations](ten-concurrency.json) and [interactive timeline](ten-concurrency.html) are saved beside this report. API observations were approximately one second apart; peak assertions were made from those observations and the gated job state. The configured cap and Agent37's verified ten-instance workspace limit independently constrained provisioning.

## Reproduce

See [lab setup](../../lab/README.md). Set both limits consistently before running:

```sh
python3 lab/manage.py configure --key-file /absolute/path/to/private-agent37-key --concurrency 10
python3 lab/manage.py start
python3 lab/manage.py setup
python3 lab/manage.py pool --concurrency 10
python3 lab/integration.py --concurrency 10 --jobs 20
```

The integration harness checks that the pool is disabled, starts with no lab workers, verifies available account slots, and compares both configured caps with its argument. It requires more jobs than VMs, then asserts the waiting remainder, restart recovery, distinct identities and automatic deletion. It disables the pool and revokes its temporary key at the end. Restore the desired default caps afterward and stop the test controller.
