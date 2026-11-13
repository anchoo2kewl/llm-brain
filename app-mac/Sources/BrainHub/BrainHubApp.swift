import AppKit
import BrainHubCore
import SwiftUI

@main
struct BrainHubApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var delegate

    var body: some Scene {
        // Each window remembers its terminal. Cmd+N opens another; Window > Merge All Windows makes tabs.
        WindowGroup("Brain Hub", id: "main", for: TerminalRef.self) { $ref in
            MainWindow(selection: $ref)
                .frame(minWidth: 640, minHeight: 360)
        }
        .defaultSize(width: 1180, height: 760)
        .commands { HubCommands() }

        Window("Keyboard Shortcuts", id: "shortcuts") { ShortcutsView() }
            .defaultSize(width: 560, height: 720)

        Settings {
            TabView {
                SettingsView().tabItem { Label("Hubs", systemImage: "server.rack") }
                KeysSettings().tabItem { Label("Keys", systemImage: "keyboard") }
            }
        }

        MenuBarExtra {
            MenuBarContent()
        } label: {
            let n = AppModel.shared.waitingCount
            Image(systemName: n > 0 ? "bell.badge" : "terminal")
            if n > 0 { Text("\(n)") }
        }
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }

    /// A Dock click with no window open brings one back.
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        MainActor.assumeIsolated { AppModel.shared.showMain() }
        return false
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        MainActor.assumeIsolated {
            AppModel.shared.requestNotifications()
            KeyRouter.install()
            GlobalHotkey.register()
            // After sleep the sockets are dead but may not know it yet.
            NSWorkspace.shared.notificationCenter.addObserver(forName: NSWorkspace.didWakeNotification, object: nil, queue: .main) { _ in
                MainActor.assumeIsolated { AppModel.shared.sessions.values.forEach { $0.reconnect() } }
            }
        }
    }
}

struct HubCommands: Commands {
    @FocusedValue(\.windowActions) private var actions
    @Environment(\.openWindow) private var openWindow

    var body: some Commands {
        let _ = (AppModel.shared.openMain = { [openWindow] in openWindow(id: "main") })
        // Cmd+W: the focused pane, else whatever window is in front (sheet, Settings, Shortcuts). Never does nothing.
        CommandGroup(replacing: .saveItem) {
            Button(Shortcut.close.title) { AppModel.shared.closeCommand() }.shortcut(.close)
        }
        CommandGroup(before: .windowList) {
            Button(Shortcut.showTerminals.title) { AppModel.shared.showMain() }.shortcut(.showTerminals)
            Divider()
        }
        CommandGroup(replacing: .help) {
            Button(Shortcut.shortcuts.title) { openWindow(id: "shortcuts") }.shortcut(.shortcuts)
            Button(Shortcut.help.title) { openWindow(id: "shortcuts") }.shortcut(.help)
        }
        CommandGroup(after: .newItem) {
            Button(Shortcut.newTerminal.title) { actions?.newTerminal() }.shortcut(.newTerminal).disabled(actions == nil)
            Button(Shortcut.search.title) { actions?.search() }.shortcut(.search).disabled(actions == nil)
        }
        CommandMenu("Terminal") {
            ForEach(Array(Shortcut.showN.enumerated()), id: \.offset) { i, s in
                Button(s.title) { actions?.select(i) }.shortcut(s)
            }
            Divider()
            Button(Shortcut.splitRight.title) { actions?.pane(.split(.horizontal)) }.shortcut(.splitRight)
            Button(Shortcut.splitDown.title) { actions?.pane(.split(.vertical)) }.shortcut(.splitDown)
            Button(Shortcut.nextPane.title) { actions?.pane(.nextPane(1)) }.shortcut(.nextPane)
            Button(Shortcut.previousPane.title) { actions?.pane(.nextPane(-1)) }.shortcut(.previousPane)
            Button(Shortcut.zoomPane.title) { actions?.pane(.zoom) }.shortcut(.zoomPane).disabled((actions?.paneCount ?? 0) < 2)
            Button(Shortcut.closePane.title) { AppModel.shared.closeCommand() }.shortcut(.closePane)
            Divider()
            Button("Rename…") { actions?.rename() }.disabled(actions?.selected == nil)
            Button(Shortcut.reconnect.title) { actions?.reconnect() }.shortcut(.reconnect).disabled(actions?.selected == nil)
            Divider()
            Button(Shortcut.endTerminal.title) { actions?.close() }.shortcut(.endTerminal).disabled(actions?.selected == nil)
        }
    }
}

/// The menu-bar item: who waits for you.
struct MenuBarContent: View {
    @Environment(\.openWindow) private var openWindow
    private var model: AppModel { AppModel.shared }

    var body: some View {
        let waiting = model.orderedRefs.filter { model.state($0) == .waiting }
        if waiting.isEmpty { Text("No agent is waiting for you") }
        ForEach(waiting, id: \.self) { ref in
            Button {
                NSApp.activate()
                openWindow(id: "main", value: ref)
            } label: {
                let t = model.terminal(ref)
                Text("\(t.map { $0.title.isEmpty ? $0.cli : $0.title } ?? "Terminal \(ref.id)") — \(t?.host ?? "")")
            }
        }
        if let err = model.hubError { Divider(); Text("Hub offline: \(err)") }
        Divider()
        Button("Show Terminals") { AppModel.shared.showMain() }
        SettingsLink { Text("Settings…") }
        Button("Quit") { NSApp.terminate(nil) }.keyboardShortcut("q")
    }
}

