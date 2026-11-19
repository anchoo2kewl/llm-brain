import AppKit
import BrainHubCore
import SwiftUI

/// Every app shortcut, in one table: the menus take their keys from here and the Keyboard Shortcuts window lists it,
/// so the two cannot drift apart.
struct Shortcut: Identifiable {
    let group: String
    let title: String
    let key: KeyEquivalent
    let modifiers: EventModifiers
    var id: String { title }

    var keyboardShortcut: KeyboardShortcut { KeyboardShortcut(key, modifiers: modifiers) }

    var label: String {
        var s = ""
        if modifiers.contains(.control) { s += "⌃" }
        if modifiers.contains(.option) { s += "⌥" }
        if modifiers.contains(.shift) { s += "⇧" }
        if modifiers.contains(.command) { s += "⌘" }
        switch key {
        case .return: return s + "↩"
        case .tab: return s + "⇥"
        default: return s + String(key.character).uppercased()
        }
    }

    // Windows
    static let newWindow = Shortcut(group: "Windows", title: "New Window", key: "n", modifiers: .command)
    static let showTerminals = Shortcut(group: "Windows", title: "Show Terminals", key: "0", modifiers: .command)
    static let close = Shortcut(group: "Windows", title: "Close Pane, or the Window", key: "w", modifiers: .command)
    static let settings = Shortcut(group: "Windows", title: "Settings", key: ",", modifiers: .command)
    static let shortcuts = Shortcut(group: "Windows", title: "Keyboard Shortcuts", key: "/", modifiers: .command)
    static let help = Shortcut(group: "Windows", title: "Brain Hub Help", key: "/", modifiers: [.command, .shift])
    // Terminals
    static let newTerminal = Shortcut(group: "Terminals", title: "New Terminal…", key: "t", modifiers: .command)
    static let search = Shortcut(group: "Terminals", title: "Search Conversations…", key: "k", modifiers: .command)
    static let reconnect = Shortcut(group: "Terminals", title: "Reconnect", key: "r", modifiers: [.command, .shift])
    static let endTerminal = Shortcut(group: "Terminals", title: "End Terminal…", key: "w", modifiers: [.command, .shift])
    static let showN = (1...9).map {
        Shortcut(group: "Terminals", title: "Show Terminal \($0)", key: KeyEquivalent(Character(String($0))), modifiers: .command)
    }
    // Panes
    static let splitRight = Shortcut(group: "Panes", title: "Split Right", key: "d", modifiers: .command)
    static let splitDown = Shortcut(group: "Panes", title: "Split Down", key: "d", modifiers: [.command, .shift])
    static let nextPane = Shortcut(group: "Panes", title: "Next Pane", key: "]", modifiers: .command)
    static let previousPane = Shortcut(group: "Panes", title: "Previous Pane", key: "[", modifiers: .command)
    static let zoomPane = Shortcut(group: "Panes", title: "Zoom Pane", key: .return, modifiers: [.command, .shift])
    static let closePane = Shortcut(group: "Panes", title: "Close Pane", key: "w", modifiers: [.command, .option])

    static let all: [Shortcut] = [newWindow, showTerminals, close, settings, shortcuts, help, newTerminal, search, reconnect,
                                  endTerminal] + showN + [splitRight, splitDown, nextPane, previousPane, zoomPane, closePane]
}

extension View {
    func shortcut(_ s: Shortcut) -> some View { keyboardShortcut(s.keyboardShortcut) }
}

/// Help > Keyboard Shortcuts: the app's keys, the global key and the tmux bindings in effect.
struct ShortcutsView: View {
    private var model: AppModel { AppModel.shared }

    var body: some View {
        let keys = model.keys
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                ForEach(["Windows", "Terminals", "Panes"], id: \.self) { group in
                    section(group, Shortcut.all.filter { $0.group == group && !$0.title.hasPrefix("Show Terminal ") }.map { ($0.label, $0.title) }
                        + (group == "Terminals" ? [("⌘1 … ⌘9", "Show terminal 1 … 9 (sidebar order) in the focused pane")] : [])
                        + (group == "Windows" ? [(GlobalHotkey.text, "Bring Brain Hub forward from any app (global)")] : []))
                }
                section("tmux (prefix \(keys.prefix.description), \(model.keysFile ?? "tmux defaults"))",
                        keys.bindings.map { b in
                            (b.root ? b.key.description : "\(keys.prefix.description) \(b.key.description)",
                             b.command + (b.repeats ? " (repeats)" : ""))
                        } + [("\(keys.prefix.description) \(keys.prefix.description)", "send the prefix to the terminal")])
                Text("Closing a pane leaves its terminal running on its machine; End Terminal stops it. "
                     + "Change the prefix and the global key in Settings, Keys.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            .padding(20)
        }
        .frame(minWidth: 520, minHeight: 560)
    }

    private func section(_ title: String, _ rows: [(String, String)]) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).font(.headline)
            Grid(alignment: .leading, horizontalSpacing: 18, verticalSpacing: 3) {
                ForEach(Array(rows.enumerated()), id: \.offset) { _, r in
                    GridRow {
                        Text(r.0).font(.body.monospaced()).frame(minWidth: 110, alignment: .leading)
                        Text(r.1)
                    }
                }
            }
        }
    }
}
