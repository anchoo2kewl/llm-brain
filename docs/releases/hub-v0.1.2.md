# Brain Hub service 0.1.2

- Agent reviews are advisory comments only. Approve and Request changes are disabled, regardless of severity, CI, configuration, or legacy Telegram commands.
- Incomplete assessments no longer produce public filler. Context follow-up remains private; clean reviews finish silently without repeated paid review loops.
- Agent code contribution and agent review have distinct provider labels. Existing human labels are preserved.
- Managed PR creation and review publication require attribution; the GitHub adapter rejects non-advisory review writes.
- Agent provenance on existing open PRs is reconciled at startup and every 15 minutes.
- Enforcement scripts and prompts travel with the signed service binary, so hub auto-updates also update runtime policy.

The macOS desktop client remains 0.1.7. Existing Claude processes are preserved;
new managed sessions inherit the CLI adapter. Workflow guards do not sandbox an
arbitrary executable or absolute-path GitHub client.
