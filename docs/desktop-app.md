# Desktop app

Brain Hub is a small native app for the hub. On macOS, use the native Mac app: see [mac-app.md](mac-app.md). This page is about the Tauri app for Linux, which also still builds on macOS.

The app source is in `app/`. It uses Tauri v2: a Rust core and the system web view (WebKit). It has no web framework and no runtime npm packages.

## What it does

- The main window shows the hub's own web pages. The app does not copy them.
- A hub list lets you save many hubs and switch between them.
- A global hotkey opens a quick-search window.
- The app shows a system notification for each new inbox item.
- A tray (menu-bar) icon gives you Open, Quick search, the hub list and Quit.
- The window remembers its size and position.

## Download

Get the build for your system from the GitHub release page (tags `client-v<version>`):

| System | File |
| --- | --- |
| macOS (native app, universal) | `BrainHub-mac-<version>.zip`, see [mac-app.md](mac-app.md) |
| Linux | `.AppImage`, `.deb` or `.rpm` |

The builds are not signed:

- macOS: the first start can be blocked. Open System Settings, Privacy and Security, and press Open Anyway. Or run `xattr -dr com.apple.quarantine "/Applications/Brain Hub.app"`.
- Linux: make the AppImage executable (`chmod +x`), or install the `.deb`/`.rpm`. The app needs WebKitGTK 4.1 and an AppIndicator library for the tray icon.

## Publish a release

1. Set the new version in `app/src-tauri/tauri.conf.json` and `app/package.json`.
2. Push a tag `client-v<version>` for that commit.
3. The `client-release` workflow runs the tests, builds the native Mac app and the Linux app, and attaches the files to a draft release. It publishes the release only when every build succeeded.

## Build

You need Rust (cargo) and Node.js. On macOS, the Command Line Tools are enough; you do not need full Xcode. On Linux, install `libwebkit2gtk-4.1-dev`, `libayatana-appindicator3-dev` and `librsvg2-dev`.

```sh
cd app
npm install
npm run build
```

`npm run build` builds for this computer. `npm run build:mac` builds for Apple Silicon. The npm package `@tauri-apps/cli` is a build tool only. You can use `cargo tauri build` instead if you have installed `cargo-tauri`.

The bundle is here:

```
app/src-tauri/target/aarch64-apple-darwin/release/bundle/macos/Brain Hub.app
```

The build signs the app with an ad-hoc signature. It does not notarise the app.

Other commands:

| Command | Does |
| --- | --- |
| `npm test` | Runs the Rust tests and the Node tests. |
| `cd src-tauri && cargo clippy --all-targets -- -D warnings` | Lints the Rust code. |
| `npm run dev` | Runs the app from source. |

## Install a local build

On macOS, copy the app to `/Applications`:

```sh
cp -R "app/src-tauri/target/aarch64-apple-darwin/release/bundle/macos/Brain Hub.app" /Applications/
```

The app has only an ad-hoc signature, so macOS can block the first start, mostly if you copy the app from another Mac. Then open System Settings, Privacy and Security, and press Open Anyway. You can also run `xattr -dr com.apple.quarantine "/Applications/Brain Hub.app"`.

On the first start, the window shows the hub list. Add a hub, for example `http://127.0.0.1:8787`. Allow notifications for Brain Hub when the system asks.

## Hub list

Open the list with Cmd+Shift+H (Ctrl+Shift+H on Linux), or choose **Manage hubs** in the tray icon.

- Add a hub with a name and a URL.
- Rename or remove a hub with the buttons in its row.
- Press **Use** to show a hub in the main window.
- A dot next to each hub shows if it answers. The app calls `GET /healthz` every 10 seconds while the list is open. A green dot means the hub answered `ok`.
- If you select a hub that does not answer, the window does not change. The list shows the error.

The list is saved as JSON in the app config folder: `~/Library/Application Support/app.llmbrain.hub/hubs.json` on macOS, `~/.config/app.llmbrain.hub/hubs.json` on Linux. You can edit this file by hand. The app drops any hub that fails the host check at start.

