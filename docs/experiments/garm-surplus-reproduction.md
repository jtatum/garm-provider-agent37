# GARM surplus allocation reproduction

The extra idle VM in the [37-check workload run](trusted-workload.md) is reproducible using GARM's real scheduler and SQLite store, with a mocked provider and forge client. No Agent37 API calls, cloud VMs, sleeps or concurrent event-delivery races are needed.

This is an anticipated consequence of GARM's best-effort webhook scheduler, not evidence of an Agent37 duplicate-create defect. The controller does not reserve a forge job for a specific runner. The exact count of VM starts is therefore an efficiency measurement, not a guaranteed one-to-one mapping to completed jobs. The separate requirement that each worker executes at most one job remains unchanged.

## Deterministic sequence

1. Queue jobs A and B with the same labels. GARM creates workers A and B and locks both jobs.
2. Worker A becomes ready first and starts job B. GARM unlocks job A because its original worker is occupied.
3. Run the queue consumer before worker B starts a job. With spare pool capacity and zero job-age backoff, GARM creates worker C for job A, even though worker B is still pending.
4. Worker B starts job A. Worker C becomes ready but has no work.
5. Run scale-down. GARM marks only idle worker C for deletion; the two active workers remain running.

A second case sets the pool cap to two. The attempted replacement is refused at capacity, both original workers can process the swapped jobs, and no third worker is created.

Both tests use the GitHub fixture in GARM's existing pool suite. The scheduling behavior is shared with Gitea; this reproduction does not need the Gitea completion-name patch or the Agent37 provider.

## Upstream contract and scope

Verified against upstream commit `33ec0921418a470ca7e00dfb20ae111595da24e1`, which was still `origin/main` when fetched on 2026-11-01:

- [The in-progress handler unlocks the original job when its worker takes another job](https://github.com/cloudbase/garm/blob/33ec0921418a470ca7e00dfb20ae111595da24e1/runner/pool/pool.go#L528).
- [The queue consumer explicitly describes best-effort allocation and surplus runners](https://github.com/cloudbase/garm/blob/33ec0921418a470ca7e00dfb20ae111595da24e1/runner/pool/pool.go#L2197).
- [Scale-down selects surplus idle workers](https://github.com/cloudbase/garm/blob/33ec0921418a470ca7e00dfb20ae111595da24e1/runner/pool/pool.go#L1364).

The local tests prove scheduler allocation and the pending-delete transition, not physical cloud deletion. Actual provider deletion was independently observed in the preceding live workload run. They do not claim general concurrency correctness across every pool or event ordering.

No runtime GARM change or new upstream PR is proposed. Reducing this overhead would be a scheduling optimization that must account for compatible labels, existing worker commitments, failed bootstraps and overlapping pools. Simply refusing creation whenever a pending worker exists could strand unrelated queued work. Job-age backoff is an existing latency/overprovisioning tradeoff; it was zero in this reproduction and the live lab, and was not changed as a result of this investigation.

## Reproduce

The [test-only patch](garm-surplus-reproduction.patch) adds two cases to the existing pool test suite. In a clean local checkout of the pinned upstream revision:

```sh
git apply --check /path/to/garm-surplus-reproduction.patch
git apply /path/to/garm-surplus-reproduction.patch
go test -race -mod=vendor -tags testing \
  -run '^TestPoolStressTestSuite$/^TestSwappedJobs' ./runner/pool/ -count=1 -v
```

The focused tests completed in approximately 0.2 seconds of test-body time and passed with the race detector. They assert the unlocked job, still-pending worker, surplus allocation when capacity exists, cap enforcement, and idle-only scale-down. The full pool race suite passed in 57.349 seconds on Go 1.27.3. GARM’s configured lint checks passed using Go 1.26.2, because the pinned analyzer cannot read Go 1.27 export data. The local test-only commit is `f1d6653fb8fc3983f694bdd959a015ee3cd65744`; it is not an upstream PR.

The historical workload JSON and the harness's strict allocation assertion are preserved unchanged. Its `success: false` includes an exact-start-count check that GARM does not promise. Job success, distinct execution identities, configured capacity and automatic cleanup are separate observations; the extra start remains visible as an efficiency finding rather than being erased or relabeled as a clean exact-allocation pass.
