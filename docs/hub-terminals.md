# Hub terminals and Workspace

Claude Code, Codex, Gemini CLI or a login shell, running interactively in tmux on any of your Macs and shown live in the browser.

## Opening a terminal

**+ New**, then Terminal (`/terminals/new`), or **New terminal** in the Workspace. The form asks for:

| Field | Meaning |
| --- | --- |
| Mac | Any host whose agent is live. |
| CLI | Claude Code, Codex, Gemini or a shell. The CLI must be installed on that Mac. |
| Folder | A recent folder, a suggestion, or a typed path (absolute or `~/…`, no `..`). Any folder in the home folder works; empty means `~`. |
| Model | Optional. |
| First message | Optional. Images and files dropped here are uploaded to that Mac first and added as `Attached: <path>` lines. |
| Worktree | Optional name and base branch (default `origin/main`). Makes `<repo>-wt-<name>` next to the repo and runs there. |
| Approvals | Off by default. With "ask" on, the CLI asks before acting. |

By default a terminal has every permission:

| CLI | Default | With approvals on |
| --- | --- | --- |
| Claude Code | `--dangerously-skip-permissions` | `--permission-mode manual` |
| Codex | `--dangerously-bypass-approvals-and-sandbox` | `--ask-for-approval on-request --sandbox workspace-write` |
| Gemini | `--approval-mode yolo` | `--approval-mode default` |

When the CLI exits, a login shell stays in the same folder, so the terminal is still a command line.

## How it runs

- tmux keeps the terminal running when the browser, the hub or the agent goes away. Install it with `brew install tmux`.
- The hub's tmux server has its own socket and config (`tmux -L llm-brain`) and never touches your own tmux.
- Each terminal is the tmux session `llmb-<id>`. From a shell on that Mac:

  ```sh
  tmux -L llm-brain attach -t llmb-<id>
  ```

- The browser opens a websocket to the hub; the hub queues an attach with a one-time token; the host agent runs `tmux attach` under a pty and dials back. The hub copies frames between the two.
- For Claude, the hub picks the session id up front, so it knows which transcript belongs to the terminal.
- Terminals use the CLI's own login unless `terminals.gateway: true` ([gateway](llm-roles-and-gateway.md#hub-terminals-and-the-gateway)).

### Folder-trust prompts

A CLI opened in a folder it hasn't seen asks whether to trust it. Nobody is at the Mac to answer. You picked the folder when you opened the terminal, so for the terminal's first minute the agent watches the screen and picks the option that trusts this folder (not its parent, and never "don't trust"). If the choice can't be told apart safely, it does nothing.

## On a phone

The terminal page has a touch key row (Esc, Tab, ⇧Tab, arrows, ⌃C, ⌃D, Enter), quick replies, a line input you can dictate into, and swipe on the header (or ‹ ›) to move between open terminals.

## Workspace

`/workspace` shows many terminals at once. Every open terminal stays attached in the background (up to 12), so switching is instant.