/// The hub list: same file as the Tauri app.
struct SettingsView: View {
    private var model: AppModel { AppModel.shared }
    @ViewState private var name = ""
    @ViewState private var url = ""
    @ViewState private var failure: String?

    var body: some View {
        Form {
            Section("Hubs") {
                if model.config.hubs.isEmpty { Text("No hub yet. Add the one on this Mac or on your tailnet.").foregroundStyle(.secondary) }
                ForEach(model.config.hubs) { h in
                    HStack {
                        Image(systemName: h.url == model.hub ? "largecircle.fill.circle" : "circle")
                            .foregroundStyle(h.url == model.hub ? Color.accentColor : .secondary)
                            .onTapGesture { model.selectHub(h.url) }
                        VStack(alignment: .leading) {
                            Text(h.name)
                            Text(h.url).font(.caption).foregroundStyle(.secondary)
                        }
                        Spacer()
                        Button(role: .destructive) {
                            do { try model.removeHub(h.url) } catch { failure = error.localizedDescription }
                        } label: { Image(systemName: "minus.circle") }
                            .buttonStyle(.borderless)
                            .help("Remove")
                    }
                }
            }
            Section("Add a hub") {
                TextField("Name", text: $name, prompt: Text("optional"))
                TextField("URL", text: $url, prompt: Text("https://hub.<tailnet>.ts.net or http://127.0.0.1:8787"))
                    .onSubmit(add)
                HStack {
                    if let failure { Text(failure).foregroundStyle(.red).font(.callout) }
                    Spacer()
                    Button("Add", action: add).disabled(url.isEmpty)
                }
            }
            if let err = model.hubError { Section { Text("The selected hub does not answer: \(err)").foregroundStyle(.red) } }
        }
        .formStyle(.grouped)
        .frame(width: 520)
        .fixedSize(horizontal: false, vertical: true)
    }

    private func add() {
        do {
            try model.addHub(name: name, url: url)
            (name, url, failure) = ("", "", nil)
        } catch {
            failure = error.localizedDescription
        }
    }
}

/// The tmux-style keys in effect and where each came from.
struct KeysSettings: View {
    private var model: AppModel { AppModel.shared }
    @AppStorage(AppModel.prefixOverrideKey) private var override = ""
    @ViewState private var draft = ""
    @ViewState private var hotkey = GlobalHotkey.text
    @ViewState private var hotkeyError: String?


    private func applyHotkey() {
        let old = GlobalHotkey.text
        UserDefaults.standard.set(hotkey, forKey: GlobalHotkey.defaultsKey)
        if GlobalHotkey.register() { hotkeyError = nil } else {
            hotkeyError = "Cannot use \(hotkey): try C-` or M-Space."
            UserDefaults.standard.set(old, forKey: GlobalHotkey.defaultsKey)
            GlobalHotkey.register()
        }
    }

    struct Row: Identifiable { let id: Int; let key: String; let command: String; let source: String }

    var body: some View {
        let keys = model.keys
        let rows = keys.bindings.enumerated().map { i, b in
            Row(id: i, key: b.root ? b.key.description : "\(keys.prefix) \(b.key)", command: b.command + (b.repeats ? "  (repeats)" : ""),
                source: b.source)
        }
        VStack(alignment: .leading, spacing: 10) {
            Text(model.keysFile.map { "Read from \($0), on top of tmux's defaults." } ?? "No ~/.tmux.conf or ~/.config/tmux/tmux.conf: tmux's defaults.")
                .foregroundStyle(.secondary)
            HStack {
                Text("Prefix: \(keys.prefix.description)").font(.body.monospaced())
                Text("from \(keys.prefixSource)").foregroundStyle(.secondary)
                Spacer()
                TextField("Override, e.g. C-a", text: $draft).frame(width: 140)
                    .onSubmit(apply)
                Button("Set", action: apply).disabled(Key(tmux: draft) == nil)
                Button("Use Config") { override = ""; draft = ""; model.loadKeys() }.disabled(override.isEmpty)
                Button("Reload") { model.loadKeys() }.help("Read the tmux config again")
            }
            Table(rows) {
                TableColumn("Key", value: \.key).width(min: 90, ideal: 110)
                TableColumn("Command", value: \.command)
                TableColumn("From", value: \.source).width(min: 100, ideal: 150)
            }
            Text("App shortcuts").font(.headline)
            Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 2) {
                ForEach(Shortcut.all.filter { !$0.title.hasPrefix("Show Terminal ") }) { k in
                    GridRow { Text(k.label).font(.body.monospaced()); Text(k.title) }
                }
                GridRow { Text("⌘1 … ⌘9").font(.body.monospaced()); Text("Show terminal 1 … 9") }
            }
            .font(.callout)
            HStack {
                Text("Bring Brain Hub forward from any app:")
                TextField("C-`", text: $hotkey).frame(width: 80).onSubmit(applyHotkey)
                Button("Set", action: applyHotkey)
                if let hotkeyError { Text(hotkeyError).foregroundStyle(.red) }
            }
            Text("Press the prefix twice to send it to the terminal. A pane is a view: closing it leaves the terminal running. "
                 + "copy-mode opens the hub tmux's copy mode.")
                .font(.caption).foregroundStyle(.secondary)
        }
        .padding()
        .frame(width: 680, height: 680)
        .onAppear { draft = override }
    }

    private func apply() {
        guard Key(tmux: draft) != nil else { return }
        override = draft
        model.loadKeys()
    }
}