The tray icon also lists the hubs. Choose one to switch.

## Terminal windows

Choose **New Terminal Window** (Cmd+N on macOS, Ctrl+N on Linux) in the app menu, the Window menu or the tray icon. A new window opens the selected hub's Workspace: all its terminals, with layouts of 1 to 9 panes and zoom (Ctrl+Shift+Enter).

- Open as many windows as you need, for example one per screen. Each window keeps its own layout, so you can put different terminals in each.
- Closing a terminal window closes only the window. The terminals keep running on their machines.
- Herdr programs open in the Workspace too. Detach from one with Ctrl+\; the program keeps running.
- Reload, Back and Forward act on the window in front.

## End or move a terminal

- **End:** hover over a terminal in the sidebar and click **×**. It ends the terminal's CLI on its machine at once. The × also shows on keyboard focus, and VoiceOver offers **End Terminal** on the row. The web workspace has the same × on each terminal and pane.
- **Move to another machine:** drag a terminal's sidebar row, or the ⠿ grip at a pane's top corner, onto a machine: its tile under the terminal list, its name, or any of its terminals in the sidebar. Or right-click the row or the grip and choose **Move to Machine**. In the web workspace, drag a terminal or a pane's ⠿ grip onto a machine under the terminal list, or use **Actions → Move focused terminal to**.
- **Clone to another machine:** the same, holding ⌥ (Alt) as you drop, or **Clone to Machine** in the menus (**Actions → Clone focused terminal to** in the web workspace). A clone starts a new conversation on the destination with all of the source's context (Claude Code `--fork-session`, `codex fork`); the source keeps running and neither one writes the other's transcript.

The hub runs the move, so it finishes even if the app quits or the laptop goes offline. The row shows each phase as it happens:

1. **Check readiness:** the terminal runs Claude Code or Codex, it has a conversation, the destination machine is connected to the hub, and the destination has the same CLI. A Claude Code conversation resumes only in Claude Code and a Codex one only in Codex, so the destination needs that CLI installed and logged in. Nothing changes if a check fails.
2. **Export:** the conversation file from the source machine, or the hub's copy when that machine is offline.
3. **Copy:** the file goes to the destination machine, where the CLI looks for it.
4. **Resume:** a new terminal on the destination continues the same conversation.
5. **Check it is live:** the hub waits about a minute for the destination machine to confirm that the CLI resumed. If it did not, the hub ends the new terminal and the source keeps running.
6. **End the source:** only now does the old terminal end. If this step fails, the conversation still runs on the destination, and the row says so; end the old one with ×.

The destination machine then owns the conversation: its terminal, its transcript and its next turns. The laptop can disconnect.

What does not move:

- Only the conversation moves. Uncommitted changes, untracked files, stashes and local branches stay on the source machine. Commit and push them first, then pull on the destination.
- A git worktree is not copied. If the same folder (under the home folder) exists on the destination, the conversation runs there. If not, it runs in the destination's home folder, and the **Copy** phase says so.
- Shell terminals and sessions started outside the hub cannot move. Continue such a session in a hub terminal first.
- Running processes do not move: a dev server, a build or a test run in the source terminal stops with the source (a clone leaves them running there). The CLI resumes the conversation, not the process state.
- The terminal's own hub runs the move, and the terminal number is that hub's. With two hubs listed (for example a laptop and a Studio), each terminal is moved by the hub that lists it, so terminal 12 on one hub is never confused with terminal 12 on another.
- Moving needs a hub that supports it. An older hub answers with "update the hub", and the web workspace hides the drop targets.

## Quick search

Press **Option+Space** on macOS, or **Ctrl+Alt+Space** on Linux, in any app. A small search window opens on top of other windows.

1. Type your query. The app waits 120 ms after the last key, then calls `GET /api/search?q=<q>&scope=cluster&limit=20` on the selected hub.
2. Each row shows the title, host, provider, time (for example `12m ago`), the folder name and a snippet.
3. Press Up or Down to move in the list.
4. Press Enter to open the session in the main window.
5. Press Cmd+Enter (Ctrl+Enter on Linux) to continue the session. The app sends `POST` to the row's `continue_url` with `{"mode":"auto"}`. A new terminal session starts with the old context. The app opens it in the main window.
6. Press Esc to hide the window. The window also hides when you click outside it.

