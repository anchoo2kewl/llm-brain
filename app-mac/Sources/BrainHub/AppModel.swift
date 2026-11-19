import AppKit
import BrainHubCore
import Observation
import UserNotifications

/// The app's shared state: the saved hubs, what the cluster runs, and the open terminal connections.
@MainActor @Observable
final class AppModel {
    static let shared = AppModel()

    private(set) var config: HubConfig
    let configPath = HubConfig.defaultPath

    private(set) var nodes: ClusterNodes?
    private(set) var terminals: [(hub: String, terminal: Terminal)] = []
    private(set) var live: [LiveSession] = []
    private(set) var endingElsewhere: Set<String> = []
    /// The hub's inbox says these wait for you.
    private(set) var inboxWaiting: Set<TerminalRef> = []
    /// Rang the bell since you last looked. Only for a hub that does not send a terminal's state.
    private(set) var rang: Set<TerminalRef> = []
    /// Terminals whose finished turn you looked at; a terminal leaves when its state changes.
    private(set) var seen: Set<TerminalRef> = []
    @ObservationIgnored private var lastState: [TerminalRef: String] = [:]
    /// Why the selected hub cannot be reached, or nil.
    private(set) var hubError: String?
    private(set) var loaded = false

    /// The sidebar order you dragged terminals to (and so ⌘1…9). Terminals not in it follow, newest first.
    private(set) var order: [TerminalRef] = {
        (try? JSONDecoder().decode([TerminalRef].self, from: UserDefaults.standard.data(forKey: "sidebarOrder") ?? Data())) ?? []
    }() {
        didSet { UserDefaults.standard.set(try? JSONEncoder().encode(order), forKey: "sidebarOrder") }
    }

    /// Terminals moving to another machine (or just moved), with the hub's phases.
    private(set) var migrations: [TerminalRef: Migration] = [:]

    /// Open connections, one per terminal, shared by every window.
    private(set) var sessions: [TerminalRef: TerminalSession] = [:]
    /// The terminal in the key window, when the app is active.
    var focused: TerminalRef? { didSet { if let focused { rang.remove(focused); seen.insert(focused) }; updateBadge() } }
    /// A notification click asks the key window to show this terminal.
    var reveal: TerminalRef?

    /// tmux-style keys: tmux's defaults, then the person's tmux config, then the prefix set in Settings.
    private(set) var keys = KeyTable.defaults
    private(set) var keysFile: String?
    /// The prefix was pressed; the next key is a pane command.
    var prefixArmed = false
    /// Every window's panes, for the key handler.
    @ObservationIgnored var windows: [WeakPanes] = []

    @ObservationIgnored private var poller: Task<Void, Never>?
    @ObservationIgnored private var primed = false

    private init() {
        config = HubConfig.load(HubConfig.defaultPath)
        loadKeys()
        poll()
    }

    var hub: String? { config.selected }
    private var nodeByURL: [String: String] = [:]
    private var lastIdentityCheck: [String: Date] = [:]

    func machineLabel(_ node: String) -> String {
        if nodes?.me.node == node, let saved = config.selectedHub { return saved.name }
        return config.hubs.first { nodeByURL[$0.url] == node }?.name ?? node
    }

    private func rememberNodes(_ cluster: ClusterNodes, base: String) {
        nodeByURL[base] = cluster.me.node
        for peer in cluster.peers where peer.ok {
            if let url = try? HubURL.validate(peer.url) { nodeByURL[url] = peer.info.node }
        }
        // A saved loopback or HTTPS address can differ from the peer's advertised address.
        for saved in config.hubs where nodeByURL[saved.url] == nil {
            if let last = lastIdentityCheck[saved.url], Date().timeIntervalSince(last) < 60 { continue }
            lastIdentityCheck[saved.url] = Date()
            Task {
                guard let client = try? HubClient(base: saved.url),
                      let cluster = try? await client.get(Paths.nodes, as: ClusterNodes.self, timeout: 3)
                else { return }
                nodeByURL[saved.url] = cluster.me.node
            }
        }
    }
    var client: HubClient? { hub.flatMap { try? HubClient(base: $0) } }

