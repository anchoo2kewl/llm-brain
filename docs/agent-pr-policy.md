# Agent PR ownership and advisory review

Agents never submit GitHub Approve or Request changes events, including through
the legacy Telegram approval command. Verified findings use COMMENT only.
Missing context triggers private source/contract follow-up. A completed review
without new actionable findings records completion privately and posts nothing.
No generic clean-review summaries, inability-to-verify disclaimers, or statements
that a comment does not request changes are published.

| Label | Meaning |
|---|---|
| `agent:assisted` | An automated agent contributed to this PR or its review. |
| `agent:author:claude` / `codex` / `gemini` / `openai` / `unknown` | Agent code contribution; separate from the human GitHub account. |
| `agent:reviewer:openai` / other provider | Agent review involvement; does not imply code authorship or approval. |
| `agent:pr-open` | An agent-assisted implementation PR remains open. |

The managed GitHub CLI adapter blocks approval/change-request review events through
`gh pr review`, REST review creation/submission, and GraphQL review mutations.
PR creation requires attribution labels; failure to create/apply them stops that
operation. Review publication and queue dispatch likewise require attribution.
Unrelated labels remain intact. Owner-authored open PRs are periodically reconciled
using explicit agent provenance in branches, PR bodies, or commit trailers; a
reviewer mention alone is never treated as evidence of agent code authorship.

The adapter is a workflow guard for managed agents, not an OS security boundary.
Humans' normal GitHub CLI remains available outside managed sessions. Existing CLI
processes keep their original environment; new sessions inherit the adapter. The
owner policy is also recorded in local Claude/Codex instructions for future work.

Hub 0.1.2 embeds these runtime scripts and worker prompts in its signed binary and
installs them at startup. This propagates enforcement through older hub updaters
that otherwise replace only the executable and MCP bridge. The attribution audit
runs at startup and every 15 minutes without a separate resident service.
