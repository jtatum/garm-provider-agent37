# Agent37 Gitea installation template

Install `gitea-agent37.py.tmpl` as the pool's GARM runner-install template. It registers a fresh ephemeral runner, sends the installing and system-info callbacks, and sends idle before starting the one-job daemon.

GARM instance authentication accepts bootstrap callbacks only while the runner is pending or installing. The idle callback is therefore the last callback. The template drops its bootstrap-token reference before starting the runner and never sends a completion callback. The launch supervisor also releases its bootstrap configuration and rendered-script references after handing execution to the installer.

Gitea's workflow-job webhook tells GARM when the job starts and finishes. GARM owns VM retirement. Local `runner.finished` and `runner.failed` files record daemon completion and failures for diagnostics; they do not replace forge events. A failed or interrupted worker cannot be reused, even when the callback or webhook path is unavailable.

The registration token is written to a private file, supplied using `--token-file`, and removed before the daemon starts. The fetched install script removes itself before starting setup. Neither bootstrap credentials nor inherited Agent37 environment credentials are passed to the runner process.

Supported `extra_context` values:

- `agent37_default_image`: Docker image for bare labels; defaults to `alpine:3.22`.
- `agent37_container_network`: empty for the runner's default Docker networking; `host` is used only by the local lab's loopback tunnel.
