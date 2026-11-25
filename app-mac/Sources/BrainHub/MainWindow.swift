import AppKit
import BrainHubCore
import SwiftUI
import WebKit

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
    /// The terminal the end question is about, and the child count it showed: the action ends only what the owner
    /// confirmed. One value, so the question and the count never disagree.
    @ViewState private var closing: (ref: TerminalRef, children: Int)?
    @ViewState private var failure: String?
    @SceneStorage("layout") private var layout = WindowLayout.terminal
    @ViewState private var web = WKWebView()

    var body: some View {
        NavigationSplitView {
            Sidebar(selection: Binding(get: { panes.tree.focusedTerminal }, set: { if let r = $0 { panes.show(r) } }),
                    rename: startRename, close: ask, end: end, openInWindow: { openWindow(value: $0) }, failure: $failure)
                .navigationSplitViewColumnWidth(min: 200, ideal: 260, max: 400)
        } detail: {
            switch (layout, model.hubAddress.flatMap(URL.init(string:))) {
            case (.hub, let url?): HubPage(web: web, url: url)
            case (.both, let url?): HSplitView { HubPage(web: web, url: url); PaneTreeView(panes: panes) }
            default: PaneTreeView(panes: panes)
            }
        }
        .background(WindowReader { w in panes.window = w })
        .font(.appBody)
        .navigationTitle(title)
        .navigationSubtitle(subtitle)
        .toolbar {
            ToolbarItem(placement: .navigation) {
                Picker("Show", selection: $layout) {
                    ForEach(WindowLayout.allCases, id: \.self) { l in
                        Label(l.title, systemImage: l.icon).help("Show \(l == .both ? "the hub and the terminal" : "the " + l.title.lowercased())")
                    }
                }
                .pickerStyle(.segmented)
                .labelStyle(.iconOnly)
                .disabled(model.hub == nil)
            }
            if let v = model.restartVersion {
                ToolbarItem {
                    Button { Updates.restart() } label: { Label("Restart to Update", systemImage: "arrow.clockwise.circle.fill") }
                        .labelStyle(.titleAndIcon).buttonStyle(.borderedProminent).tint(.green).controlSize(.small)
                        .help("Version \(v) is installed. Restart Brain Hub to use it. \(Updates.restartNote)")
                }
            } else if let v = model.availableVersion {
                ToolbarItem {
                    Button { Updates.check(manual: true) } label: { Label("Update Available", systemImage: "arrow.down.circle.fill") }
                        .labelStyle(.titleAndIcon).buttonStyle(.bordered).controlSize(.small)
                        .help("Version \(v) is available. Click to download and install it. \(Updates.restartNote)")
                }
            }
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
        .confirmationDialog(endQuestion, isPresented: Binding(get: { closing != nil }, set: { if !$0 { closing = nil } })) {
            Button((closing?.children ?? 0) > 0 ? "End All" : "End Terminal", role: .destructive) {
                if let c = closing { endNow(c.ref, children: c.children > 0 ? "end" : nil) }
            }
        } message: { Text((closing?.children ?? 0) > 0 ? "Their processes stop on their machines." : "The process in it stops on its machine.") }
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
            if layout == .hub, panes.tree.focusedTerminal != nil { layout = .terminal } // a chosen terminal comes into view
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
            close: { if let s = current { ask(s) } },
            reconnect: { if let s = current { model.session(s).reconnect() } },
            pane: { panes.perform($0) },
            paneCount: panes.tree.panes.count)
    }

    /// End a terminal: its pane closes here and its CLI stops on its machine. A parent ends with its whole subtree,
    /// after one question.
    private func end(_ ref: TerminalRef) {
        if model.liveDescendants(ref) > 0 { ask(ref) } else { endNow(ref) }
    }

    /// Ask before ending: the question names the child sessions that end too.
    private func ask(_ ref: TerminalRef) { closing = (ref, model.liveDescendants(ref)) }

    private func endNow(_ ref: TerminalRef, children: String? = nil) {
        Task { do { try await model.close(ref, children: children) } catch { failure = error.localizedDescription } }
    }

    private var endQuestion: String {
        guard let c = closing, c.children > 0 else { return "End this terminal?" }
        let (ref, n) = (c.ref, c.children)
        let name = model.terminal(ref).map(TerminalRow.name) ?? "this terminal"
        return "End \(name) and its \(n) child session\(n == 1 ? "" : "s")?"
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
        // The session's summary lives here, not in its sidebar row.
        let state = model.state(ref)
        if state == .waiting || state == .blocked, let ask = [t.ask, t.fix].compactMap({ $0 }).first(where: { !$0.isEmpty }) {
            return "\(AppModel.headline(t)): \(ask)"
        }
        return "\(t.host) · \(t.cwd)"
    }
}

