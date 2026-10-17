# Hub API

Every HTTP endpoint the hub serves.

"Tailnet" means the request must come from loopback or a Tailscale address. "Same-site" means a POST (or the marked GET) is refused when it comes from another site. The hub binds only to loopback and the Tailscale address anyway, so pages are reachable only from the tailnet too.

Session-control routes (tasks, chats, terminals, files, close, rename, updates) exist only when the session agent is on.

## Pages

| Method and path | Page |
| --- | --- |
| `GET /`, `GET /inbox` | Inbox (or the fleet overview, if set as home) |
| `GET /now` | Now |
| `GET /fleet` | Fleet overview |
| `GET /workspace` | Workspace |
| `GET /new` | Start something |
| `GET /chats`, `GET /chats/new` | Tasks and model chats; new one |
| `GET /agents`, `GET /agents/{id}` | Hub-run sessions; one task |
| `GET /agents/{id}/conversation` | A task's conversation |
| `GET /llms/{id}` | A model chat |
| `GET /sessions`, `GET /sessions/{id}` | Transcripts; one transcript |
| `GET /sessions/{id}/more` | More messages of a transcript |
| `GET /sessions/{id}/agent` | A transcript's live-agent controls |
| `GET /messages/{id}/raw` | One message's raw JSON |
| `GET /runs`, `GET /runs/{id}`, `GET /runs/{id}/log` | Fleet runs, one run, its log |
| `GET /log/{name}` | A log by file name |
| `GET /terminals/new`, `GET /terminals/{id}` | New terminal; a terminal |
| `GET /terminals/{id}/status` | A terminal's state |
| `GET /edit` | Editor |
| `GET /files`, `GET /files/{id}`, `GET /browse` | Kept files, one file, folder browser |
| `GET /search` | Search |
| `GET /insights` | Insights |
| `GET /hosts` | Hosts |
| `GET /chat`, `GET /slack` | Team chat |
| `GET /memory` | Shared memory |
| `GET /prompts` | Prompt library |
| `GET /settings`, `GET /settings/slack`, `GET /settings/updates` | Settings |
| `GET /offline` | Offline page for the service worker |
| `GET /manifest.webmanifest`, `GET /sw.js` | Installable app |
| `GET /static/…`, `GET /favicon.ico` | Assets |
| `GET /healthz` | `ok` |
| `GET /status.json` | The fleet's last snapshot |

## Live updates and inbox

| Method and path | Guard | Does |
| --- | --- | --- |
| `GET /api/events` | Tailnet | Server-sent events: inbox counts and what's running, when they change. |
| `GET /api/inbox` | Tailnet | The inbox as JSON. |
| `POST /inbox/seen` | Tailnet | Mark items seen or dismissed. |

## Tasks and chats

| Method and path | Guard | Does |
| --- | --- | --- |
| `POST /chats/new` | Tailnet, same-site | Start a task or model chat. |
| `POST /agents/{id}/send` | Tailnet, same-site | Next message to a task. |
| `POST /agents/{id}/stop` | Tailnet, same-site | Stop the running turn. |
| `POST /agents/{id}/review` | Tailnet, same-site | Cross-review by the other LLM. |
| `POST /agents/{id}/sendback` | Tailnet, same-site | Send a review's findings back. |
| `POST /sessions/{id}/send` | Tailnet, same-site | Continue any transcript. |
| `POST /sessions/{id}/review` | Tailnet, same-site | Cross-review a transcript's folder. |
| `POST /llms/{id}/send` | Tailnet, same-site | Next message in a model chat. |
| `GET /llms/{id}/events` | Tailnet | Stream a model chat's reply. |
| `POST /prompts`, `POST /prompts/{id}/delete` | Tailnet, same-site | Save or delete a prompt. |
| `POST /sessions/{id}/rename`, `/agents/{id}/rename`, `/llms/{id}/rename`, `/terminals/{id}/rename` | Tailnet, same-site | Rename. |

## Terminals

