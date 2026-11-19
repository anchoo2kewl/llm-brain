# Brain Hub desktop 0.1.6

- Open **Help → Diagnostics** to read the local application event log and available macOS crash reports. Open the log or crash-report folder from the same window. Event logs rotate after 1 MiB and record lifecycle events without terminal contents.
- MCP exposes **diagnostics_list** and **diagnostics_read** for this computer's Brain Hub logs and crash reports. Reads are limited to 1 MiB and redact common credential fields and the local home-directory prefix.
- **Running elsewhere** has an End Session button and context-menu action, plus **End All Running Elsewhere** for each machine. These stop the actual CLI process through the session's hub. A missing machine agent produces an error.
- Closing an attached terminal clears its pane references before releasing its connection. Native terminal surfaces keep callback data alive through deferred cleanup and remove window observers during disposal.
- Launching another macOS or Linux copy activates the existing Brain Hub instance and exits the new process.
- Machine destination tiles appear when dragging over the sidebar. Move and Clone remain available from the terminal's context menu.

The native cleanup changes address lifecycle hazards found during investigation. No Brain Hub application crash report was available on the test MacBook, so the reported delayed crash could not be matched to a specific stack trace. Existing hub crash reports remain available in Diagnostics.
