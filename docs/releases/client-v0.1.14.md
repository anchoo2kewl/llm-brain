# Brain Hub desktop 0.1.14

- **Restart to Update everywhere.** The app checks for a new release at start, every hour and after a wake from sleep. It downloads and installs the release in the background, then shows "Restart to Update". Sessions keep running on the hub during a restart. Windows installs silently. Linux .deb and .rpm show "Update available" with the download, because they need root. The web workspace shows "Reload to update" when the hub version changes.
- **A cleaner sidebar.** Each row has two short lines: the name and age, then the tool, the model and the context %. The selected row shows the context bar and a details grid.
- **A simpler tree.** One chevron opens and closes a parent, and a closed parent hides every session under it. You can end a child by itself. Ending a parent asks once, then ends its children. Drag a child to the top level, or use Make Standalone, to make it independent.
- **Needs me.** A "Needs me (N)" button and ⌘J (Ctrl+Shift+J on the web) open the session that has waited longest, then the next one.
- **Hold my place.** Rows do not move while the pointer or focus is in the list. An "order paused" mark shows this.
- **Closed-parent rollup.** A closed parent shows, for example, "2 need you · 3 working", with the red pulse.
- **Now line and context levels.** The details start with a one-line status, for example "Working 9m · 5 files (+112/−19)". The context % is grey below 75 %, amber from 75 % and red from 85 %. From 80 %, Handoff compacts the session or starts a successor session.
- Mouse reports in the web terminal no longer count as input. Next and previous window keep the start order.

Some features need hub 0.1.14 or later (git summary, waiting time, Handoff). With an older hub, they stay hidden.

Builds: native universal macOS app, Linux (AppImage, deb, rpm) and Windows (msi, exe) desktop packages.