    var machines: [Machine] {
        BrainHubCore.machines(terminals: terminals, live: live, nodes: nodes?.names ?? [], keep: Set(sessions.keys), order: order)
    }

    /// Drag in the sidebar: the machine's terminals take the new order, ahead of the terminals not yet ordered.
    func move(in machine: Machine, from: IndexSet, to: Int) {
        var refs = machine.terminals.map(\.ref)
        refs.move(fromOffsets: from, toOffset: to)
        setOrder(refs)
    }

    func moveToTop(_ ref: TerminalRef, in machine: Machine) {
        setOrder([ref] + machine.terminals.map(\.ref).filter { $0 != ref })
    }

    private func setOrder(_ refs: [TerminalRef]) {
        let open = Set(terminals.map { TerminalRef(hub: $0.hub, id: $0.terminal.id) })
        // Ended terminals leave the list, so it stays small.
        order = refs + order.filter { !refs.contains($0) && open.contains($0) }
    }
    var machineNames: [String] { machines.map(\.name) }

    func terminal(_ ref: TerminalRef) -> Terminal? {
        terminals.first { $0.hub == ref.hub && $0.terminal.id == ref.id }?.terminal
    }

    /// Order of the sidebar, for Cmd+1…9.
    var orderedRefs: [TerminalRef] { machines.flatMap { $0.terminals.map(\.ref) } }

    /// The agent's state: the hub's answer. A hub that sends none (an older one) is read from its inbox and the bell.
    func state(_ ref: TerminalRef) -> AgentState {
        let t = terminal(ref)
        if let t, !t.isOpen { return .ended }
        if sessions[ref]?.ended == true { return .ended }
        if let s = t?.state { return s }
        return rang.contains(ref) || (inboxWaiting.contains(ref) && focused != ref) ? .waiting : .working
    }

    /// The row is marked and counted: the agent needs an answer, or it finished a turn you did not look at yet.
    func wantsYou(_ ref: TerminalRef) -> Bool {
        let s = state(ref)
        guard s == .waiting || s == .blocked else { return false }
        guard let t = terminal(ref), t.state != nil else { return true }
        return t.needsAnswer || !seen.contains(ref)
    }
    var waitingCount: Int { orderedRefs.filter(wantsYou).count }

    static let prefixOverrideKey = "tmux.prefixOverride"

    func loadKeys() {
        let conf = tmuxConfig()
        var t = KeyTable.load(conf?.text, source: conf?.path ?? "")
        if let text = UserDefaults.standard.string(forKey: Self.prefixOverrideKey), let k = Key(tmux: text) { t.overridePrefix(k) }
        (keys, keysFile) = (t, conf?.path)
    }

    /// The panes of the key window.
    var keyPanes: WindowPanes? {
        windows.removeAll { $0.value == nil }
        return windows.lazy.compactMap(\.value).first { $0.window != nil && $0.window === NSApp.keyWindow }
    }

    /// Opens a main window; set by the menu commands, which have SwiftUI's openWindow.
    @ObservationIgnored var openMain: (() -> Void)?

    /// Bring a terminal window forward from any state: hidden, minimised, or every window closed.
    func showMain() {
        NSApp.unhide(nil)
        NSApp.activate()
        let mains = windows.compactMap { $0.value?.window }
        if let w = mains.first(where: \.isVisible) ?? mains.first(where: \.isMiniaturized) {
            if w.isMiniaturized { w.deminiaturize(nil) }
            w.makeKeyAndOrderFront(nil)
        } else {
            openMain?()
        }
    }

