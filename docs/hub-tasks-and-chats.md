# Hub tasks and model chats

Start headless Claude, Codex or Gemini sessions on any Mac from the hub, continue and cross-review them, and chat with any model the gateway serves.

**+ New** (`/new`) offers three things:

| Choice | What | Good for |
| --- | --- | --- |
| Terminal | A live CLI in tmux. See [Terminals](hub-terminals.md). | Real work |
| Task | One job at a time for Claude, Codex or Gemini in a folder. Each answer is saved. | Working from a phone |
| Model chat | Any model the gateway serves. No tools, no folder. | Quick questions |

Everything you start is listed at `/chats`.

## Tasks

The form (`/chats/new`) picks:

| Field | Meaning |
| --- | --- |
| Mac | Any host whose agent is live (it polled in the last 30 s) and has that CLI. |
| CLI | Claude Code, Codex or Gemini CLI. |
| Folder | A repo or worktree under that Mac's `--roots`. Empty runs read-only in a scratch folder, `<state>/chats/<session>`. |
| Worktree | Optional name (lowercase letters, digits, `.`, `_`, `-`) and base branch: makes `<repo>-wt-<name>` and runs there. |
| Model | Optional. |
| Mode | Read-only, edits, or bypass. See below. |
| Prompt | The first message. It may use prompt-library variables. |

| Mode | Claude | Codex | Gemini |
| --- | --- | --- | --- |
| Read-only | `--permission-mode plan` | `--sandbox read-only` | `--approval-mode plan` |
| Edits (default with a folder) | `--permission-mode acceptEdits` | `--sandbox workspace-write` | `--approval-mode auto_edit` |
| Bypass | `--dangerously-skip-permissions` | `--dangerously-bypass-approvals-and-sandbox` | `--approval-mode yolo` |

Bypass needs its own "allow bypass for this session" tick box, every time.

### How a turn runs

Each turn is headless: `claude -p --output-format stream-json`, `codex exec --json`, or `gemini --output-format stream-json`. Claude and Codex go through the account gateway when it answers, the way `bin/llm-run` does; otherwise, and always for Gemini, the CLI's own login is used.

The session id is captured from the first turn, so every later message resumes the same conversation. While a turn runs, the page shows a typing indicator and the live output. The turn log is `logs/agent-<id>.<time>.log`, shipped and shown like a worker log. The transcript itself arrives through normal ingestion.

### On any session page

Every session page, hub-started or not, gets:

- a composer to continue it, when its Mac has a live agent;
- **Stop** for a running turn;
- **Review with <the other LLM>**;
- **Send review back**, on a review;
- Close and Rename ([Terminals](hub-terminals.md#closing));
- the exact `claude --resume <id>` or `codex resume <id>` to continue by hand.

### Cross-review

**Review with <the other LLM>** starts a read-only session of the other CLI (Claude or Codex) in the same folder. It reviews `git diff <base>...HEAD` and `git log <base>..HEAD` plus uncommitted work (`git status`, `git diff HEAD`), using the rules from `bin/review-rules`. It appears as its own chat, linked to the original.

**Send review back** queues a turn on the reviewed session with the review's findings.

A terminal has the same **Review** button.

## Model chats

A plain chat with any model the gateway lists at `/v1/models`, grouped as Claude, OpenAI, Gemini and other.

- No tools and no folder.
- Replies stream token by token. The reply is produced in the background, so closing the tab doesn't lose it.
- Markdown is rendered safely: everything is escaped first, and only code blocks, headings, lists, quotes, bold, italics, inline code and http(s) links become tags.
- The model can change mid-conversation.
- Drop, paste or pick files on the message box. Images go as images and text files as text.

It needs `CPA_API_KEY` in the hub's `--gateway-env` file. When the gateway is down, the page says so.

## Prompt library

`/prompts` keeps saved prompts. A prompt can use `{{VARS}}`. `CWD`, `REPO` (the folder's name), `HOST` and `BASE` (the worktree base) are filled in. Any other variable must be given as `NAME=value` lines, or the task is refused with the list of missing names.

## Agents and commands

`/agents` lists every hub-run session, the hosts with a live agent (their CLIs and version), and the command log.

### The command queue

The hub keeps a queue of commands per Mac: turns, stops, terminal open/attach/kill/restart, file transfers, closes, tool updates.

- `hub serve` runs the session agent for its own Mac against the database.
- `hub ship` runs it on the laptop and polls `POST /api/agent/poll` every 3 s (`--poll`), so nothing listens there.
- Each agent reports results to `POST /api/agent/commands/{id}` and its inventory (CLIs, tools, folders, terminals) to `POST /api/agent/inventory`.

The hub checks what a page asks for, and the Mac that runs it checks again: task folders against `--roots` after resolving symlinks, so a link inside a root that points outside is refused. Every control endpoint is tailnet-only and refuses cross-site POSTs, and every command is logged.

`--agent=false` on `serve` or `ship` turns all of this off for that Mac.
