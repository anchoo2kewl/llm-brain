# Control Brain Hub from any MCP client

`bin/brain-mcp` is a small stdio MCP server. Any MCP client (Claude Code, Codex, Claude Desktop, Cursor, or another
stdio client) can use it to manage hubs, sessions and workspaces. It forwards each tool call to the hub's own HTTP
API over your tailnet, so the hub's access rules apply (tailnet only, same-site checks on changes). It stores no key
and offers no command or shell execution: every tool is a fixed, typed, size-limited operation.

The Mac app ships the bridge as `Brain Hub.app/Contents/Resources/brain-mcp`. **Settings → MCP** shows ready-to-paste
configurations for the selected hub. It needs `python3` (included with the Xcode Command Line Tools on macOS and
installed on most Linux systems).

## Install

Set `BRAIN_HUB_URL` to the hub's base URL (no path, no credentials), or write it to
`~/.config/llm-brain/connections-hub.url`.

Claude Code:

```sh
claude mcp add brain --scope user --env BRAIN_HUB_URL=https://hub.<tailnet>.ts.net -- '/Applications/Brain Hub.app/Contents/Resources/brain-mcp'
```

Codex (`~/.codex/config.toml`):

```toml
[mcp_servers.brain]
command = "/Applications/Brain Hub.app/Contents/Resources/brain-mcp"
env = { BRAIN_HUB_URL = "https://hub.<tailnet>.ts.net" }
```

Claude Desktop, Cursor and other clients (`mcpServers` JSON):

```json
{ "mcpServers": { "brain": { "type": "stdio", "command": "/Applications/Brain Hub.app/Contents/Resources/brain-mcp",
  "args": [], "env": { "BRAIN_HUB_URL": "https://hub.<tailnet>.ts.net" } } } }
```

On Linux, use the `bin/brain-mcp` path from a checkout of this repository.

A deployment can append its own rules to the server's instructions in `~/.config/llm-brain/mcp-instructions.txt`
(up to 4,000 characters, for example review or approval policy). Keep secrets out of it.

## Tools

| Tool | What it does |
| --- | --- |
| `hubs_list` | The configured hub, its peers (node names to pass as `hub`) and the machines sessions run on |
| `session_list`, `session_detail` | Sessions with machine, folder, provider and state. Without `hub`, `session_list` covers the configured hub and every online peer; each row names its hub |
| `session_screen`, `session_send` | Read the screen with a hash; send text or keys only against a fresh hash |
| `session_wait`, `session_wait_many` | Wait on the hub until sessions go quiet, show text or end |
| `session_create` | Start a Claude Code or Codex session on a machine |
| `session_rename`, `session_close` | Rename; end a session's CLI (its conversation stays on disk) |
| `session_move` | `mode: move` (default) or `clone` to another machine; returns a job |
| `move_status` | The job's phases (check, export, copy, resume, verify, close) and the new session id |
| `workspace_get` | What each open workspace window shows: layout, sessions in its panes, focus, zoom |
| `workspace_control` | Open or focus a session, set the layout (1, 2, stack, 3, 4, 6, 9), zoom or unzoom |
| `queue_list`, `queue_issue`, `queue_control`, `memory_search` | The hub's job queue and shared memory, where the hub has them |

Every tool takes an optional `hub`: a node name from `hubs_list`. Session ids belong to the hub that listed them;
pass the same `hub` when you act on one. The bridge resolves a name only to the configured hub or a peer that hub
lists as online, never to an address you type.

## Moves and clones

A move ends the source only after the destination is verified live: the hub checks the destination is connected
and has the same CLI (Claude Code or Codex), copies the conversation, resumes it, and waits for the destination to
confirm it stayed up. A clone forks the conversation on the destination and keeps the source. Uncommitted files,
worktrees and running processes do not move. See "End or move a terminal" in [desktop-app.md](desktop-app.md).

## Workspace control

Layout and focus live in each window. `workspace_control` leaves a request on the hub; the open web workspaces
apply it within a few seconds, and the Mac app applies open, focus, zoom and unzoom in its front window (it splits
panes as you choose, so it ignores layout requests). Each window reports what it shows, which `workspace_get` returns.
A request reaches only windows that are open; nothing replays when a window opens later.

Hubs without these features answer `HTTP 404` for move, workspace or queue tools.

The bridge in Brain Hub 0.1.5 lists only the configured hub's sessions. If that hub holds none (for example a laptop hub whose terminals live on another hub), pass `hub` from `hubs_list`, or use a newer `bin/brain-mcp`.

## Confluence

`hive/skills/confluence/confluence-mcp` is a second stdio server: fixed Confluence Cloud tools (search, read, create,
update, attach, comment and image repair). It sends every request to `CONFLUENCE_URL` only, follows no redirect,
limits response and attachment sizes, refuses a stale page version and attaches files only from the temp folders
or `CONFLUENCE_ATTACH_DIRS`. It needs only `python3`.

Import the skill into the hive, so every node has the file at the same path:

```sh
hub skills import --dir hive/skills --source llm-brain
```

Then add the server with its environment. The token belongs in a secret, never in a file in a repository:

```sh
claude mcp add confluence --scope user \
  --env CONFLUENCE_URL=https://<site>.atlassian.net/wiki --env CONFLUENCE_EMAIL=<you@example.com> \
  --env CONFLUENCE_TOKEN=<api token> -- ~/.local/state/llm-brain/hive/skill/confluence/confluence-mcp
```

The hub's MCP import turns `CONFLUENCE_TOKEN` into `${secret:confluence.CONFLUENCE_TOKEN}` and keeps the value on
that node, encrypted. Set the same secret on each other node (or use **Copy secrets to peers** on `/mcp`).
