# Session list: research and rules

Goal: a list of many long-running agent sessions that stays calm, so the owner sees at a glance which session needs
them and which one is selected. This note collects what polished apps do and turns it into rules for the Mac app,
the Tauri app (Linux, Windows) and the hub web list. The applied spec is [../design.md](../design.md).

## What the references do

| Source | What it shows |
| --- | --- |
| Linear, "A calmer interface for a product in motion" (2026) and the 2024 redesign | Navigation must recede once you arrive. Softer contrast and fewer separators reduce visual noise. The task content stays in focus. |
| Apple HIG, Sidebars; Lists and tables | Rows of text are easiest to scan. A navigation list keeps the selected row highlighted. Put secondary actions in a context menu, not in the row. |
| Slack, sidebar preferences | Unread is shown by weight (bold name). The sidebar can hide quiet channels; colour is kept for mentions. |
| Discord, unread and mention indicators | Two levels only: a quiet dot for activity and a red badge when someone needs you. Users complain when the quiet marker shows where it adds nothing. |
| Arc, auto-archive of idle tabs | Idle items leave the main list and go to an archive you open on demand. |
| Warp, vertical tabs | A sidebar of sessions with a small status dot and an unread dot; two densities; collapsible groups. |
| Raycast, Switch Windows | Type a name to jump to any open item: the keyboard path to a session in a long list. |
| Things 3 (Apple Design Awards 2009 and 2017) | Restraint: few elements per row, headings for groups, much whitespace. |
| WCAG 2.2: 1.4.1 Use of Color, 1.4.11 Non-text Contrast, 2.2.2 Pause, Stop, Hide, 2.3.3 Animation from Interactions | Do not rely on colour alone; a state cue needs 3:1 contrast; motion that starts by itself must be stoppable or not needed; respect reduced motion. |

## Rules

1. **Two short lines per row.** The name and the age; then, muted, the tool, the model and the context used. No folder
   and no summary in the row: they show in a tooltip and in the selected session's header (Linear, Things 3, HIG).
   The selected row adds the context bar and a small grid of details (owner request, 2026-11-25).
2. **No repeated chips.** Do not show "Claude Code" on every row: show a small tool mark (`CC`, `CX`, `AG`) and the
   model's short name (Linear, Things 3).
3. **Neutral rows.** Colour only for the selection, the "needs you" signal and errors (Linear, Discord).
4. **Two levels of attention.** A quiet dot for state; a gentle red pulse only when the session waits for you. A
   finished turn is not a call to action (Discord, Slack).
5. **One selection.** An accent bar, the accent background and a bold name, for the selected row only. Not colour
   alone (HIG, WCAG 1.4.1).
6. **Ended sessions leave the list.** They go to a collapsed "Ended (n)" section at the bottom (Arc).
7. **Order by recent activity, strictly.** The latest of the agent's last output or state change and the owner's last
   input. A move is short and calm; the selected row stays selected and in view; row heights never change.
8. **Quiet structure.** Small, muted section headers (machine). Spacing on an 8 px grid (4 px half steps). Fewer
   separators (Linear).
9. **Keyboard first.** Up and Down, Enter to open, Cmd/Ctrl+1 to 9, and a switcher (Cmd/Ctrl+K) that finds a session
   by name (Raycast, Warp).
10. **Respect reduced motion.** No pulse and no move animation with Reduce Motion or `prefers-reduced-motion`
    (WCAG 2.3.3, 2.2.2).
11. **Finished empty, loading and error states.** A short title, one sentence and one action; never a blank pane.

## Sources

- Linear: [A calmer interface for a product in motion](https://linear.app/now/behind-the-latest-design-refresh), [UI refresh changelog (2026-03-12)](https://linear.app/changelog/2026-03-12-ui-refresh), [How we redesigned the Linear UI](https://linear.app/now/how-we-redesigned-the-linear-ui)
- Apple: [HIG Sidebars](https://developer.apple.com/design/human-interface-guidelines/sidebars), [HIG Lists and tables](https://developer.apple.com/design/human-interface-guidelines/lists-and-tables)
- Slack: [Adjust your sidebar preferences](https://slack.com/help/articles/212596808-Adjust-your-sidebar-preferences)
- Discord: [New Server Notification Settings](https://support.discord.com/hc/en-us/articles/21084266106775-New-Server-Notification-Settings-Experiment)
- Arc: [Arc for macOS release notes](https://resources.arc.net/hc/en-us/articles/20498377604887-Arc-for-macOS-2023-Release-Notes)
- Warp: [Vertical tabs](https://docs.warp.dev/terminal/windows/vertical-tabs/)
- Raycast: [Navigation](https://manual.raycast.com/navigation)
- Things 3: [MacStories review](https://www.macstories.net/reviews/things-3-beauty-and-delight-in-a-task-manager/)
- W3C: [Understanding 1.4.11 Non-text Contrast](https://www.w3.org/WAI/WCAG22/Understanding/non-text-contrast), [Understanding 2.2.2 Pause, Stop, Hide](https://www.w3.org/WAI/WCAG22/Understanding/pause-stop-hide), [Understanding 2.3.3 Animation from Interactions](https://www.w3.org/WAI/WCAG21/Understanding/animation-from-interactions)

Ghostty and Apple Mail were not researched separately: Ghostty has no session list, and Mail repeats the HIG list rules.
