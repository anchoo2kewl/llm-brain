import AppKit
import BrainHubCore
import SwiftUI

/// What the menu commands act on: the key window.
struct WindowActions {
    var newTerminal: () -> Void
    var search: () -> Void
    var select: (Int) -> Void
    var selected: TerminalRef?
    var rename: () -> Void
    var close: () -> Void
    var reconnect: () -> Void
    var pane: (PaneAction) -> Void
    var paneCount: Int
}

struct WindowActionsKey: FocusedValueKey { typealias Value = WindowActions }

extension FocusedValues {
    var windowActions: WindowActions? {
        get { self[WindowActionsKey.self] }
        set { self[WindowActionsKey.self] = newValue }
    }
}

/// SwiftUI's `State` property wrapper. In the macOS 27 SDK, `@State` resolves to a macro whose plugin ships only with
/// Xcode; this name reaches the wrapper, so the app also builds with the Command Line Tools alone.
typealias ViewState<Value> = SwiftUI.State<Value>

/// One window: machines and their terminals on the left, the window's panes on the right (one pane until you split).
struct MainWindow: View {
    /// The window's value: the terminal in the focused pane, so a reopened window shows it again.
    @Binding var selection: TerminalRef?
    @Environment(\.openWindow) private var openWindow
    @Environment(\.controlActiveState) private var active
    private var model: AppModel { AppModel.shared }
    @ViewState private var panes = WindowPanes()
    @ViewState private var renaming: TerminalRef?
    @ViewState private var newTitle = ""
    @ViewState private var closing: TerminalRef?
    @ViewState private var failure: String?

    var body: some View {
        NavigationSplitView {
            Sidebar(selection: Binding(get: { panes.tree.focusedTerminal }, set: { if let r = $0 { panes.show(r) } }),
                    rename: startRename, close: { closing = $0 }, openInWindow: { openWindow(value: $0) }, failure: $failure)
                .navigationSplitViewColumnWidth(min: 200, ideal: 260, max: 400)
        } detail: {
            PaneTreeView(panes: panes)
        }
        .background(WindowReader { w in panes.window = w })
        .navigationTitle(title)
        .navigationSubtitle(subtitle)
        .toolbar {
            ToolbarItemGroup {
                if model.config.hubs.count > 1 {
                    Menu {
                        ForEach(model.config.hubs) { h in
                            Button { model.selectHub(h.url); panes.tree = PaneTree() } label: {
                                if h.url == model.hub { Label(h.name, systemImage: "checkmark") } else { Text(h.name) }
                            }
                        }
                    } label: { Label(model.config.selectedHub?.name ?? "Hub", systemImage: "server.rack") }
                        .help("Switch hub")
                }
                Button { panes.perform(.split(.horizontal)) } label: { Label("Split", systemImage: "rectangle.split.2x1") }
                    .help("Split the pane (⌘D, or \(model.keys.prefix.description) then a split key)")
                Button { panes.showSearch = true } label: { Label("Search", systemImage: "magnifyingglass") }
                    .help("Search conversations across the cluster (⌘K)")
                Button { panes.showNew = true } label: { Label("New Terminal", systemImage: "plus") }
                    .help("New terminal (⌘T)")
                    .disabled(model.hub == nil)
            }
        }
        .sheet(isPresented: Binding(get: { panes.showNew }, set: { panes.showNew = $0 })) { NewTerminalSheet { panes.show($0) } }
        .sheet(isPresented: Binding(get: { panes.showSearch }, set: { panes.showSearch = $0 })) { SearchPalette { panes.show($0) } }
        .alert("Rename terminal", isPresented: Binding(get: { renaming != nil }, set: { if !$0 { renaming = nil } })) {
            TextField("Title", text: $newTitle)
            Button("Rename") {
                guard let ref = renaming else { return }
                let title = newTitle
                Task { do { try await model.rename(ref, to: title) } catch { failure = error.localizedDescription } }
            }
            Button("Cancel", role: .cancel) {}
        }
        .confirmationDialog("End this terminal?", isPresented: Binding(get: { closing != nil }, set: { if !$0 { closing = nil } })) {
            Button("End Terminal", role: .destructive) {
                guard let ref = closing else { return }
                if let p = panes.tree.pane(showing: ref) { panes.tree.focus(p); panes.tree.close() }
                Task { do { try await model.close(ref) } catch { failure = error.localizedDescription } }
            }
        } message: { Text("The process in it stops on its machine.") }
        .confirmationDialog("Close this pane?", isPresented: Binding(get: { panes.confirmClose }, set: { panes.confirmClose = $0 })) {
            Button("Close Pane") { panes.closeNow() }.keyboardShortcut(.defaultAction)
        } message: { Text("Its agent is still working. The terminal keeps running on its machine; open it again from the sidebar.") }
        .alert("Something went wrong", isPresented: Binding(get: { failure != nil }, set: { if !$0 { failure = nil } })) {
            Button("OK") {}
        } message: { Text(failure ?? "") }
        .focusedSceneValue(\.windowActions, actions)
        .onAppear {
            model.windows.append(WeakPanes(value: panes))
            if let s = selection, panes.tree.focusedTerminal == nil { panes.show(s) }
        }
        .onChange(of: panes.tree.focusedTerminal) {
            selection = panes.tree.focusedTerminal
            focus()
        }
        .onChange(of: active, initial: true) { focus() }
        .onChange(of: model.reveal) {
            guard let ref = model.reveal, active == .key else { return }
            panes.show(ref)
            model.reveal = nil
        }
    }

