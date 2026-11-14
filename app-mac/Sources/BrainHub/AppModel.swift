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
    /// The hub's inbox says these wait for you.
    private(set) var inboxWaiting: Set<TerminalRef> = []
    /// Rang the bell since you last looked.
    private(set) var rang: Set<TerminalRef> = []
    /// Why the selected hub cannot be reached, or nil.
    private(set) var hubError: String?
    private(set) var loaded = false

    /// The sidebar order you dragged terminals to (and so ⌘1…9). Terminals not in it follow, newest first.
    private(set) var order: [TerminalRef] = {
        (try? JSONDecoder().decode([TerminalRef].self, from: UserDefaults.standard.data(forKey: "sidebarOrder") ?? Data())) ?? []
    }() {
        didSet { UserDefaults.standard.set(try? JSONEncoder().encode(order), forKey: "sidebarOrder") }
    }

    /// Open connections, one per terminal, shared by every window.
    private(set) var sessions: [TerminalRef: TerminalSession] = [:]
    /// The terminal in the key window, when the app is active.
    var focused: TerminalRef? { didSet { if let focused { rang.remove(focused) }; updateBadge() } }
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

    enum DotState { case working, waiting, ended }
    func state(_ ref: TerminalRef) -> DotState {
        if let t = terminal(ref), !t.isOpen { return .ended }
        if sessions[ref]?.ended == true { return .ended }
        if rang.contains(ref) || (inboxWaiting.contains(ref) && focused != ref) { return .waiting }
        return .working
    }
    var waitingCount: Int { orderedRefs.filter { state($0) == .waiting }.count }

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
            let fresh = waiting.subtracting(inboxWaiting)
            (self.nodes, self.terminals, self.live, self.inboxWaiting, hubError, loaded) = (nodes, all, live, waiting, nil, true)
            if primed { for ref in fresh where sessions[ref] == nil { notify(ref) } }
            primed = true
            updateBadge()
        } catch {
            guard client.base == hub else { return }
            hubError = error.localizedDescription
            loaded = true
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
        if inFront { return }
        rang.insert(ref)
        updateBadge()
        notify(ref)
    }

    func create(host: String, cli: CLI, path: String, title: String) async throws -> TerminalRef {
        guard let client else { throw HubError("Add a hub first (Settings).") }
        struct Created: Decodable { var id: Int64 }
        let r = try await client.post(Paths.newTerminal, form: [("host", host), ("cli", cli.rawValue), ("path", path),
                                                                ("format", "json")], as: Created.self)
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

    func close(_ ref: TerminalRef) async throws {
        struct Ignored: Decodable {}
        _ = try await HubClient(base: ref.hub).post(Paths.close(ref.id), json: [:], as: Ignored.self, timeout: 30)
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
        content.body = "Waiting for you" + (t.map { " on \($0.host)" } ?? "")
        content.sound = .default
        content.userInfo = ["hub": ref.hub, "id": ref.id]
        let req = UNNotificationRequest(identifier: "\(ref.hub)#\(ref.id)", content: content, trigger: nil)
        UNUserNotificationCenter.current().add(req) { err in if let err { NSLog("Brain Hub: notification failed: %@", err.localizedDescription) } }
    }
}

final class NotificationDelegate: NSObject, UNUserNotificationCenterDelegate, Sendable {
    static let shared = NotificationDelegate()

    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        let info = response.notification.request.content.userInfo
        guard let hub = info["hub"] as? String, let id = (info["id"] as? NSNumber)?.int64Value else { return }
        await MainActor.run {
            NSApp.activate()
            AppModel.shared.reveal = TerminalRef(hub: hub, id: id)
        }
    }

    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async
        -> UNNotificationPresentationOptions { [.banner, .sound] }
}
