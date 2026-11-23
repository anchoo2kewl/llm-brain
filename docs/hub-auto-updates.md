# Hub service automatic updates

Hub updates are separate from desktop client updates. The hub daemon follows stable GitHub releases tagged hub-v*, never client-v*. Desktop client updates and their preference are unaffected.

Automatic updates are enabled by default. A short-lived scheduler wakes every 15 minutes, but normal release checks occur every six hours. A deferred update retries on the next wakeup. There is no additional resident updater process.

Updates select the machine's macOS/Linux architecture, validate the published SHA-256 checksums and the staged binary's version, and verify its macOS signature. The daemon is stopped only after downloads are verified and the hub reports no active managed work, commands or migration. Tmux terminal sessions keep running; their clients reconnect. An update that fails its running-version health check restores the previous executable. A version that previously failed is not retried automatically until a newer version is published (a manual request can retry it).

## Install scheduling

Install a hub binary with the update API and copy bin/brain-hub-update next to bin/hub, then run:

    python3 deploy/install-hub-auto-update.py --brain /path/to/llm-brain

macOS installs a separate user launchd job, com.llm-brain.hub-update. Linux installs a user systemd service and timer; the existing daemon service defaults to llm-brain-hub.service and can be selected with --hub-service. Custom installations can pass --state and a loopback --url. The updater preserves the daemon's existing launchd plist or systemd service arguments.

Settings/status are local files under the hub state directory: hub-update.json and hub-update-status.json. Updater output goes to logs/hub-update.log on macOS and the user journal on Linux. Installation retains bin/hub.previous for recovery.

## MCP

The updated Brain MCP bridge exposes hub_update. Use hubs_list to get the node name for hub; each request controls that selected hub only.

    {"action":"status","hub":"desk-1"}
    {"action":"check","hub":"desk-1"}
    {"action":"install","hub":"desk-1"}
    {"action":"settings","auto_enabled":false,"hub":"desk-1"}
    {"action":"settings","auto_enabled":true,"interval_hours":6,"hub":"desk-1"}

Install queues the independent updater job; read status to follow checking, available, deferred, installing, up_to_date or rolled_back. Disabling automatic updates does not disable explicit check/install. Reconnect existing MCP clients to refresh tool schemas. The API is GET/POST /api/hub-update; GET /api/hub-update/health exposes the running daemon version and readiness.

## Publish a hub release

Package the deployed migration-service source, preserving its existing session features:

    python3 deploy/package-hub-release.py 0.1.0 --source /path/to/migration-service --out /tmp/hub-release-0.1.0

The packager tests the source, builds Darwin/Linux arm64/amd64 binaries, signs Darwin binaries, and creates hub-update.json plus the updater/MCP support assets. It refuses an old public hub snapshot that lacks the installed migration API. Build on a Mac to sign the Darwin assets. Publish all output assets under hub-v0.1.0, marking it as not latest so the desktop client's latest-release endpoint remains client-v0.1.7. Create a draft, upload/verify all assets, and publish only after validation. Future hub releases use the same hub-v channel and manifest schema.

The migration-service update also persists MCP workspace reports and sequence numbers across restarts, so unchanged desktop panes remain discoverable and the first command after an update is retained.
