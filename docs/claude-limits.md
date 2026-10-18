# Claude session resource limits

Install for the current user on each machine:

```sh
bin/install-claude-limits
```

This applies to interactive Claude Code as well as fleet and hub-launched Claude sessions. It requires Claude
Code 2.1.295 or newer and Python 3. The installer copies the guard to `~/.local/lib/llm-brain/`, merges hooks and
environment defaults into `~/.claude/settings.json`, and preserves existing hooks (including AgentShield),
permissions, plugins and the status line. The first pre-install settings are saved as
`~/.claude/settings.pre-claude-limits.json`. Re-running the installer does not duplicate hooks or reset usage.

Restart existing Claude processes to load the environment limits. Settings hooks can hot-reload, but that is
not a substitute for restarting to pick up native context and concurrency limits. Existing transcripts count
when a hook next runs, so resuming an already-over-budget session will stop it. Work and transcripts remain intact.

## Defaults

| Limit | Default | Enforcement |
| --- | --- | --- |
| Context window | 200,000 tokens | Claude's native `CLAUDE_CODE_DISABLE_1M_CONTEXT=1`; custom gateway window sizing is also set to 200K |
| Auto-compaction window | 100,000 tokens | Native `CLAUDE_CODE_AUTO_COMPACT_WINDOW`; applies to parent and subagents |
| Concurrent subagents | 2 per session | Native `CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS` |
| Subagent depth | 1 | Native depth limit plus Agent/Task denial inside a subagent |
| Agent calls | 6 per session tree | Hook; includes launches, resumes, and failed attempts; further calls denied, parent may continue directly |
| Agentic turns | 80 | Native default when the invocation does not explicitly supply a turn limit |
| Recorded model responses | 200 per session tree | Hook; counts unique assistant API message IDs |
| Output tokens | 100,000 per session tree | Hook; streaming blocks for the same message are counted once |
| Input tokens | 20,000,000 per session tree | Hook; includes uncached input, cache writes and repeated cache reads |
| Tool calls | 300 per session tree | Hook; reservations happen before tools run |
| Output per request | 16,384 tokens for most requests | Native `CLAUDE_CODE_MAX_OUTPUT_TOKENS`, reducing possible overshoot |

The parent and every transcript under its `subagents/` directory share one ledger under
`~/.local/state/llm-brain/claude-budget/`. Incremental reads retain only offsets, usage counters and tool IDs;
no prompts or tool contents are copied into that ledger. File locks serialize parallel reservations. Resumes,
compaction, transcript rewrites, and changing working directory do not refund the recorded budget. An 80%
warning asks the agent to finish a small step and save a handoff. At a response/token/tool budget limit,
`continue:false` stops the loop rather than returning a tool error that invites repeated retries. Unreadable
policies and corrupted state stop the loop. Unexpected hook process failures block the triggering action through
`onFailure: block` instead of silently disabling protection.

Headless Claude can report `hook_stopped` as exit 0. `llm-run` translates the budget guard's stop marker into
exit 75 with `BUDGET_STOPPED`, and fleet reviews do not automatically retry that same head after this marker.
The hub's stream capture also marks `terminal_reason: hook_stopped` as incomplete. Those adapter changes
require deployment of the updated fleet/hub; the installed user settings work independently.

## Scope and limits of the protection

This is a session safety budget, not a guaranteed account-wide weekly quota ceiling. Claude writes transcripts
asynchronously, so usage enforcement trails in-flight requests and can overshoot by their usage. Compaction,
title generation and other internal requests may not all appear as normal assistant usage. Already-running tools
are not killed, and a child already in flight may finish its current request before its next hook stops it.
The native window prevents normal growth toward 1M; it is not a provider-side network request-size firewall.

New independent Claude processes, separate sessions, forks with different transcript identities, other apps,
and other machines have separate budgets. User/project settings can override native defaults or explicitly
disable hooks: these are user controls, not tamper-resistant managed policy. Do not delegate around a limit by
launching another Claude process. A hard account-wide weekly cutoff would need provider/gateway enforcement
for every client, including clients that use their own login directly.

If the guard stops a task, inspect the saved work and use a concise handoff for an explicitly started fresh
session; do not automatically relaunch the same job. Edit the positive limits in
`~/.local/lib/llm-brain/claude-budget-policy.json` for future work if a different budget is needed. Existing
stopped ledgers stay stopped. The installer preserves a customized policy. There is no automatic budget reset.

Settings are backed up through the existing `claude-backup` sync; that sync does not automatically apply
per-machine settings or install guard executables on a new machine. Run the installer there too. No parallel
settings or memory sync is introduced.

## Verification

```sh
python3 tests/claude_budget_test.py
python3 tests/fleet_plan_test.py
python3 tests/slop_score_test.py
make -C hub check
```

The guard tests run without model calls and cover parallel reservations, pending tool IDs already present in
a transcript, streaming deduplication, parent/child aggregation, resume/compaction persistence, partial writes,
corrupt state, and idempotent installation. The installer rejects older CLI versions rather than installing
fail-closed hook fields they may ignore.

References: [Claude environment variables](https://code.claude.com/docs/en/env-vars),
[context windows](https://code.claude.com/docs/en/model-config#turn-off-1m-context),
and [hook decisions](https://code.claude.com/docs/en/hooks#json-output).
The old `CLAUDE_CODE_MAX_SUBAGENTS_PER_SESSION` is a no-op in current Claude Code; the persistent Agent/Task
hook is needed for a total launch budget.
