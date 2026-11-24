import Foundation

/// A machine in the sidebar: its open terminals and the conversations running there outside the hub.
public struct Machine: Identifiable, Equatable, Sendable {
    public struct Entry: Identifiable, Hashable, Sendable {
        public var ref: TerminalRef
        public var terminal: Terminal
        /// Its child sessions, newest first. A child whose parent is not listed shows at the top level.
        public var children: [Entry] = []
        public var id: TerminalRef { ref }
        /// This row and every row under it, depth first.
        public var all: [Entry] { [self] + children.flatMap(\.all) }
    }
    public var name: String
    public var terminals: [Entry]
    public var elsewhere: [LiveSession]
    public var id: String { name }
}

/// Newest first by when each terminal opened (`started`), ties and a hub without `started` by the higher id.
/// Activity, state, attention and selection never change the order: only a new or a removed terminal does.
public func newestFirst(_ a: Terminal, _ b: Terminal) -> Bool {
    let x = a.started.flatMap(parseHubTime) ?? .distantPast, y = b.started.flatMap(parseHubTime) ?? .distantPast
    return x != y ? x > y : a.id > b.id
}

/// A hub time: RFC 3339 UTC, with or without fractional seconds (the hub writes `2026-11-23T15:04:05.000Z`).
func parseHubTime(_ s: String) -> Date? { HubTime.fractional.date(from: s) ?? HubTime.whole.date(from: s) }

/// Made once: a sort compares many times. ISO8601DateFormatter is thread-safe.
private enum HubTime {
    nonisolated(unsafe) static let fractional: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()
    nonisolated(unsafe) static let whole = ISO8601DateFormatter()
}

/// Group terminals by machine. `nodes` (the cluster's machines, this hub first) come first even with nothing open,
/// so a new terminal can go there; other hosts follow by name. Ended terminals show only when `keep` holds them
/// (they were opened in this app). Inside a machine, terminals are newest first (`newestFirst`), and so are the
/// conversations running elsewhere (higher id first).
public func machines(terminals: [(hub: String, terminal: Terminal)], live: [LiveSession], nodes: [String],
                     keep: Set<TerminalRef>) -> [Machine] {
    var shown: [Machine.Entry] = []
    for (hub, t) in terminals {
        let ref = TerminalRef(hub: hub, id: t.id)
        if t.isOpen || keep.contains(ref) { shown.append(.init(ref: ref, terminal: t)) }
    }
    var byHost: [String: [Machine.Entry]] = [:]
    for e in sessionTree(shown) { byHost[e.terminal.host, default: []].append(e) }
    let liveByHost = Dictionary(grouping: live, by: \.host)
    var names: [String] = []
    for n in nodes where !names.contains(n) { names.append(n) }
    names += Set(byHost.keys).union(liveByHost.keys).subtracting(names).sorted()
    return names.map { name in
        Machine(name: name,
                terminals: (byHost[name] ?? []).sorted { newestFirst($0.terminal, $1.terminal) },
                elsewhere: (liveByHost[name] ?? []).sorted { $0.id > $1.id })
    }
}

/// Nest each session under its parent: (`parent_hub`, `parent_id`) = (`hub`, `id`). A hub without these fields, or
/// a child whose parent is not in the list, gives a flat list. Every level is newest first.
public func sessionTree(_ entries: [Machine.Entry]) -> [Machine.Entry] {
    struct Key: Hashable { var hub: String; var id: Int64 }
    let keys = Set(entries.compactMap { e in e.terminal.hubNode.map { Key(hub: $0, id: e.terminal.id) } })
    func parent(_ t: Terminal) -> Key? {
        guard let h = t.parentHub, let p = t.parentId, p > 0, keys.contains(Key(hub: h, id: p)) else { return nil }
        return Key(hub: h, id: p)
    }
    let byParent = Dictionary(grouping: entries.filter { parent($0.terminal) != nil }) { parent($0.terminal)! }
    // One row: the hub address it came from, the hub node it names and its id.
    func row(_ e: Machine.Entry) -> String { "\(e.ref.hub)|\(e.terminal.hubNode ?? "")|\(e.terminal.id)" }
    var placed = Set<String>()
    func build(_ e: Machine.Entry) -> Machine.Entry {
        var e = e
        placed.insert(row(e))
        guard let node = e.terminal.hubNode else { return e }
        let kids = (byParent[Key(hub: node, id: e.terminal.id)] ?? []).filter { !placed.contains(row($0)) }
        e.children = kids.sorted { newestFirst($0.terminal, $1.terminal) }.map(build)
        return e
    }
    var top = entries.filter { parent($0.terminal) == nil }.sorted { newestFirst($0.terminal, $1.terminal) }.map(build)
    // Bad data (a session that is its own ancestor) has no root: show those sessions at the top level, never hide them.
    for e in entries.sorted(by: { newestFirst($0.terminal, $1.terminal) }) where !placed.contains(row(e)) { top.append(build(e)) }
    return top.sorted { newestFirst($0.terminal, $1.terminal) }
}
