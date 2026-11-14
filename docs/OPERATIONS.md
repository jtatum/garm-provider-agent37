# Operations

The provider is a fresh-worker adapter. GARM owns queue demand, registration metadata, completion handling, and deletion decisions. The guest's supervisor runs one ephemeral runner; it never re-registers a completed or interrupted worker.

## Configuration and limits

[`examples/provider.toml`](../examples/provider.toml) lists every provider setting. Unknown TOML keys and unknown provider extra specs are rejected.

| Setting | Meaning |
| --- | --- |
| `provider_id` | Stable namespace, 1–64 letters, digits, dots, underscores or hyphens; starts with a letter or digit. Keep it stable across restarts. |
| `api_key_file` | Absolute path to a private file containing the Agent37 workspace API key. The key is not accepted as a command-line argument or inline TOML value. |
| `state_dir` | Absolute, persistent directory with mode `0700`. The service identity needs read/write access. |
| `region` | `us`, `eu`, or `ap-south`; default `us`. |
| `max_instances` | Capacity reserved or used by this controller/provider namespace, across its pools; default 2. Valid range 1–200 is configuration validation, not a claim that the account has that quota. |
| `default_disk_gb` | Default 20, valid 2–20. The `small` flavor caps its default at 12 GB. |
| `create_timeout` | Overall create/readiness/bootstrap-launch deadline, default `4m`; valid 1 second to 15 minutes. |
| `poll_interval` | Readiness poll delay, default `1s`; valid 100 milliseconds to 30 seconds. |

The account may contain unrelated instances consuming its total quota. The provider never deletes those to make room. An explicit capacity/funding refusal becomes a GARM capacity error. The configured cap does not enforce a wallet limit or disable Agent37 automatic top-up. Configure account funding separately.

Pool extra specs accept `disk_gb`, `extra_context`, and GARM's injected `runner_install_template`. The two custom template context values are:

```json
{
  "extra_context": {
    "agent37_default_image": "alpine:3.22",
    "agent37_container_network": ""
  }
}
```

An empty network selects the runner's normal container networking. The laptop lab sets `host` to reach its loopback reverse forwards; do not copy that choice without considering the actual network layout. Explicit runner label mappings are preserved; plain labels are mapped to the configured Docker job image.

CA bundles and proxy settings supplied by GARM are supported. SSH key injection, JIT, GitHub runner groups, GARM agent mode, boot-debug tracing, and boot-time package installation are unsupported. Install dependencies in a new image revision instead. The runner template currently sets a one-hour job timeout and disables the runner cache service; change the template deliberately if different policy is needed.

## Identity and state

Agent37 resources carry `garm-controller-id`, `garm-provider-id`, `garm-pool-id`, and `garm-runner-name`. Cloud names are deterministic hashes; GARM sees the original runner name. Reads and mutations verify ownership, and pool-scoped operations refuse another pool's workers. `RemoveAllInstances` covers all pools in the same controller/provider namespace and leaves other namespaces untouched.

The provider creates a private namespace directory beneath `state_dir` with an atomic `creations.json` journal and lock files. The journal contains names, pool/instance IDs, request fingerprints, stages and timestamps, not API keys or instance tokens. Locks and reservations coordinate separate executable invocations. Retired (`deleted` or `failed`) records are tombstones that block reuse of a runner name; they are compacted out 24 hours after their last update, well beyond GARM's retries of a failed create, so the journal stays proportional to recent activity rather than total history. Active and ambiguous (`pending`/`unknown`) records are never compacted. Unheld per-runner lock files are removed after deletions. Preserve the directory across controller restarts and upgrades. Independent state directories or machines do not share admission control; this version does not implement a distributed lock service.

A per-runner lock prevents concurrent calls from bootstrapping the same worker twice. The shared journal lock covers capacity admission and journal updates. Deletion performs its cloud calls outside it and then rechecks, under the lock, that no relevant creation advanced in the meantime; if one did, it returns an error so GARM retries against current inventory. Journal bookkeeping uses its own short deadline instead of the remainder of `create_timeout`, so a slow boot does not turn brief lock contention into a discarded worker. If a retry uses a refreshed GARM instance token after bootstrap has launched, the provider verifies the original live supervisor and adopts it instead of replaying the credential-bearing command. A worker with an earlier attempt but no live supervisor is retired, not reused.

## Ambiguous creation and recovery

Create is never blindly retried. Before a request, the provider saves its reservation. If the response is lost, it searches owned inventory for that original runner name. A single matching VM can be reconciled; multiple matches fail closed. An explicit rejection such as HTTP 503 `no_capacity` releases the reservation. A transport failure or otherwise unknown result remains quarantined and consumes capacity until reconciled.

Known allocated IDs missing from list results are checked individually before their capacity is considered free. Deletion by the original GARM runner name also checks its recorded ID, so GARM can clean up a worker even when the create response was lost. Deleting an already absent, known-terminal worker is idempotent. Deleting an unresolved reservation without a known VM intentionally returns an error rather than claiming the cloud operation never happened.

If an unresolved intent blocks admission:

1. Disable the affected pool and stop new provider invocations before editing state. Back up the private state directory.
2. Match the journal's original runner name and pool against the exact controller/provider ownership metadata in Agent37. Do not identify a resource only by a similar display name.
3. If a matching VM exists, delete it through the provider using its GARM name or exact ID, then verify its absence and the corresponding runner's removal from Gitea/GARM.
4. If no VM is visible, do not clear the reservation based on one empty listing or a request timeout. Establish that the original server-side create has settled and no owned resource exists; use provider-side investigation if the outcome remains uncertain.
5. Only after that verification, an operator may mark the specific journal record terminal (`deleted`) while the controller is stopped. Keep its name/fingerprint as a tombstone (normal compaction removes it after 24 hours); do not erase the whole journal or retry the same logical runner name. Re-enable the pool and let GARM allocate a new runner identity.

