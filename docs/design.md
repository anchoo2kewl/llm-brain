# Desktop design spec

The Mac app (`app-mac/`) and the hub's web pages (which the Tauri app for Linux and Windows shows) follow this one spec.
The values come from the hub's design tokens, `hub/internal/web/static/tokens.css`. The Mac app copies them in
`app-mac/Sources/BrainHub/Theme.swift`. The shared list rules are in `BrainHubCore` (Mac) and
`hub/internal/web/static/wslist.js` (web). Both have tests.

## Font

| Use | Font | Fallback |
| --- | --- | --- |
| App text: session list, titles, Settings | JetBrainsMono Nerd Font | The system UI font |
| Terminal pane | JetBrainsMono Nerd Font | The platform monospace font |

- The font is the JetBrainsMono Nerd Font from [nerd-fonts](https://github.com/ryanoasis/nerd-fonts) **v3.5.1**
  (`JetBrainsMono.zip`, SHA-256 `fab782a66f7d3019da64f6572db9fc5d3a4bcb19f9fa13e2d8a62e3693d6396e`), the
  Omarchy default. The apps bundle the Regular and Bold files. The licence is SIL OFL 1.1 (`OFL.txt` next to the files).
- Files: `hub/internal/web/static/fonts/` (hub pages, and the Mac app copies it into `Contents/Resources/Fonts`) and
  `app/ui/fonts/` (the Tauri app's own pages).
- The Mac terminal sets `font-family = JetBrainsMono Nerd Font` after your Ghostty config. The web terminal measures
  its cells again when the font has loaded, so the columns and the line height match it.

## Session list

The rules come from [design/session-list-research.md](design/session-list-research.md).

| Item | Rule |
| --- | --- |
| Row | One line: the state dot, the name, the folder (last part of the path, muted), then the age on the right. A small provider mark (`CC`, `CX`, `AG`, `>_`) shows only when the list has more than one provider. Context use shows only at 80 % and more (red from 90 %). The summary, the account, the subagents and the shortcut are in the tooltip; the selected session's summary is in the window subtitle. The × to end a session shows on hover. |
| Order | Strictly the latest activity or interaction, most recent first: the agent's last output or state change (`last_activity_at`) and the owner's last input (`last_input_at`, and the input this app sent). An older hub: its `updated` time and the input this app sent. Start time only when there is nothing else. Ties: the higher id. Whole seconds, so jitter under a second never reorders. Status, "finished its turn", polling and selection change nothing. Each machine group sorts the same way. Sessions running elsewhere follow, by the time of their conversation file. |
| Move | A row that moves glides to its new place in 200 ms; no motion with Reduce Motion or `prefers-reduced-motion`. The selected row stays selected and in view. Row heights never change. |
| Selection | Exactly one row looks selected: a 3 px accent bar on the left, the accent background (`--acc-soft` on the web, the system selection on the Mac) and a bold name. Up and Down move it; Enter opens. |
| Colour | Neutral rows and neutral names. Colour only for the selection, the "needs you" pulse and errors. A finished turn shows only in the dot. |
| Session tree | A child session shows under its parent, one level deeper, collapsed by default. The parent row shows the child count with a chevron; click it, or press Right to open and Left to close. A parent ranks by the most recent row under it (a conductor rises when a child works); children rank by their own activity. A child never shows at the top level while its parent is listed. A child on another hub shows that hub's name. Open and closed parents are kept across restarts. The join is (`parent_hub`, `parent_id`) = (`hub`, `id`). A hub without these fields gives the flat list. A search shows every match flat. |
| Needs you | The agent asks a question or shows a permission prompt (the hub's `needs_owner`; an older hub: `blocked`, or `waiting` with `ask_kind` `question`): the dot pulses red (`--st-failed`), slowly (2 s), low contrast. No row background and no sound. A finished turn or an error is a report: it does not pulse. The pulse stops when you open the session. With Reduce Motion, the dot is steady red. |
| Recency | The age (`now`, `2m`, `1h`, `3d`) uses the same time as the order. The dot fades with age: 100 % under 1 minute, 85 % under 10 minutes, 70 % under 1 hour, 55 % under 1 day, then 40 %. |
| Ended or failed | They leave the live list: one collapsed "Ended (n)" group at the bottom. Its rows fade to 55 %. |
| Structure | Machine headers small, upper case and muted. Spacing on an 8 px grid (4 px half steps). No separators between rows. |
| Switcher | ⌘K (Mac) opens the palette: open sessions that match the name come first (names that start with the text, then names that contain it), then conversations. On the web, Ctrl+Shift+K (⌘K on a Mac) goes to the filter; Enter opens the first match. ⌘1 to ⌘9 show sessions 1 to 9 on the Mac. |
| Hub fields | The tree, `needs_owner` and the exact times come with hub 0.1.12 in `GET /api/terminals`. The Mac app reads every hub of the cluster, so it nests a child that runs on another hub. The web workspace reads only its own hub, so there such a child shows only in its own hub's workspace. |

## Restart to update

- When the app installed a newer version of itself, a green **Restart to Update** button shows until the app restarts.
  On the Mac it is in the window toolbar, the app menu and the menu-bar menu. In the Tauri app it is a menu-bar item,
  the first tray item and a button on the Hubs page. There is no dialog. Its tooltip says that sessions run on the hub
  and stay open.
- When a newer release exists that the app did not install (automatic updates off, or a deb, rpm or MSI install), the
  same places show **Update Available** instead.
- The hub's web pages show a small green **Reload to update** sign at the bottom right when the hub restarted with
  another version under an open tab. One click reloads the page.
- One click restarts the app into the new version. The new copy starts after the old one ended.
- A hub's own update shows only in Settings, next to that hub: "Hub 0.1.11 · 0.1.12 installed, restart pending" or
  "Hub 0.1.11 · 0.1.13 available".

## Pasted images

An image you paste or drop on a terminal goes to that terminal's machine, and its path goes into the CLI's input as
before. A small preview of each image then shows at the top right of the pane until you press Return. The red × on a
preview hides the preview only. The app never edits text in the terminal: to leave an image out of the message,
delete its path in the CLI.
