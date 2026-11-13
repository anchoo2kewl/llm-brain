# Mac app

Brain Hub is a small native app for Apple Silicon Macs. It replaces the browser for the hub.

The app source is in `app/`. It uses Tauri v2: a Rust core and the system web view (WKWebView). The app is about 4.6 MB. It has no web framework and no runtime npm packages.

## What it does

- The main window shows the hub's own web pages. The app does not copy them.
- A hub list lets you save many hubs and switch between them.
- A global hotkey opens a quick-search window.
- The app shows a macOS notification for each new inbox item.
- A menu-bar icon gives you Open, Quick search, the hub list and Quit.
- The window remembers its size and position.

## Build

You need Rust (cargo) and Node.js. You do not need full Xcode. The Command Line Tools are enough.

```sh
cd app
npm install
npm run build
```

The build runs `tauri build --target aarch64-apple-darwin`. The npm package `@tauri-apps/cli` is a build tool only. You can use `cargo tauri build` instead if you have installed `cargo-tauri`.

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

## Install

Copy the app to `/Applications`:

```sh
cp -R "app/src-tauri/target/aarch64-apple-darwin/release/bundle/macos/Brain Hub.app" /Applications/
```

The app has only an ad-hoc signature, so macOS can block the first start, mostly if you copy the app from another Mac. Then open System Settings, Privacy and Security, and press Open Anyway. You can also run `xattr -dr com.apple.quarantine "/Applications/Brain Hub.app"`.

On the first start, the window shows the hub list. Add a hub, for example `http://127.0.0.1:8787`. Allow notifications for Brain Hub in System Settings when macOS asks.

## Hub list

Open the list with Cmd+Shift+H, or choose **Manage hubs** in the menu-bar icon.

- Add a hub with a name and a URL.
- Rename or remove a hub with the buttons in its row.
- Press **Use** to show a hub in the main window.
- A dot next to each hub shows if it answers. The app calls `GET /healthz` every 10 seconds while the list is open. A green dot means the hub answered `ok`.
- If you select a hub that does not answer, the window does not change. The list shows the error.

The list is saved as JSON in `~/Library/Application Support/app.llmbrain.hub/hubs.json`. You can edit this file by hand. The app drops any hub that fails the host check at start.

The menu-bar icon also lists the hubs. Choose one to switch.

## Quick search

Press **Option+Space** in any app. A small search window opens on top of other windows.

1. Type your query. The app waits 120 ms after the last key, then calls `GET /api/search?q=<q>&scope=cluster&limit=20` on the selected hub.
2. Each row shows the title, host, provider, time (for example `12m ago`), the folder name and a snippet.
3. Press Up or Down to move in the list.
4. Press Enter to open the session in the main window.
5. Press Cmd+Enter to continue the session. The app sends `POST` to the row's `continue_url` with `{"mode":"auto"}`. A new terminal session starts with the old context. The app opens it in the main window.
6. Press Esc to hide the window. The window also hides when you click outside it.

A newer keystroke replaces an older request. The app drops the reply of the old request.

If the hub answers 404 to `/api/search`, the hub is too old for quick search. The window tells you. Press Enter to open `<hub>/search?q=<q>` in the main window.

If some nodes did not answer, the window lists their names.

### Change the hotkey

Open the hub list. In **Quick-search hotkey**, press **Change**, then press the new keys. You must use at least one modifier key. The app saves the hotkey in `hubs.json`. If another app uses the same hotkey, the app shows an error and keeps the old hotkey. **Reset** goes back to Option+Space. Tools such as Raycast and Alfred often use Option+Space too.

## Notifications

The app calls `GET /api/inbox` on the selected hub every 30 seconds. It shows a notification for each new item. Many new items give one summary notification.

- An item is new when its key is new, or when its time changed. The hub brings an item back when something newer happens there.
- The app keeps the list of seen items in memory only.
- The first poll after a start, or after a hub switch, only records the items. It shows no notification. So the app does not announce your whole inbox each time you start it.
- A notification does not open a page when you click it. Open the window from the menu-bar icon.
- The app polls only the selected hub.

## Keyboard shortcuts

| Keys | Does |
| --- | --- |
| Option+Space | Opens quick search (global). |
| Cmd+R | Reloads the page. |
| Cmd+[ and Cmd+] | Back and forward. |
| Cmd+Shift+H | Opens the hub list. |
| Cmd+W | Hides the window. The app keeps running. |
| Cmd+Q | Quits the app. |

Click the Dock icon to show the window again.

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

- Apple Silicon only. The bundle is built for `aarch64-apple-darwin`.
- The app is not notarised. See Install.
- The quick-search window is a normal always-on-top window, not a system panel. When it opens, macOS can bring the main window forward too.
- The app cannot cancel an HTTP request that is already running. It starts no request for an old keystroke, and it drops the reply of an old request. A request ends after 5 seconds at most.
