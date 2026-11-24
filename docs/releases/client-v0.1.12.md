# Brain Hub desktop 0.1.12

- **Windows client.** The Tauri app now builds for Windows: an `.msi` and an NSIS setup `.exe` on the release. The app's own pages and links work in WebView2. It does not update itself on Windows yet.
- **Restart to Update.** After the app installed a newer version, a green **Restart to Update** sign shows until you restart, with no dialog. Mac: in the toolbar, the app menu and the menu-bar menu, also after an install from the MCP `desktop_update` tool. Linux AppImage: a menu-bar item, the first tray item and a button on the Hubs page.
- **Versions in Settings.** Settings shows "App 0.1.12 · Hub 0.1.y" and, for each hub, whether a hub update is installed and waits for a restart, or is available. App and hub versions stay separate.
- **Session list.** Strictly newest first; activity and state never move a row (the Mac sidebar's drag order and **Move to Top** are gone). One clear selection (accent bar and bold title); "finished its turn" uses only the dot and the title colour; ended rows fade; rows keep one height; Up and Down move the selection. Each row shows how recent it is (`now`, `2m`, `1h`) and the dot fades with age. A session that needs an answer pulses red, slowly, until you open it (steady with Reduce Motion). With hub 0.1.12, child sessions show under their parent, collapsed, with a count (→ and ← open and close them).
- **JetBrainsMono Nerd Font** (nerd-fonts v3.5.1, SIL OFL 1.1) is bundled for the app text and the terminal.
- **Pasted images** show a small preview at the top right of the pane until you press Return. The red × hides the preview only.
- The hub's web workspace (what the Tauri app shows) follows the same list rules, font and previews after the hub update that includes them.

Builds: native universal macOS app, Linux (AppImage, deb, rpm) and Windows (msi, exe) desktop packages.