There is no automatic command to dismiss unknown outcomes. If the journal is corrupted, preserve it and investigate; the provider refuses new allocation rather than guessing capacity or ownership.

## Readiness, failures, and cleanup

Agent37's `running` state alone does not establish a usable runner. The provider waits for `/root/garm-image/ready.json`, the Gitea runner executable, and `docker info`. The image writes the marker after loading the verified job-image archive and successfully running its offline smoke container.

Once ready, a private supervisor fetches the selected GARM install template through the authenticated metadata endpoint. The Gitea template obtains the worker's registration token, writes it to a mode-`0600` file for `--token-file`, removes it after registration, and uses capacity one plus `daemon --once`. The Agent37 API key and local Gitea administrator credentials are never sent to the VM or job. The worker necessarily receives its own GARM and runner credentials; this implementation's initial trust scope is trusted repositories.

Synchronous readiness/bootstrap-launch failure and cancellation attempt owned-VM cleanup. Installation failures before the idle callback can use GARM bootstrap callbacks. After idle, those credentials are deliberately unusable: Gitea workflow-job webhooks drive completion and GARM reconciliation handles offline workers. Local terminal markers retain runner diagnostics; they do not grant a second registration or replace completion webhooks. The local test exposed an upstream completion-event issue with deleted ephemeral Gitea runners; use the compatibility fix and final GARM revision recorded in [RESULTS.md](RESULTS.md). Do not claim full lifecycle compatibility with the unpatched baseline.

Inspect GARM runner state/events first, then the owned instance and its private `/root/garm/launch.json`, `bootstrap.log`, `runner.log`, and terminal markers if the VM still exists. Logs may contain workflow output; do not publish them without review. Provider protocol JSON is written to stdout, while errors go to stderr without raw API bodies, exec output, or credentials.

Stop/sleep is not a supported way to preserve or reuse a worker. `Stop` returns an unsupported-operation error. `Start` only verifies the original running supervisor on the same boot; a stopped, crashed, completed or rebooted worker must be deleted and replaced. Keep auto-sleep off for these pools.

To retire an installation, disable its pools, drain running work through GARM, and delete the owned runners. Verify both cloud inventory and Gitea runner registrations before removing provider configuration or state. Revoking an API key is not a substitute for deleting billable VMs.

## Image preparation

The source lives in [`images/runner`](../images/runner). Fetch the Alpine image for `linux/amd64` from:

```text
alpine@sha256:2e0b81d60597f449baa56dc25d355367869d2dca4cd57e866f1e2b52af3eb55f
```

Tag it locally as `garm-template-cache/alpine:3.22`, then export with `docker image save --platform linux/amd64 -o images/runner/alpine.tar garm-template-cache/alpine:3.22`. The committed Dockerfile expects archive SHA256 `3c1b545ef346526439a40ab87bf252f21c0e5591e3d181d63bd0a6436a745bb3`; the entrypoint independently checks architecture and root-filesystem layer digest. Archive serialization can differ between Docker versions. If the archive checksum changes, inspect the exported image and review the checksum update rather than removing the check.

The [lab image helper](../lab/README.md#custom-image) uploads an explicit five-file allowlist through Agent37's authenticated custom-template builder. You can also use the pinned Agent37 CLI 0.4.1 `templates build images/runner --name garm-gitea-runner` with a separately reviewed build context. Keep `.env`, private lab state, keys and runner state outside that directory. The resulting workspace template revision is immutable; use its explicit `name@revision` in GARM. Rebuilding creates another revision and does not update existing workers automatically.

Binaries and archives belong outside `/root`, which Agent37 mounts as the persistent disk. The image uses `/opt` and `/usr/local/bin`; Docker and its managed containerd store their runtime data under `/root/docker`. Never build a reusable template from a worker that has already registered or executed jobs.

The recipe freezes the Ubuntu base digest, Docker-related package versions, and runner checksum. Other apt package versions resolve at build time and are recorded inside `/opt/garm/packages.txt`; the published image revision freezes the resulting filesystem. Prewarming job images and BuildKit build caches are separate tasks. The supplied Alpine cache is a small acceptance-test fixture. Choose job images and measure startup costs for the workflows your pool will run.

## Network and rollout

For private endpoints, see the optional [Tailscale networking configuration](../tailscale/README.md) and [live connectivity results](experiments/private-networking.md).

Workers must reach GARM metadata/callback URLs and Gitea API/clone URLs. Job containers must reach the checkout URL and any services required by their workflows. Configure HTTPS and the intended CA/proxy settings for production. The temporary reverse tunnels used by the laptop lab are test infrastructure, not provider functionality.

Upgrade the provider binary only while no provider invocations are running. Stop the controller (or drain it so no create, delete or list is in flight) before replacing the binary, and never let old and new binaries operate on the same `state_dir` at the same time. Releases may change how invocations coordinate through the state directory. For example, unheld per-runner lock files are now removed after deletions, which is safe only because every invocation re-checks that the file it locked is still the one at its path; an older binary waiting on a lock does not perform that check and could lose mutual exclusion with a newer one. Run a single controller replica with a non-overlapping replacement strategy so restarts do not overlap either.

Private `lab/.state`, generated binaries, image archives, credentials and VM logs are excluded from Git. The checked-in lab code is specific to the isolated local Gitea fixture; see [results](RESULTS.md) before adapting it. Validate endpoint reachability, registration/callbacks, artifacts and any registry publication before routing production jobs. Size capacity and caches from representative workloads. Multi-controller coordination and budget enforcement are not provided.