    /// Cmd+W: dismiss a sheet, close the focused pane (its window with the last pane), or close the window in front.
    func closeCommand() {
        guard let key = NSApp.keyWindow ?? NSApp.mainWindow else { return }
        if key.sheetParent != nil {
            // SwiftUI sheets end through their Cancel button, which answers Escape.
            if let esc = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [], timestamp: 0, windowNumber: key.windowNumber,
                                          context: nil, characters: "\u{1b}", charactersIgnoringModifiers: "\u{1b}", isARepeat: false, keyCode: 53) {
                key.sendEvent(esc)
            }
            return
        }
        if let panes = windows.compactMap(\.value).first(where: { $0.window === key }) { panes.requestClose() } else { key.performClose(nil) }
    }

    func updateBadge() {
        let n = waitingCount
        NSApp.dockTile.badgeLabel = n > 0 ? "\(n)" : nil
    }

    // MARK: hubs

    func addHub(name: String, url: String) throws {
        var c = config
        try c.add(name: name, url: url)
        try save(c)
    }

    func removeHub(_ url: String) throws {
        var c = config
        c.remove(url)
        try save(c)
    }

    func selectHub(_ url: String) {
        var c = config
        c.select(url)
        try? save(c)
    }

    private func save(_ c: HubConfig) throws {
        try c.save(configPath)
        let switched = c.selected != config.selected
        config = c
        if switched {
            terminals = []; live = []; nodes = nil; inboxWaiting = []; primed = false; loaded = false
            poll()
        }
    }

    // MARK: polling

    /// Refresh now, then every 4 seconds.
    func poll() {
        poller?.cancel()
        poller = Task { [weak self] in
            while !Task.isCancelled {
                await self?.refresh()
                try? await Task.sleep(for: .seconds(4))
            }
        }
    }

    func refresh() async {
        guard let client else { hubError = nil; loaded = true; return }
        do {
            async let n = client.get(Paths.nodes, as: ClusterNodes.self)
            async let ts = client.get(Paths.terminals, as: [Terminal].self)
            async let ls = client.get(Paths.liveSessions, as: [LiveSession].self)
            async let ib = client.get(Paths.inbox, as: Inbox.self)
            let (nodes, mine, live) = try await (n, ts, ls)
            // Peers that run terminals of their own (most only answer search); a peer that fails is skipped.
            var all = mine.map { (hub: client.base, terminal: $0) }
            var waiting = Set(((try? await ib)?.waitingTerminals ?? []).map { TerminalRef(hub: client.base, id: $0) })
            for peer in nodes.peers where peer.ok {
                guard let pc = try? HubClient(base: peer.url),
                      let pts = try? await pc.get(Paths.terminals, as: [Terminal].self, timeout: 3), !pts.isEmpty else { continue }
                all += pts.map { (hub: pc.base, terminal: $0) }
                if let pib = try? await pc.get(Paths.inbox, as: Inbox.self, timeout: 3) {
                    waiting.formUnion(pib.waitingTerminals.map { TerminalRef(hub: pc.base, id: $0) })
                }
            }
            guard client.base == hub else { return } // switched hubs meanwhile
            rememberNodes(nodes, base: client.base)
            let fresh = waiting.subtracting(inboxWaiting)
            (self.nodes, self.terminals, self.live, self.inboxWaiting, hubError, loaded) = (nodes, all, live, waiting, nil, true)
            let inFront = NSApp.isActive ? focused : nil
            for (hub, t) in all {
                let ref = TerminalRef(hub: hub, id: t.id)
                guard let s = t.state else { // an older hub: its inbox says who waits
                    if primed, fresh.contains(ref), sessions[ref] == nil { notify(ref) }
                    continue
                }
                let key = "\(s.rawValue)/\(t.askKind ?? "")"
                guard lastState.updateValue(key, forKey: ref) != key else { continue }
                if ref == inFront { seen.insert(ref) } else { seen.remove(ref) }
                if primed, ref != inFront, s == .waiting || s == .blocked { notify(ref) }
            }
            primed = true
            updateBadge()
            await followWorkspace(hubs: Set(all.map(\.hub)).union([client.base]))
        } catch {
            guard client.base == hub else { return }
            hubError = error.localizedDescription
            loaded = true
        }
    }

    // MARK: agents steering the workspace

    /// The last workspace request applied, per hub (-1: not looked yet), and the pane state last reported.
    @ObservationIgnored private var workspaceSeq: [String: Int64] = [:]
    @ObservationIgnored private var reported: [String: String] = [:]

    /// Agents (the Brain MCP) ask a hub to open, focus or zoom its terminals in the open workspaces; this applies the
    /// requests in the key window (open and focus bring it forward) and tells each hub what the windows show.
    /// The terminal id is the asking hub's, so it never picks another hub's terminal with the same number.
    private func followWorkspace(hubs: Set<String>) async {
        for base in hubs {
            guard let c = try? HubClient(base: base),
                  let w = try? await c.get(Paths.workspace(since: max(workspaceSeq[base] ?? -1, 0)), as: WorkspaceUpdate.self, timeout: 3)
            else { continue }
            for i in w.pending(after: workspaceSeq[base] ?? -1, kind: "mac") {
                let panes = keyPanes ?? windows.lazy.compactMap(\.value).first
                switch i.action {
                case "open", "focus":
                    guard let id = i.terminal, terminal(TerminalRef(hub: base, id: id))?.isOpen == true else { continue }
                    showMain()
                    (keyPanes ?? panes)?.show(TerminalRef(hub: base, id: id))
                case "zoom": if panes?.tree.zoomed == nil { panes?.tree.toggleZoom() }
                case "unzoom": if panes?.tree.zoomed != nil { panes?.tree.toggleZoom() }
                default: break // layouts are the web workspace's; here a window splits as you choose
                }
            }
            workspaceSeq[base] = w.seq
            // What the windows show of this hub's terminals.
            for (n, p) in windows.compactMap(\.value).enumerated() {
                let slots = p.tree.panes.compactMap { p.tree.terminals[$0] }.filter { $0.hub == base }.map(\.id)
                let focus = p.tree.focusedTerminal.flatMap { $0.hub == base ? $0.id : nil } ?? 0
                let state: [String: any Sendable] = ["client": "mac:\(n + 1)", "slots": slots, "focus": focus, "zoom": p.tree.zoomed != nil]
                let key = "\(base) \(n) \(slots) \(focus) \(p.tree.zoomed != nil)"
                guard reported["\(base) \(n)"] != key else { continue }
                reported["\(base) \(n)"] = key
                struct Empty: Decodable {}
                _ = try? await c.post(Paths.workspaceState, json: state, as: Empty.self, timeout: 3)
            }
        }
    }

    // MARK: terminals

    func session(_ ref: TerminalRef) -> TerminalSession {
        if let s = sessions[ref] { return s }
        let s = TerminalSession(ref: ref, model: self)
        sessions[ref] = s
        return s
    }

    /// The terminal rang the bell: its agent finished and waits for you.
    func bell(_ ref: TerminalRef) {
        let inFront = focused == ref && NSApp.isActive
        if inFront || terminal(ref)?.state != nil { return } // the hub says when this one waits
        rang.insert(ref)
        updateBadge()
        notify(ref)
    }

    func create(host: String, cli: CLI, path: String, title: String, account: String = "") async throws -> TerminalRef {
        guard let client else { throw HubError("Add a hub first (Settings).") }
        struct Created: Decodable { var id: Int64 }
        let r = try await client.post(Paths.newTerminal, form: [("host", host), ("cli", cli.rawValue), ("path", path),
                                                                ("profile", account), ("format", "json")], as: Created.self)
        let ref = TerminalRef(hub: client.base, id: r.id)
        if !title.trimmingCharacters(in: .whitespaces).isEmpty { try? await rename(ref, to: title) }
        await refresh()
        return ref
    }

    func rename(_ ref: TerminalRef, to title: String) async throws {
        struct Renamed: Decodable { var title: String }
        _ = try await HubClient(base: ref.hub).post(Paths.rename(ref.id), form: [("title", title)], as: Renamed.self)
        await refresh()
    }

    func endElsewhere(_ session: LiveSession) async throws {
        guard let client else { throw HubError("Select a hub first.") }
        let key = "\(client.base)#\(session.id)"
        guard endingElsewhere.insert(key).inserted else { return }
        defer { endingElsewhere.remove(key) }
        struct Closed: Decodable {}
        AppDiagnostics.record("elsewhere_close_requested session=\(session.id)")
        do {
            _ = try await client.post("/api/sessions/\(session.id)/close", json: [:], as: Closed.self, timeout: 30)
            for _ in 0..<30 {
                let running = try await client.get(Paths.liveSessions, as: [LiveSession].self, timeout: 5)
                if !running.contains(where: { $0.id == session.id }) {
                    AppDiagnostics.record("elsewhere_close_confirmed session=\(session.id)")
                    await refresh()
                    return
                }
                try await Task.sleep(for: .seconds(1))
            }
            throw HubError("The stop was requested, but this session is still reported as running. Check its machine agent.")
        } catch {
            AppDiagnostics.record("elsewhere_close_failed session=\(session.id)")
            throw error
        }
    }

    func close(_ ref: TerminalRef) async throws {
        struct Ignored: Decodable {}
        _ = try await HubClient(base: ref.hub).post(Paths.close(ref.id), json: [:], as: Ignored.self, timeout: 30)
        AppDiagnostics.record("terminal_close_confirmed id=\(ref.id)")
        for panes in windows.compactMap(\.value) { panes.tree.forget(ref) }
        sessions[ref]?.stop()
        sessions[ref] = nil
        await refresh()
    }

    /// Continue a conversation in a new terminal (the hub forks it when it can).
    func continueSession(_ hit: SessionHit) async throws -> TerminalRef {
        guard let client else { throw HubError("Add a hub first (Settings).") }
        let reply = try await client.post(hit.continueURL, json: ["mode": "auto"], as: OpenedTerminal.self)
        return try await opened(reply, hub: client.base)
    }

    func continueLive(_ s: LiveSession) async throws -> TerminalRef {
        guard let client else { throw HubError("Add a hub first (Settings).") }
        let reply = try await client.post(Paths.continueSession(s.id), json: ["mode": "auto"], as: OpenedTerminal.self)
        return try await opened(reply, hub: client.base)
    }

    /// Move a conversation to another machine. A peer's session is moved by the peer that holds its copy.
    func move(_ hit: SessionHit, to machine: String, closeSource: Bool) async throws -> TerminalRef {
        guard let client else { throw HubError("Add a hub first (Settings).") }
        var base = client.base
        if let peer = nodes?.peers.first(where: { $0.info.node == hit.node }) { base = try HubURL.validate(peer.url) }
        let reply = try await HubClient(base: base).post(Paths.move(hit.id), json: ["to": machine, "close_source": closeSource],
                                                         as: OpenedTerminal.self)
        return try await opened(reply, hub: base)
    }

    /// Move a terminal's conversation to another machine. Its hub runs the move as a job (it goes on if the app
    /// quits); this follows the phases. Returns the new terminal; on a failure the old one still runs. A clone forks
    /// the conversation there and keeps the source.
    func migrate(_ ref: TerminalRef, to machine: String, clone: Bool = false) async throws -> TerminalRef? {
        struct Started: Decodable { var job: String }
        let c = try HubClient(base: ref.hub)
        let started: Started
        do {
            let body: [String: any Sendable] = clone ? ["to": machine, "clone": true] : ["to": machine, "close_source": true]
            started = try await c.post(Paths.migrate(ref.id), json: body, as: Started.self, timeout: 30)
        } catch let e as HubError where e.message.contains("404") || e.message.contains("405") {
            throw HubError("This hub can't move terminals yet: update the hub.")
        }
        defer { Task { try? await Task.sleep(for: .seconds(20)); self.migrations[ref] = nil } }
        var misses = 0
        while true {
            do {
                let m = try await c.get(Paths.migration(started.job), as: Migration.self)
                migrations[ref] = m
                misses = 0
                if m.done {
                    await refresh()
                    if let e = m.error { throw HubError("\(clone ? "Clone" : "Move") failed: \(e). The terminal still runs where it was.") }
                    return m.dest.map { TerminalRef(hub: ref.hub, id: $0) }
                }
            } catch let e as HubError where e.message.hasPrefix("Move failed") || e.message.hasPrefix("Clone failed") {
                throw e
            } catch {
                misses += 1 // the hub restarting or the network blinking: the job goes on there
                if misses > 30 { throw HubError("Lost track of the move: check the hub's terminals.") }
            }
            try await Task.sleep(for: .seconds(1))
        }
    }

    private func opened(_ reply: OpenedTerminal, hub: String) async throws -> TerminalRef {
        guard let ref = openedTerminalRef(reply, hub: hub, nodes: nodes) else { throw HubError("The hub did not say which terminal it opened.") }
        await refresh()
        return ref
    }

    // MARK: notifications

    nonisolated static var canNotify: Bool { Bundle.main.bundleIdentifier != nil }

    func requestNotifications() {
        guard Self.canNotify else { return }
        let c = UNUserNotificationCenter.current()
        c.delegate = NotificationDelegate.shared
        let reply = UNTextInputNotificationAction(identifier: "reply", title: "Reply", options: [], textInputButtonTitle: "Send",
                                                  textInputPlaceholder: "Type an answer for the agent")
        c.setNotificationCategories([UNNotificationCategory(identifier: "ask", actions: [reply], intentIdentifiers: [])])
        c.requestAuthorization(options: [.alert, .sound, .badge]) { granted, err in
            if !granted { NSLog("Brain Hub: notifications are not allowed (%@)", err?.localizedDescription ?? "turned off in System Settings") }
        }
    }

    private func notify(_ ref: TerminalRef) {
        guard Self.canNotify else { return }
        NSApp.requestUserAttention(.informationalRequest)
        let t = terminal(ref)
        let content = UNMutableNotificationContent()
        content.title = (t?.title.isEmpty == false ? t!.title : t?.cli) ?? "Terminal \(ref.id)"
        content.subtitle = Self.headline(t) + (t.map { " · \($0.host)" } ?? "")
        content.body = t?.ask ?? t?.fix ?? ""
        content.categoryIdentifier = "ask"
        content.sound = .default
        content.userInfo = ["hub": ref.hub, "id": ref.id]
        let req = UNNotificationRequest(identifier: "\(ref.hub)#\(ref.id)", content: content, trigger: nil)
        UNUserNotificationCenter.current().add(req) { err in if let err { NSLog("Brain Hub: notification failed: %@", err.localizedDescription) } }
    }
}

