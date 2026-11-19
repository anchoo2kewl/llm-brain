# Brain Hub service 0.1.1

Fixes laptop sessions disappearing from the local hub after a VPN or peer-hub outage.

- Local session control is the default, even when an old deployment still has `--agent-hub`.
- Surviving Brain Hub tmux sessions are recovered without restarting Claude or losing its live context.
- Recovery preserves conversation UUIDs and refuses conflicting machine/session identities.
- New terminals retain non-secret recovery metadata alongside their tmux process.
- Recovered local notifications retain credential validation and private retry spooling.
- MCP follows the hub explicitly selected in the desktop app; an environment override still takes precedence.

Validated on the MacBook: both existing Claude processes survived unchanged; recovered
session screen capture through MCP and terminal websocket attachment succeeded while
Studio was unreachable. A disposable local shell started, accepted screen capture,
and closed. Full service Go tests and MCP Python regression tests pass. Signed Darwin
arm64/amd64 and Linux arm64/amd64 hub binaries are included with SHA-256 manifests.

This is a hub-only update. The desktop client remains 0.1.7. Studio's old `ts.net`
address must be replaced with its new Headscale address to access Studio remotely.
Legacy in-process hooks can initially try their old URL; the local spool retries
delivery without restarting the conversation.