| Method and path | Guard | Does |
| --- | --- | --- |
| `POST /terminals/new` | Tailnet, same-site | Open a terminal. `format=json` returns `{"id"}`. |
| `GET /terminals/{id}/ws` | Tailnet, same-site | The browser's terminal websocket. |
| `POST /terminals/{id}/kill` | Tailnet, same-site | End it. |
| `POST /terminals/{id}/restart` | Tailnet, same-site | Restart a frozen CLI (page). |
| `POST /terminals/{id}/review` | Tailnet, same-site | Read-only review of its folder. |
| `GET /api/terminals` | Tailnet | List terminals. |
| `GET /api/live-sessions` | Tailnet | Claude/Codex sessions running outside hub terminals. |
| `POST /api/terminals/{id}/send` | Tailnet, same-site | Type text or press keys. |
| `GET /api/terminals/{id}/screen` | Tailnet | The visible screen (`?scrollback=N`, `?format=text`). |
| `POST /api/terminals/{id}/restart` | Tailnet, same-site | Restart a frozen CLI; returns `{"command"}`. |
| `POST /api/terminals/{id}/close` | Tailnet, same-site | Close it. |

## Closing sessions

| Method and path | Guard | Does |
| --- | --- | --- |
| `POST /sessions/{id}/close` | Tailnet, same-site | Close from a page. |
| `POST /api/sessions/{id}/close` | Tailnet, same-site | Close any session (JSON). |
| `GET /api/sessions/{id}/close` | Tailnet | Where the close got to. |

## Files

| Method and path | Guard | Does |
| --- | --- | --- |
| `GET /hosts/{host}/ls` | Tailnet, same-site | List a folder. |
| `GET /hosts/{host}/download` | Tailnet, same-site | Download a file. |
| `POST /hosts/{host}/upload` | Tailnet, same-site | Upload to a Mac. |
| `POST /hosts/{host}/keep` | Tailnet, same-site | Keep a copy on the hub. |
| `POST /llms/upload` | Tailnet, same-site | Model-chat attachment. |
| `GET /fs/ws` | Tailnet, same-site | Editor file channel. |

## Team chat

| Method and path | Guard | Does |
| --- | --- | --- |
| `POST /settings/slack` | Tailnet | Save what to watch. |
| `POST /settings/slack/token` | Tailnet | Set the Slack token. |
| `GET /api/chat/messages?after=<id>&limit=<n>` | Tailnet | New messages, for `bin/chat-watch`. |
| `GET /api/chat/me` | Tailnet | The token's own user. |

## Shared memory

| Method and path | Guard | Does |
| --- | --- | --- |
| `POST /api/memory` | Tailnet, same-site | Save a memory. |
| `GET /api/memory?limit=` | Tailnet | Recent memories. |
| `GET /api/memory/context?repo=&issue=&pr=&folder=&files=&q=&k=&self=&format=md` | Tailnet | Context pack. `cwd=` works for `folder=`. |
| `GET /api/presence?repo=&issue=&cwd=` | Tailnet | Who is working on what. |

## Insights

| Method and path | Guard | Does |
| --- | --- | --- |
| `GET /api/insights` | Tailnet | Whole report (`?range=`). |
| `GET /api/insights/{section}` | Tailnet | One section. |
| `GET /api/insights/sessions/{id}` | Tailnet | One conversation's stats. |

## Push and settings

| Method and path | Guard | Does |
| --- | --- | --- |
| `GET /api/push/key` | Tailnet | The hub's VAPID public key. |
| `POST /api/push/subscribe`, `/api/push/unsubscribe`, `/api/push/test` | Tailnet, same-site | Manage this device's subscription. |
| `POST /settings/app` | Tailnet, same-site | Theme and home page. |
| `POST /settings/updates`, `POST /settings/updates/run` | Tailnet, same-site | Update schedule; run now. |

## Machine to machine

| Method and path | Guard | Used by |
| --- | --- | --- |
| `GET /api/ship/offsets?host=` | Tailnet | `hub ship`: where to resume each file. |
| `POST /api/ship/chunk` | Tailnet | `hub ship`: new bytes of a file. 409 on an offset mismatch. |
| `POST /api/ship/heartbeat` | Tailnet | `hub ship`: running workers, role, time zone. |
| `POST /api/agent/poll` | Tailnet, same-site | Host agents: claim commands. |
| `POST /api/agent/commands/{id}` | Tailnet, same-site | Host agents: report a result. |
| `POST /api/agent/inventory` | Tailnet, same-site | Host agents: CLIs, tools, folders, terminals. |
| `GET /api/agent/term/{token}` | Tailnet | Host agents: terminal websocket, one-time token. |
| `POST`/`GET /api/agent/file/{token}` | Tailnet | Host agents: file transfers, one-time token. |