- Layouts: 1, 2, 3, 4, 6 or 9 panes. On a narrow screen, one.
- The sidebar lists every terminal with its state: working (output in the last few seconds), waiting (went quiet after answering you, or rang the bell), idle.
- "Running elsewhere" lists Claude and Codex sessions on any Mac that are not hub terminals (a laptop's own terminal, an IDE). They open read-only as their live transcript.
- Each pane can be Terminal or Chat on its own.
- The layout is kept in the browser.

### Workspace shortcuts

All are Ctrl+Shift, caught before the terminal sees them.

| Keys | Action |
| --- | --- |
| Ctrl+Shift+1…9 | Focus pane N |
| Ctrl+Shift+← / → | Previous or next terminal |
| Ctrl+Shift+↑ / ↓ | Previous or next pane |
| Ctrl+Shift+Enter | Zoom the focused pane |
| Ctrl+Shift+L | Next layout |
| Ctrl+Shift+N | New terminal |
| Ctrl+Shift+J | Jump to the first terminal waiting for you |

## Terminal and Chat views

Every conversation can be read two ways. Switch on the session page, a task's page, a terminal's page and in each Workspace pane header.

| View | Looks like |
| --- | --- |
| Terminal (default) | The CLI itself: `❯` prompts, `⏺` answers and tool calls on one line (`⏺ Bash(gh issue list)`), `⎿` results cut to three lines with `… +N lines` to expand. Markdown stays text with light colour. Codex shows `›`, `•`, `└` and `Ran <command>`; Gemini shows `>` and `✦`. For a hub terminal it is the live terminal. |
| Chat | Cards with rendered markdown. On a hub terminal it shows the transcript over the live terminal; what you type still goes to the terminal. |

The choice is kept per browser (`hub_view` cookie and localStorage). A Workspace pane can override it, and `?view=term` or `?view=chat` on a URL shows that view once.

## Frozen terminals

A CLI that touches a folder macOS guards (Desktop, Documents, Downloads, Pictures, Music, parts of Library) makes macOS ask on that Mac's own screen. The CLI waits until someone clicks: no keys reach it, nothing redraws, no CPU. It looks dead.

Each host agent watches for this. Once a minute it notes each terminal's screen and CPU time. A terminal where both stood still for 3 minutes is sampled once (macOS `sample`, 1 s). If a process in it sat in a file call (`open`, `stat`, `getattrlist`…) the whole time, the terminal is **blocked**. It shows as blocked on Now, the Workspace, its page and the inbox (and so in a push): "Terminal N is waiting on a macOS permission prompt on <Mac>".

The fixes it offers:

1. Click Allow on that Mac (Screen Sharing: `vnc://<Mac>`).
2. Give the hub agent and tmux Full Disk Access: System Settings, Privacy & Security, Full Disk Access. The page names both paths.
3. **Restart CLI…**: ends the stuck CLI and starts it again in the same tmux on a fresh pseudo-terminal, so keys typed into the frozen one don't run in the new shell. It continues the conversation (`claude --resume`, `codex resume`); Gemini starts afresh in the same folder.

Any change on screen or in CPU time clears it. Without Full Disk Access, don't have an agent search the whole home folder (`find ~`, `rg ~`); see [Troubleshooting](troubleshooting.md#a-terminal-froze).

## Closing

Every page that shows a session or terminal can close it: Now, Inbox, Sessions, a session, Chats, a terminal, the fleet overview, and the Workspace's ⏻ on each terminal and pane. A tap on **Close…** shows the real button; in the Workspace the first ⏻ tap arms it.

- A hub terminal is ended as a terminal (its tmux session is killed). If its Mac's agent is offline, the page tells you the `tmux -L llm-brain kill-session` command to run there.
- A hub task's running turn is stopped.
- Any other Claude, Codex or Gemini session, even one started in Terminal.app or an IDE on another Mac, is closed by that Mac's agent.

To find that process, the agent tries, most certain first:

1. Claude's own `~/.claude/sessions/<pid>.json`, checked against the process start time (guards against a reused pid).
2. The process that has the transcript open (`lsof`).
3. The one process of that CLI whose working folder is the session's folder. Two or more is ambiguous, and nothing happens.

It sends TERM, then HUP, then KILL. It never signals anything that isn't one of those CLIs, never a headless run (`claude -p`, `codex exec`, fleet workers), and never the hub or a process the hub runs inside.

## Renaming

A terminal, a task, a model chat or a transcript can be renamed from its page. Things that are the same conversation are renamed together (a terminal and its transcript; a task and its transcript). A Claude transcript gets the name in its own file, the way `/rename` writes it, so `claude --resume` shows it too. Nothing is typed into a running CLI.

## Review a terminal's work

**Review** on a terminal starts a read-only Claude or Codex session on the terminal's folder that reviews the work there. It shows up as its own task. See [Tasks and chats](hub-tasks-and-chats.md#cross-review).

## Scripting a terminal

| Endpoint | Does |
| --- | --- |
| `GET /api/terminals` | List terminals. |
| `POST /api/terminals/{id}/send` | Type text and/or press keys. JSON `{"text": "…", "keys": ["Enter"]}`, or form fields `text=` and `keys=` (comma-separated). Up to 64 KB of text and 50 keys. |
| `GET /api/terminals/{id}/screen` | The visible screen as JSON `{"screen": …}`. `?scrollback=N` adds N lines above; `?format=text` returns plain text. |
| `POST /api/terminals/{id}/restart` | Restart a frozen CLI. |
| `POST /api/terminals/{id}/close` | End the terminal. |

These are tailnet-only and, like every POST, refused from another site. See [API](hub-api.md).