struct Sidebar: View {
    @Binding var selection: TerminalRef?
    var rename: (TerminalRef) -> Void
    var close: (TerminalRef) -> Void
    var end: (TerminalRef) -> Void
    var openInWindow: (TerminalRef) -> Void
    @Binding var failure: String?
    @ViewState private var expanded: Set<TerminalRef> = []
    /// Parents whose child sessions show. Collapsed by default; kept across restarts.
    @AppStorage("sidebar.treeOpen") private var treeOpenStore = ""
    @AppStorage("sidebar.endedOpen") private var endedOpen = false
    private var treeOpen: Set<TerminalRef> {
        get { Set((try? JSONDecoder().decode([TerminalRef].self, from: Data(treeOpenStore.utf8))) ?? []) }
        nonmutating set { treeOpenStore = String(decoding: (try? JSONEncoder().encode(Array(newValue))) ?? Data(), as: UTF8.self) }
    }
    @ViewState private var draggingOverSidebar = false
    private var model: AppModel { AppModel.shared }

    /// The rows to show: each top-level session, then its open children (and theirs), one level deeper each.
    private func visible(_ entries: [Machine.Entry], depth: Int = 0, parentHub: String? = nil) -> [TreeRow] {
        entries.flatMap { e -> [TreeRow] in
            let other = depth > 0 && e.terminal.hubNode != parentHub ? e.terminal.hubNode : nil
            let row = TreeRow(entry: e, depth: depth, otherHub: other)
            return [row] + (treeOpen.contains(e.ref) ? visible(e.children, depth: depth + 1, parentHub: e.terminal.hubNode) : [])
        }
    }

    /// The parents above a session in the tree, nearest last.
    private func ancestors(of ref: TerminalRef) -> Set<TerminalRef> {
        func walk(_ e: Machine.Entry, _ path: [TerminalRef]) -> [TerminalRef]? {
            if e.ref == ref { return path }
            for c in e.children { if let p = walk(c, path + [e.ref]) { return p } }
            return nil
        }
        for e in model.machines.flatMap(\.terminals) { if let p = walk(e, []) { return Set(p) } }
        return []
    }

    /// Forget open parents that are gone, only after a good refresh: an outage must not clear the saved state.
    private func forgetGoneParents() {
        guard model.loaded, model.hubError == nil else { return }
        let kept = treeOpen.intersection(model.allRefs)
        if kept != treeOpen { treeOpen = kept }
    }

    private func toggleTree(_ ref: TerminalRef) {
        var open = treeOpen
        if open.contains(ref) { open.remove(ref) } else { open.insert(ref) }
        treeOpen = open
    }

    private func setTree(_ ref: TerminalRef?, open: Bool) -> KeyPress.Result {
        guard let ref, let e = model.machines.flatMap({ $0.terminals.flatMap(\.all) }).first(where: { $0.ref == ref }),
              !e.children.isEmpty, treeOpen.contains(ref) != open else { return .ignored }
        toggleTree(ref)
        return .handled
    }

