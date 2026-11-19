# Brain Hub desktop 0.1.7

- Fix the Mac terminal startup/close crash caused by a native worker callback inheriting main-thread isolation. Worker wakeups now reach the main queue before accessing the terminal engine. Native regression tests cover worker callbacks and repeated terminal startup/exit/cleanup.
- Make session Close buttons larger and red. Show Closing… and disable them while a stop is pending; duplicate stop requests are blocked across windows. The same controls apply to Running elsewhere, including its bulk-close action.
- Optionally choose an existing destination code folder when moving or cloning a session. Pin per-machine folder mappings for later moves, or explicitly use the home folder. Selected and pinned folders are validated before resume; failed moves preserve the source. Code files and uncommitted changes are not copied.
- MCP adds move_folders list/set/remove and destination_path, pin_path and without_folder on session_move. Migration status includes the resolved path. Reconnect existing MCP clients to refresh tool schemas.
- Preserve Control+Enter through the native/web terminal and the hub’s tmux bridge so Claude Code can send drafts and queued messages immediately. Include an optional Claude keybindings template selecting Control+Enter as the send-now hint.
- Include Ghostty native crash metadata in Diagnostics and MCP log access without exposing binary memory dumps.
- Add a signed macOS hub installer that unloads the launchd job before replacing its executable and checks readiness after reload.

Validation: Mac debug and optimized tests, terminal callback/start-stop stress checks, all migration-service Go packages, 16 MCP tests, and a real tmux/pty Control+Enter regression test. A live MacBook-to-Studio move retained a conversation token, read matching fixture contents from a different destination path, and closed the source only after the destination was verified. Invalid-folder testing kept the source alive.

Builds: native universal macOS app and Linux desktop packages. The migration-service update is required for destination-folder and pin options; the complete deployment patch and installer are included under deploy/.
