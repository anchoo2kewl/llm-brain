# Hub search

Full-text search over everything the hub holds, a few milliseconds per query.

Open `/search`, or press `/` on any page.

## What is indexed

| Type | What |
| --- | --- |
| `message` | Every transcript message: text, thinking, tool calls and tool output. |
| `session` | Claude and Codex conversations, by title and metadata. |
| `run` | Fleet worker runs and their logs. |
| `event` | Fleet log lines (`fleet.log`, `chat.log`). |
| `chat` | Slack messages. |
| `llm` | Model chats through the gateway. |

The index is [bleve](https://github.com/blevesearch/bleve), kept in `<state>/hub.bleve`. It syncs whenever ingestion or the chat poller writes, and every 30 s. When the index format changes, it is rebuilt from the database. With `hub serve --index off`, search reads the database instead (slower).

## Query syntax

All clauses must match.

| Syntax | Meaning |
| --- | --- |
| `deploy rollback` | Words |
| `"kubectl get pods"` | An exact phrase |
| `deploy*` | A prefix |
| `-flaky` | Must not match |
| `app#835`, `web-api/x.go`, a uuid, `snake_case` | Code tokens, matched whole or as a run of their parts |

### Filters

| Filter | Example |
| --- | --- |
| `host:` | `host:studio` |
| `type:` (also `is:`, `in:`) | `type:run`, `type:chat`, `type:llm` |
| `provider:` | `provider:codex` |
| `kind:` | `kind:tool_use` |
| `tool:` | `tool:bash` |
| `role:` | `role:user` |
| `status:` | `status:failed` |
| `session:` | `session:<id or uuid>` |
| `after:` (also `since:`) | `after:2026-10-01`, `after:7d`, `after:today` |
| `before:` (also `until:`) | `before:2026-10-14`, `before:yesterday` |
| `channel:` | `channel:general` (chat) |
| `from:` | `from:teammate-a` (chat) |
| `mentions:` | `mentions:me` (chat) |

Values of the same filter are ORed: `type:run type:event`. Values can be quoted. Dates take `YYYY-MM-DD`, `YYYY-MM-DDTHH:MM`, RFC 3339, `today`, `yesterday`, or a relative `7d` or `12h`. Type names have aliases: `messages`, `msg`, `conversation`, `runs`, `events`, `log`, `slack`, `plain`.

Anything else with a colon (`http://x`, `a:b`) is an ordinary word.

## How code tokens match

Text is split on whitespace, quotes, brackets and `,;|=<>`. Each alphanumeric part of a word is a token, the whole word is a token too, and each pair of adjacent parts is a token. So `see app2#835 in web-app-api/internal/x.go` matches `app2#835`, `835`, `web-app`, `internal/x.go` and `x.go`. Everything is lowercased; nothing is stemmed.
