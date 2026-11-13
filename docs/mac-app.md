# Mac app

Brain Hub for macOS is a native app (SwiftUI and AppKit, no web view). It shows the hub's terminals as real terminals,
with panes like tmux. The source is in `app-mac/`. Linux uses the Tauri app in `app/` ([desktop-app.md](desktop-app.md)).

## Install

Get `BrainHub-mac-<version>.zip` from the GitHub release (tag `client-v<version>`). It is a universal app (Apple Silicon
and Intel), macOS 14 or later. Unzip it and move `Brain Hub.app` to `/Applications`.

The app has an ad-hoc signature only. If macOS blocks the first start, open System Settings, Privacy and Security, and
press Open Anyway, or run `xattr -dr com.apple.quarantine "/Applications/Brain Hub.app"`.

The app uses the same bundle identifier and the same hub list file as the Tauri app, so it replaces it on a Mac and keeps
your hubs: `~/Library/Application Support/app.llmbrain.hub/hubs.json`.

## Build

You need Swift 6 (the Command Line Tools are enough) or Xcode.

```sh
cd app-mac
swift test              # the non-UI logic
./build-app.sh          # dist/Brain Hub.app and dist/BrainHub-mac-<version>.zip
./build-app.sh --arch arm64 --arch x86_64   # universal (needs Xcode)
```

The version comes from `app/src-tauri/tauri.conf.json`, so one `client-v*` tag releases both apps. The
`client-release` workflow builds the universal zip and attaches it to the release.

If `swift build` fails to link `Package.swift` with "Undefined symbols ... PackageDescription.Package", the Command Line
Tools have stale `*.private.swiftinterface` files from an older install in `usr/lib/swift/pm/ManifestAPI` and
`usr/lib/swift/pm/PluginAPI`. Reinstall the Command Line Tools, or remove those four files (they say Swift 5.10 in
their first lines).

## The window

- **Sidebar**: each machine of the cluster and its open terminals. The dot says what the agent does: green working,
  orange waiting for you (it rang the bell, or the hub's inbox says so), grey ended. "Running elsewhere" lists Claude
  Code and Codex conversations that run outside the hub; double-click one to continue it in a hub terminal.
- **Panes**: a window starts with one pane. Split it up to 9 panes. Click a terminal in the sidebar to show it in the
  focused pane. Closing a pane only removes the view: the terminal keeps running on its machine. **End Terminal**
  stops it.
- **New Terminal** (⌘T): pick the machine, the CLI (Claude Code, Codex, Gemini or a shell), a folder and a title.
- **Search** (⌘K): conversations on every machine of the cluster. Return continues the selected one in a new terminal;
  **Move** moves the conversation, with its context, to another machine.
- The terminal reconnects by itself after a network drop or sleep, and says why when the hub or the machine is offline.
- Copy and paste: the hub's tmux has the mouse on, so a drag selects in tmux and copies to the Mac clipboard, and the
  wheel scrolls tmux's history. Shift-drag selects locally; then ⌘C. Option is Meta.

## Notifications

When a terminal rings the bell (its agent finished and waits for you) and you are not looking at it, the app shows a
notification, puts the count of waiting terminals on the Dock icon and in the menu-bar item. Click the notification
to show that terminal. The app asks for permission on the first start; you can change it in System Settings,
Notifications, Brain Hub.

## Keys

| Keys | Does |
| --- | --- |
| ⌘T | New terminal |
| ⌘K | Search conversations |
| ⌘1 … ⌘9 | Show terminal 1 … 9 (sidebar order) in the focused pane |
| ⌘D, ⇧⌘D | Split right, split down |
| ⌘], ⌘[ | Next pane, previous pane |
| ⇧⌘↩ | Zoom the pane |
| ⌘W, ⌥⌘W | Close the pane (asks first when its agent is still working); the window goes with its last pane. In other windows and sheets, ⌘W closes them |
| ⇧⌘W | End the terminal (stops it on its machine) |
| ⇧⌘R | Reconnect |
| ⌘0 | Show the terminal window, from any state |
| ⌘N | New window (Window, Merge All Windows makes tabs) |
| ⌘/, ⌘? | Help, Keyboard Shortcuts: every shortcut and the tmux bindings in effect |
| ⌘, | Settings |
| ⌃` | Bring Brain Hub forward from any app (change it in Settings, Keys) |

After the last window closes the app keeps running: click the Dock icon, use ⌘0, the menu-bar item's
**Show Terminals**, or the global key to get a window back.

### tmux keys

The app reads your tmux config (`~/.tmux.conf`, else `~/.config/tmux/tmux.conf`) and uses its prefix and bindings, on
top of tmux's defaults (prefix C-b, `%` and `"` split, arrows and `o` move, `x` closes, `z` zooms, `c` new terminal,
`n`/`p` next and previous terminal, `0`–`9` terminal by number, `[` copy mode, C-arrows and M-arrows resize). It
understands `set -g prefix`, `set -g base-index`, `unbind` (also `-a`, `-n`) and `bind` (also `-n` and `-r`) for
`split-window`, `select-pane`, `resize-pane` (also `-Z`), `kill-pane`, `new-window`, `next-window`,
`previous-window`, `select-window`, `copy-mode` and `send-prefix`. Other lines are ignored. Press the prefix twice to
send it to the terminal. Settings, Keys shows every binding and the line it came from, and can set another prefix.

## Code

| File | What |
| --- | --- |
| `Sources/BrainHubCore/` | The non-UI logic, with tests: hub URL allow-list, API types, websocket frames, hub list, panes, tmux config |
| `Sources/BrainHub/TerminalEngine.swift` | The terminal emulator behind one small protocol (bytes in; input, resize, bell and title out; focus). SwiftTerm today; the only file that imports it, so another engine (libghostty) can replace it |
| `Sources/BrainHub/TerminalSession.swift` | One terminal's websocket: reconnect, keep-alive pings, offline messages |
| `Sources/BrainHub/Panes.swift` | Panes of a window and the tmux key handling |

The hub API it uses: `GET /api/terminals`, `/api/live-sessions`, `/api/inbox`, `/api/cluster/nodes`, `/api/search`,
`/terminals/{id}/status`; `POST /terminals/new`, `/terminals/{id}/rename`, `/api/terminals/{id}/close`,
`/api/sessions/{id}/continue`, `/api/sessions/{id}/move`; and the websocket `/terminals/{id}/ws` (binary frames are
terminal bytes, a text frame `{"t":"resize","cols":N,"rows":N}` resizes, close code 4000 carries the reason). Like the
Tauri app, it talks only to loopback and tailnet hosts.