    var body: some View {
        let machines = model.machines
        let ended = model.ended
        let treeOpen = self.treeOpen // decoded once per render
        ScrollViewReader { proxy in
        List(selection: $selection) {
            if let err = model.hubError {
                Label { Text("Hub offline: \(err)").font(.callout) } icon: { Image(systemName: "wifi.exclamationmark").foregroundStyle(.red) }
            }
            let index = Dictionary(uniqueKeysWithValues: model.orderedRefs.enumerated().map { ($1, $0) })
            ForEach(machines) { m in
                Section {
                    if m.terminals.isEmpty && m.elsewhere.isEmpty {
                        Text("No open terminals").foregroundStyle(.secondary).font(.callout)
                            .onDrop(of: [.utf8PlainText, .plainText], isTargeted: nil) { dropped($0, on: m.name) }
                    }
                    ForEach(visible(m.terminals), id: \.entry.ref) { row in
                        let e = row.entry
                        TerminalRow(ref: e.ref, terminal: e.terminal, number: index[e.ref], selected: selection == e.ref, expanded: $expanded,
                                    tree: .init(depth: row.depth, children: e.children.count, open: treeOpen.contains(e.ref),
                                                hub: row.otherHub, toggle: { toggleTree(e.ref) }),
                                    end: { end(e.ref) })
                            .tag(e.ref)
                            .id(e.ref)
                            .itemProvider { NSItemProvider(object: e.ref.dragText as NSString) }
                            .contextMenu {
                                if selection != e.ref { // the selected row always shows its details
                                    Button(expanded.contains(e.ref) ? "Hide Details" : "Show Details") {
                                        if expanded.contains(e.ref) { expanded.remove(e.ref) } else { expanded.insert(e.ref) }
                                    }
                                }
                                MoveMenus(ref: e.ref) { n, clone in migrate(e.ref, to: n, clone: clone) }
                                Button("Open in New Window") { openInWindow(e.ref) }
                                Button("Rename…") { rename(e.ref) }
                                if row.depth > 0 { Button("Make Standalone") { standalone(e.ref) } }
                                Divider()
                                Button(model.closingTerminals.contains(e.ref) ? "Closing…" : "End Terminal…", role: .destructive) { close(e.ref) }
                                    .disabled(model.closingTerminals.contains(e.ref))
                            }
                    }
                    // A row dragged here from another machine's section moves (⌥: clones) the terminal here.
                    .onInsert(of: [.utf8PlainText, .plainText]) { _, providers in _ = dropped(providers, on: m.name) }
                    if !m.elsewhere.isEmpty {
                        DisclosureGroup("Running elsewhere (\(m.elsewhere.count))") {
                            Button(model.endingElsewhere.isEmpty ? "End All Running Elsewhere" : "Closing sessions…", role: .destructive) { endElsewhere(m.elsewhere) }
                                .buttonStyle(.bordered).tint(.red).controlSize(.large)
                                .disabled(!model.endingElsewhere.isEmpty)
                            ForEach(m.elsewhere) { s in
                                HStack {
                                    VStack(alignment: .leading, spacing: 1) {
                                        Text(s.title.split(separator: "\n").first.map(String.init) ?? s.cli).lineLimit(1)
                                        Text("\(s.cli) · \(s.cwd)").font(.caption).foregroundStyle(.secondary).lineLimit(1)
                                    }
                                    Spacer()
                                    SessionCloseButton(closing: model.endingElsewhere.contains("\(model.hub ?? "")#\(s.id)")) { endElsewhere([s]) }
                                    .help("End this session on its machine")
                                    .accessibilityLabel("End session")
                                    .disabled(model.endingElsewhere.contains("\(model.hub ?? "")#\(s.id)"))
                                }
                                .contextMenu {
                                    Button("Continue in a Terminal") { continueLive(s) }
                                    Button("End Session", role: .destructive) { endElsewhere([s]) }
                                        .disabled(model.endingElsewhere.contains("\(model.hub ?? "")#\(s.id)"))
                                }
                                .onTapGesture(count: 2) { continueLive(s) }
                                .help("Double-click to continue this conversation in a hub terminal")
                            }
                        }
                    }
                } header: {
                    Text(model.machineLabel(m.name).uppercased()).font(.appCaption2).tracking(0.5).foregroundStyle(.tertiary)
                        .frame(maxWidth: .infinity, alignment: .leading).contentShape(Rectangle())
                        .help("Drop a terminal here to move its conversation to \(model.machineLabel(m.name))")
                        .onDrop(of: [.utf8PlainText, .plainText], isTargeted: nil) { dropped($0, on: m.name) }
                }
            }
            // Ended and failed sessions leave the live list: one collapsed section at the bottom.
            if !ended.isEmpty {
                Section {
                    DisclosureGroup(isExpanded: $endedOpen) {
                        ForEach(ended) { e in
                            TerminalRow(ref: e.ref, terminal: e.terminal, number: nil, selected: selection == e.ref, expanded: $expanded, end: nil)
                                .tag(e.ref)
                                .id(e.ref)
                                .contextMenu {
                                    if selection != e.ref {
                                        Button(expanded.contains(e.ref) ? "Hide Details" : "Show Details") {
                                            if expanded.contains(e.ref) { expanded.remove(e.ref) } else { expanded.insert(e.ref) }
                                        }
                                    }
                                }
                        }
                    } label: {
                        Text("Ended (\(ended.count))").font(.appCaption).foregroundStyle(.secondary)
                    }
                }
            }
        }
        // The order follows activity at once; the selected row stays selected and in view.
        .onChange(of: model.allRefs) {
            if let s = selection { proxy.scrollTo(s) }
            forgetGoneParents()
        }
        // A row that moves glides to its new place (no motion with Reduce Motion).
        .animation(SessionStyle.move, value: model.allRefs)
        // A child picked elsewhere (⌘K, a notification): open its parents so its row shows, then show it.
        .onChange(of: selection) {
            guard let s = selection else { return }
            let path = ancestors(of: s)
            if !path.isSubset(of: self.treeOpen) { self.treeOpen = self.treeOpen.union(path) }
            proxy.scrollTo(s)
        }
        .overlay {
            if !model.loaded { ProgressView("Connecting to the hub…").font(.appCaption) }
        }
        }
        // Right opens a parent's child sessions, Left closes them.
        .onKeyPress(.rightArrow) { setTree(selection, open: true) }
        .onKeyPress(.leftArrow) { setTree(selection, open: false) }
        .onDrop(of: [.utf8PlainText, .plainText], isTargeted: $draggingOverSidebar) { _ in false }
        .safeAreaInset(edge: .bottom) {
            if draggingOverSidebar {
                MachineDropPanel { ref, n, clone in migrate(ref, to: n, clone: clone) }
            }
        }
    }

