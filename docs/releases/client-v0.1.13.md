# Brain Hub desktop 0.1.13

- **Order by recent use, strictly.** The session list puts the session with the latest activity or input at the top: the agent's last output or state change, and your last input (typing, a paste, a reply). Status changes, "finished its turn", polling and selection never move a row. A move is a short glide (none with Reduce Motion), and the selected session stays selected and in view.
- **Conductor on top.** A parent session ranks by the most recent session under it, so it rises when a child works. Children stay under their parent, by their own activity. Open and closed parents are kept across restarts.
- **A calmer list.** One line per row: dot, name, folder and age. No "Claude Code" chip on every row (a small provider mark only when providers differ), context only at 80 % and more, neutral names. The summary is in the tooltip and in the window subtitle. Ended sessions go to a collapsed "Ended (n)" group at the bottom. Machine headers are small and quiet.
- **Quick switcher.** ⌘K now lists the open sessions that match what you type first, then conversations. On the web, Ctrl+Shift+K (⌘K on a Mac) goes to the filter.
- The design rules and their sources: docs/design/session-list-research.md and docs/design.md.
- The hub's web workspace (what the Linux and Windows app shows) follows the same rules after the hub update that includes them.

Builds: native universal macOS app, Linux (AppImage, deb, rpm) and Windows (msi, exe) desktop packages.
