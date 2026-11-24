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

| Item | Rule |
| --- | --- |
| Order | Newest first by when the session opened (`started`), ties by the higher id. A hub without `started`: by id. Activity, state, attention, recency, focus and selection never change the order. Only a new session (it goes on top) or a removed one changes it. Each machine group sorts the same way. Sessions running elsewhere follow, higher id first. |
| Selection | Exactly one row looks selected: a 3 px accent bar on the left, the accent background (`--acc-soft` on the web, the system selection on the Mac) and a bold title. Up and Down move the selection. |
| Attention | "Finished its turn" or waiting: the dot and the title colour (`--st-waiting`: `#b45309` light, `#fbbf24` dark). Never a row background. |
| Needs you | The agent asks a question, shows a permission prompt or stopped on an error: the dot pulses red (`--st-failed`), slowly (2 s), low contrast. No row background and no sound. A finished turn that is only a report does not pulse. The pulse stops when you open the session. With Reduce Motion (macOS) or `prefers-reduced-motion`, the dot is steady red. |
| Recency | The row shows how long ago the session ran or got input: `now`, `2m`, `1h`, `3d`. It uses the later of `last_activity_at` and `last_input_at`, else `updated`. The dot fades with age: 100 % under 1 minute, 85 % under 10 minutes, 70 % under 1 hour, 55 % under 1 day, then 40 %. |
| Ended or failed | The row fades to 55 %. |
| Row height | Fixed: the title line, the badge or folder line and a preview line (two lines on the Mac) that is kept when it is empty. Selecting a row never reorders, resizes or scrolls the list. |

## Restart to update

- When the app installed a newer version of itself, a green **Restart to Update** button shows until the app restarts.
  On the Mac it is in the window toolbar, the app menu and the menu-bar menu. In the Tauri app it is a menu-bar item,
  the first tray item and a button on the Hubs page. There is no dialog.
- One click restarts the app into the new version. The new copy starts after the old one ended.
- A hub's own update shows only in Settings, next to that hub: "Hub 0.1.11 · 0.1.12 installed, restart pending" or
  "Hub 0.1.11 · 0.1.13 available".

## Pasted images

An image you paste or drop on a terminal goes to that terminal's machine, and its path goes into the CLI's input as
before. A small preview of each image then shows at the top right of the pane until you press Return. The red × on a
preview hides the preview only. The app never edits text in the terminal: to leave an image out of the message,
delete its path in the CLI.