    /// A row dropped on another machine moves there; a child dropped on its own machine goes to the top level.
    private func dropped(_ providers: [NSItemProvider], on machine: String) -> Bool {
        AppModel.droppedRef(providers) { ref in
            guard let t = model.terminal(ref) else { return }
            if t.host != machine { migrate(ref, to: machine, clone: AppModel.cloneModifier) } else if t.isOpen, (t.parentId ?? 0) > 0 { standalone(ref) }
        }
    }

    private func standalone(_ ref: TerminalRef) {
        Task { do { try await model.makeStandalone(ref) } catch { failure = error.localizedDescription } }
    }

    /// Move (or clone) a terminal to another machine; the new terminal is selected when it is live.
    private func migrate(_ ref: TerminalRef, to machine: String, clone: Bool = false) {
        model.startMigration(ref, to: machine, clone: clone, done: { selection = $0 }, failed: { failure = $0 })
    }

    private func endElsewhere(_ sessions: [LiveSession]) {
        Task {
            var errors: [String] = []
            for session in sessions {
                do { try await model.endElsewhere(session) }
                catch { errors.append(error.localizedDescription) }
            }
            if !errors.isEmpty { failure = errors.joined(separator: "\n") }
        }
    }

    private func continueLive(_ s: LiveSession) {
        Task {
            do { selection = try await model.continueLive(s) } catch { failure = error.localizedDescription }
        }
    }
}

/// One visible sidebar row of the session tree.
struct TreeRow {
    var entry: Machine.Entry
    var depth: Int
    /// The child's hub, when it is not its parent's.
    var otherHub: String?
}
