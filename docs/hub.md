# Hub

One web app for every agent you run: what needs you, what is running, every past conversation and run on every Mac, with search, terminals and insights.

The hub is Go with [Turso](https://github.com/tursodatabase/turso) (the Rust rewrite of SQLite) for storage and [bleve](https://github.com/blevesearch/bleve) for search. Pages are server-rendered with `html/template`; there is no front-end build step. The source is in `hub/`.

## Pages

The top bar has Inbox, Now, Workspace, History and Insights, plus Search and **+ New**. History groups Chats, Sessions, Runs and Files. The More menu holds the rest.

| Page | Path | What |
| --- | --- | --- |
| Inbox | `/`, `/inbox` | What needs you. See below. |
| Now | `/now` | Everything running, grouped by Mac. |
| Workspace | `/workspace` | Many terminals at once. [Terminals](hub-terminals.md#workspace). |
| New | `/new` | Start a terminal, a task or a model chat. |
| Chats | `/chats` | Hub-started tasks and model chats. [Tasks and chats](hub-tasks-and-chats.md). |
| Sessions | `/sessions`, `/sessions/{id}` | Every Claude and Codex transcript from every Mac, live. |
| Runs | `/runs`, `/runs/{id}` | Every fleet worker: prompt, live log, status, PR/issue link, its conversation. |
| Files | `/files`, `/browse` | Kept files and a folder browser per Mac. [Files and editor](hub-files-and-editor.md). |
| Editor | `/edit?host=&root=` | Monaco on a folder of a Mac. [Files and editor](hub-files-and-editor.md#editor). |
| Search | `/search` | Full-text search. [Search](hub-search.md). |
| Insights | `/insights` | Charts and findings. [Insights](hub-insights.md). |
| Fleet overview | `/fleet` | Issue pipeline, quota, hosts, agents, recent runs, scheduler log. [Insights](hub-insights.md#fleet-overview). |
| Slack | `/chat`, `/slack` | Team chat by source and thread. [Team chat](chat.md). |
| Hosts | `/hosts` | Every Mac: role (serve or ship), last seen, sessions, messages, runs, workers alive, time zone. |
| Agents and commands | `/agents` | Hub-run sessions, hosts with a live agent, the command log. |
| Prompt library | `/prompts` | Saved prompts with variables. |
| Shared memory | `/memory` | Recent memories and who is working on what. [Shared memory](memory.md). |
| Settings | `/settings` | Theme, home page, notifications; links to Updates, Slack, memory, hosts, prompts. |
| Updates | `/settings/updates` | Keep the CLIs current on every Mac. See below. |

### Inbox

One list of what needs you, from every source the hub has, in this order: waiting, needs review, blocked, failed.

| Item | Source |
| --- | --- |
| An agent waiting for your answer | Hub tasks, open terminals, and Claude/Codex conversations that ended on the agent's answer in the last 6 hours. Headless runs (`claude -p`, `codex exec`) never count. |
| A frozen terminal | A terminal blocked on a macOS prompt. [Frozen terminals](hub-terminals.md#frozen-terminals). |
| A failed task or fleet run | The last 36 hours. |
| A blocked issue | Fleet issues with a label containing `blocked`. |
| A PR that needs review | Fleet PRs waiting for a review. |
| A chat message | Slack DMs and mentions you haven't answered, and the chat digest's asks. |
| A failed tool update | From the Updates page. |

You can answer from the list. Quick replies (`yes`, `continue`, `1`, `2`, `no`) or a line of text go straight to the task or terminal. Opening or dismissing an item marks it seen; it comes back when something newer happens there.

Settings can make the fleet overview the home page instead of the inbox.

### Now

Everything running, on every Mac, grouped by host with the busiest first: hub terminals and tasks, fleet workers, Claude/Codex sessions started outside the hub, and tool updates.

### Sessions and runs

A session page is a readable, live transcript. It has a strip with tokens, an estimated cost, tool calls and token use over time. Runs of three or more tool calls in a row fold into one expander. Claude subagent transcripts are included.

Each session page also has a composer to continue it (when its Mac has a live agent), Stop, Review with the other LLM, Close, Rename, and the exact `claude --resume <id>` or `codex resume <id>` to continue by hand. See [Tasks and chats](hub-tasks-and-chats.md).

A run page shows the worker's prompt, its live log, status, links and its own conversation.

## Commands

```sh
make -C hub build     # -> bin/hub (darwin/arm64, no CGO)
make -C hub check     # go vet + go test
```

| Command | What it does |
| --- | --- |
| `hub serve [flags]` | Ingest this Mac's files and serve the UI. The always-on Mac. |
| `hub ship --to URL [flags]` | Send this Mac's files to a hub. A laptop. |
| `hub memory import [flags]` | Import Claude Code's memory files. [Shared memory](memory.md#importing-claudes-memories). |
| `hub version` | Print the build version. |

### Flags for both serve and ship

| Flag | Default | Meaning |
| --- | --- | --- |
| `--state` | `~/.local/state/llm-brain` | State folder (logs, runs, status.json). |
| `--claude` | `~/.claude/projects` | Claude Code transcripts. `''` skips them. |
| `--codex` | `~/.codex/sessions` | Codex sessions. `''` skips them. |
| `--host` | `hostname -s` | This Mac's name. |
| `--interval` | `10s` | Re-scan interval. |
| `--agent` | `true` | Run the session agent: terminals, tasks, files, close, updates on this Mac. `--agent=false` makes it read-only. |
| `--roots` | `work_root` if it exists, else `~/projects` | Comma-separated folders tasks may run in. |
| `--brain` | The checkout the binary lives in | llm-brain checkout, for `config/fleet.yaml` and `bin/review-rules`. |

### serve only

| Flag | Default | Meaning |
| --- | --- | --- |
| `--port` | `8787` | Listen port. |
| `--listen` | `127.0.0.1` + Tailscale IPv4 | Comma-separated addresses to bind instead. |
| `--db` | `<state>/hub.db` | Database file. |
| `--index` | `<state>/hub.bleve` | Search index folder. `off` searches the database instead. |
| `--no-scan` | `false` | Serve only; don't ingest this Mac's files. |
| `--org` | `""` | GitHub org of `pr-` and `review-` worker keys, for links. |
| `--issue-repo` | `""` | `owner/repo` of `issue-` worker keys, for links. |
| `--people` | `""` | `login=Name,…` display names. |
| `--gateway` | `http://127.0.0.1:8317` | Account gateway. `''` disables quota and plain chats. |
| `--gateway-env` | `~/.config/llm-brain/cpa.env` | File with `CPA_MGMT_KEY` and `CPA_API_KEY`. |
| `--slack-env` | `~/.config/llm-brain/slack.env` | `SLACK_TOKEN`, read only when the hub has no token yet. |
| `--chat-interval` | `2m` | How often to read Slack. `0` disables chat. |
| `--chat-backfill` | `24h` | How far back a newly watched conversation is read. |
| `--taskai` | `https://taskai.cc` | Where memories are copied. `''` keeps them on the hub. |
| `--taskai-env` | `~/.config/llm-brain/taskai.env` | File with `TASKAI_API_KEY`. |
| `--memory-project` | `Agent memory` | TaskAI project name or id for memories. |

### ship only

| Flag | Default | Meaning |
| --- | --- | --- |
| `--to` | required | Hub base URL, e.g. `http://hub-mac:8787`. |
| `--once` | `false` | Ship once and exit. |
| `--poll` | `3s` | How often the session agent asks the hub for commands. |

## Network and security

- The hub binds only to `127.0.0.1` and the Mac's Tailscale IPv4. If Tailscale is not up yet, it serves on localhost and retries every 30 s.
- Ship, agent, chat, memory, push and session-control endpoints accept only loopback and Tailscale addresses (`100.64.0.0/10`, `fd7a:115c:a1e0::/48`).
- Every POST that changes something is refused when it comes from another site.
- Pages send a strict Content-Security-Policy (`self` only), `nosniff` and `no-referrer`.
- There is no login. Anyone on the tailnet can read everything and start sessions on any Mac running the agent.

## Ingestion

Each Mac's collector reads:

- Claude Code transcripts: `~/.claude/projects/*/*.jsonl` and `*/*/subagents/*.jsonl` (not the `memory/` folders);
- Codex rollouts: every `.jsonl` under `~/.codex/sessions` (current and 2025 formats);
- fleet logs: `fleet.log`, `chat.log` and every worker log in `<state>/logs/`, and worker prompts from `runs/*.prompt` and `reviews/*/prompt.md`;
- which workers are alive, from `runs/*.pid`.

`hub serve` ingests its own Mac. `hub ship` sends chunks to `/api/ship/chunk` and a heartbeat with its running workers. Ingestion is incremental and idempotent: each file has a byte offset that advances in the same transaction as its rows, and a shipper asks the hub for its offsets first. The hub re-scans every 10 s.

Very large content is cut: 20 KB per tool input or result, 200 KB per text block, 256 KB per raw line (images stripped).

## App shell

- One design system: `static/tokens.css` (colour, type, spacing, radius, motion) and `static/ui.css` (components).
- Light and dark themes, or auto (Settings, Appearance; stored per browser).
- One status language: working, waiting for you, blocked, done, failed.
- On a phone: a bottom tab bar (Inbox, Now, New, Search, More), tables become cards, and nothing scrolls sideways at 375 px.
- Every page listens to one server-sent event stream, `/api/events`, for badges and live sections, instead of each polling on its own.

### Keyboard shortcuts

Not while typing, and not on pages that own the keyboard (terminals, Workspace, editor).

| Keys | Goes to |
| --- | --- |
| `/` | Search |
| `c` | New |
| `g i` | Inbox |
| `g n` | Now |
| `g w` | Workspace |
| `g h` | History (Chats) |
| `g s` | Sessions |
| `g r` | Runs |
| `g f` | Fleet overview |
| `g t` | Insights |
| `g c` | Settings |

The Workspace and the editor have their own; see [Terminals](hub-terminals.md#workspace) and [Editor](hub-files-and-editor.md#editor).

## Installable app and push

The hub has a web manifest and a service worker with an offline page and push. Browsers need HTTPS for both, so serve the hub through Tailscale once:

```sh
tailscale serve --bg 8787
```

Then open `https://<mac>.<tailnet>.ts.net`, add it to the home screen (on iOS, Share, Add to Home Screen; iOS only notifies the installed app), and turn notifications on in Settings. Settings can also send a test and lists each subscribed device with its last delivery.

You are notified once for each new inbox item. Payloads are encrypted per browser (RFC 8291, aes128gcm) and signed with the hub's own VAPID key (RFC 8292), which it keeps in its database, so the push service relays only ciphertext.

The app's shortcuts are Inbox, Now, New terminal and Workspace.

## Tool updates

`/settings/updates` keeps these current on every Mac with a live agent:

| Tool | Notes |
| --- | --- |
| Homebrew | `brew update`, then upgrade everything outdated |
| Claude Code | |
| Codex | |
| Gemini CLI | |
| GitHub CLI (`gh`) | |
| open-code-review (`ocr`) | |

Each host agent reports how each tool was installed (cask, formula, npm or Claude's own installer) and updates it that way. You can turn the daily update on or off, pick the hour, the tools and the hosts, or press **Update now** for one tool, one host or all. Each run keeps the versions before and after and the tail of its output. A failure lands in the inbox.

## Data

| Path | What |
| --- | --- |
| `<state>/hub.db` | Everything: transcripts, runs, chat, settings, memories, terminals, push keys. |
| `<state>/hub.bleve` | Search index. Rebuilt from the database when its version changes. |
| `<state>/assets/` | Kept files and chat uploads. |
| `<state>/logs/agent-<id>.<time>.log` | Task turn logs. |

`hub.db` grows with your transcripts. There is no retention yet.