extension AppModel {
    /// Why a terminal wants you, in two or three words.
    static func headline(_ t: Terminal?) -> String {
        switch (t?.state, t?.askKind) {
        case (.blocked, _): "Blocked"
        case (_, "question"): "Needs an answer"
        case (_, "error"): "Stopped on an error"
        case (_, "done"): "Finished its turn"
        default: "Waiting for you"
        }
    }

    /// Type text into a terminal and press Enter (the reply to a notification).
    func send(_ text: String, to ref: TerminalRef) async {
        struct OK: Decodable {}
        do {
            _ = try await HubClient(base: ref.hub).post(Paths.send(ref.id), json: ["text": text, "keys": ["Enter"]], as: OK.self)
            await refresh()
        } catch { NSLog("Brain Hub: reply failed: %@", error.localizedDescription) }
    }
}

final class NotificationDelegate: NSObject, UNUserNotificationCenterDelegate, Sendable {
    static let shared = NotificationDelegate()

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        let info = response.notification.request.content.userInfo
        guard let hub = info["hub"] as? String, let id = (info["id"] as? NSNumber)?.int64Value else { return }
        let ref = TerminalRef(hub: hub, id: id)
        if let typed = response as? UNTextInputNotificationResponse {
            await AppModel.shared.send(typed.userText, to: ref)
            return
        }
        await MainActor.run {
            NSApp.activate()
            AppModel.shared.reveal = ref
        }
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async
        -> UNNotificationPresentationOptions { [.banner, .sound] }
}
