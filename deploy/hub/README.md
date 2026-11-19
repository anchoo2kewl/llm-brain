# Migration service folder options

`optional-move-folders.patch` contains the complete tested hub/agent implementation and regression tests for optional destination paths and durable folder mappings. It targets the migration-control service at commit `8177346` (which includes terminal move/clone jobs and transcript import). The public desktop source snapshot predates that service API, so this deployment patch keeps its implementation reviewable without replacing unrelated control-plane source.

Apply to that migration-service checkout with `git am`, run `go test ./...` from `hub`, and build `go build -o ../bin/hub .`. Update the source hub and destination agents together. Existing agents that do not acknowledge strict folder selection cause the job to fail before resume; the source stays running.

The database change only adds `move_folders`. Existing moves remain compatible, including the automatic folder fallback when no mapping or explicit path is selected.

## Local session recovery (hub 0.1.1)

After the folder-options and hub-auto-update patches, apply `local-first-sessions.patch`
with `git apply`. Its exact base is `ed9c4fc9cc7c5bc07687dec2e4f7d7a323b95c83`.
It includes the service implementation, MCP selection change, and regression tests.
Run `go test ./...` in `hub` and the Python MCP tests before packaging.

Each `hub serve` now controls its local session queue by default, including deployments
with an old `--agent-hub` argument. Remote-only delegation requires explicit
`--remote-agent --agent-hub URL` and continues to require that remote hub online.
Surviving `llmb-*` tmux terminals are registered locally without restarting their
processes; conversation UUIDs are recovered from non-secret tmux metadata or legacy
launch arguments. Conflicting session identities are left untouched and logged.
No recovery path creates a second Claude process or queues an open command.

MCP follows the desktop's saved hub selection unless `BRAIN_HUB_URL` is explicitly
set. It never redirects a failed session operation to another hub's id namespace.
Existing Claude hooks may retain an old peer URL until the CLI is restarted; their
private pending notification files are retried by the local executor, using the
existing local credential. Local attachment and session control do not need the peer.

This patch does not discover a replacement address for a disconnected peer. Configure
Studio's new Headscale address separately to restore access to Studio-hosted sessions.