A newer keystroke replaces an older request. The app drops the reply of the old request.

If the hub answers 404 to `/api/search`, the hub is too old for quick search. The window tells you. Press Enter to open `<hub>/search?q=<q>` in the main window.

If some nodes did not answer, the window lists their names.

### Change the hotkey

Open the hub list. In **Quick-search hotkey**, press **Change**, then press the new keys. You must use at least one modifier key. The app saves the hotkey in `hubs.json`. If another app uses the same hotkey, the app shows an error and keeps the old hotkey. **Reset** goes back to the default for your system. Tools such as Raycast and Alfred often use Option+Space too.

## Notifications

The app calls `GET /api/inbox` on the selected hub every 30 seconds. It shows a notification for each new item. Many new items give one summary notification.

- An item is new when its key is new, or when its time changed. The hub brings an item back when something newer happens there.
- The app keeps the list of seen items in memory only.
- The first poll after a start, or after a hub switch, only records the items. It shows no notification. So the app does not announce your whole inbox each time you start it.
- A notification does not open a page when you click it. Open the window from the tray icon.
- The app polls only the selected hub.

## Keyboard shortcuts

On Linux, use Ctrl where this table says Cmd.

| Keys | Does |
| --- | --- |
| Option+Space (macOS), Ctrl+Alt+Space (Linux) | Opens quick search (global). |
| Cmd+R | Reloads the page. |
| Cmd+[ and Cmd+] | Back and forward. |
| Cmd+Shift+H | Opens the hub list. |
| Cmd+N | Opens a new terminal window (the Workspace). |
| Cmd+W | Hides the window. The app keeps running. |
| Cmd+Q | Quits the app. |

On macOS, click the Dock icon to show the window again. On Linux, use the tray icon.

## Security notes

- The hub has no login. It accepts only loopback and Tailscale clients. The app keeps the same rule. It refuses every other host, with a message.
- Allowed hosts: `127.0.0.0/8`, `::1`, `100.64.0.0/10`, `fd7a:115c:a1e0::/48` and `*.ts.net`. The name `localhost` is not allowed. Use `127.0.0.1`.
- The app checks every hub URL when you add it, when it loads the file, and before every request. It also checks every link that a hub gives (`link`, `continue_url`). It accepts only a path on the same hub.
- Requests from Rust use a 5-second timeout. They do not follow redirects. They do not use a proxy.
- The main window can go only to an allowed hub host. A link to another site (for example GitHub or Slack) opens in your default browser.
- Pages from a hub get no access to the app commands. Only the app's own pages (quick search and the hub list) can change settings or call a hub.
- The app stores no credentials. The config file holds only names, URLs and the hotkey.
- Use `https://<name>.<tailnet>.ts.net` through `tailscale serve` for a remote hub. Plain `http` to a tailnet host is allowed because Tailscale encrypts the traffic.

## Limits

- The builds are not signed or notarised. See Download.
- Only the macOS build was run by hand. The Linux build comes from CI and was not run by hand yet.
- The quick-search window is a normal always-on-top window, not a system panel. When it opens, macOS can bring the main window forward too.
- The app cannot cancel an HTTP request that is already running. It starts no request for an old keystroke, and it drops the reply of an old request. A request ends after 5 seconds at most.

### Desktop updates

Beginning with 0.1.4, Brain Hub checks for a newer stable desktop release at startup. In the application menu, use **Check for Updates…** to check immediately or turn off **Automatically Install Updates**. On macOS, move Brain Hub into a writable Applications folder before updating. On Linux, keep the AppImage in a writable folder. Both install the matching release for the next launch and leave active terminals running. Linux deb and rpm installations announce new versions and use the system package installer. Release downloads are checked against the SHA-256 digest supplied by GitHub. Users on 0.1.3 or earlier install this release once to get the updater.