    private var actions: WindowActions {
        let current = panes.tree.focusedTerminal
        return WindowActions(
            newTerminal: { if model.hub != nil { panes.showNew = true } },
            search: { panes.showSearch = true },
            select: { i in let refs = model.orderedRefs; if i < refs.count { panes.show(refs[i]) } },
            selected: current,
            rename: { if let s = current { startRename(s) } },
            close: { closing = current },
            reconnect: { if let s = current { model.session(s).reconnect() } },
            pane: { panes.perform($0) },
            paneCount: panes.tree.panes.count)
    }

    private func startRename(_ ref: TerminalRef) {
        newTitle = model.terminal(ref)?.title ?? ""
        renaming = ref
    }

    private func focus() {
        if active == .key { model.focused = panes.tree.focusedTerminal }
    }

    private var title: String {
        guard let ref = panes.tree.focusedTerminal else { return "Brain Hub" }
        guard let t = model.terminal(ref) else { return "Terminal \(ref.id)" }
        return t.title.isEmpty ? t.cli : t.title
    }

    private var subtitle: String {
        guard let ref = panes.tree.focusedTerminal, let t = model.terminal(ref) else { return "" }
        return "\(t.host) · \(t.cwd)"
    }
}

struct Sidebar: View {
    @Binding var selection: TerminalRef?
    var rename: (TerminalRef) -> Void
    var close: (TerminalRef) -> Void
    var openInWindow: (TerminalRef) -> Void
    @Binding var failure: String?
    private var model: AppModel { AppModel.shared }

    var body: some View {
        List(selection: $selection) {
            if let err = model.hubError {
                Label { Text("Hub offline: \(err)").font(.callout) } icon: { Image(systemName: "wifi.exclamationmark").foregroundStyle(.red) }
            }
            let index = Dictionary(uniqueKeysWithValues: model.orderedRefs.enumerated().map { ($1, $0) })
            ForEach(model.machines) { m in
                Section(m.name) {
                    if m.terminals.isEmpty && m.elsewhere.isEmpty {
                        Text("No open terminals").foregroundStyle(.secondary).font(.callout)
                    }
                    ForEach(m.terminals) { e in
                        row(e, number: index[e.ref])
                            .tag(e.ref)
                            .contextMenu {
                                Button("Open in New Window") { openInWindow(e.ref) }
                                Button("Rename…") { rename(e.ref) }
                                Divider()
                                Button("End Terminal…", role: .destructive) { close(e.ref) }
                            }
                    }
                    if !m.elsewhere.isEmpty {
                        DisclosureGroup("Running elsewhere (\(m.elsewhere.count))") {
                            ForEach(m.elsewhere) { s in
                                VStack(alignment: .leading, spacing: 1) {
                                    Text(s.title.split(separator: "\n").first.map(String.init) ?? s.cli).lineLimit(1)
                                    Text("\(s.cli) · \(s.cwd)").font(.caption).foregroundStyle(.secondary).lineLimit(1)
                                }
                                .contextMenu { Button("Continue in a Terminal") { continueLive(s) } }
                                .onTapGesture(count: 2) { continueLive(s) }
                                .help("Double-click to continue this conversation in a hub terminal")
                            }
                        }
                    }
                }
            }
        }
        .overlay {
            if !model.loaded { ProgressView() }
        }
    }

    private func row(_ e: Machine.Entry, number: Int?) -> some View {
        let t = e.terminal
        return HStack(spacing: 8) {
            Circle().fill(color(model.state(e.ref))).frame(width: 8, height: 8)
                .help(help(model.state(e.ref)))
            VStack(alignment: .leading, spacing: 1) {
                Text(t.title.isEmpty ? t.cli : t.title).lineLimit(1)
                Text("\(t.cli) · \(t.cwd)").font(.caption).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer(minLength: 0)
            if let number, number < 9 { Text("⌘\(number + 1)").font(.caption2).foregroundStyle(.tertiary) }
        }
    }

    private func continueLive(_ s: LiveSession) {
        Task {
            do { selection = try await model.continueLive(s) } catch { failure = error.localizedDescription }
        }
    }

    private func color(_ s: AppModel.DotState) -> SwiftUI.Color {
        switch s { case .working: .green; case .waiting: .orange; case .ended: .gray }
    }

    private func help(_ s: AppModel.DotState) -> String {
        switch s { case .working: "Running"; case .waiting: "Waiting for you"; case .ended: "Ended" }
    }
}
