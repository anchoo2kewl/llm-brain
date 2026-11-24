import Foundation

/// A machine in the sidebar: its open terminals and the conversations running there outside the hub.
public struct Machine: Identifiable, Equatable, Sendable {
    public struct Entry: Identifiable, Hashable, Sendable {
        public var ref: TerminalRef
        public var terminal: Terminal
        public var id: TerminalRef { ref }
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

/// A hub time: RFC 3339 with or without fractional seconds.
func parseHubTime(_ s: String) -> Date? {
    let f = ISO8601DateFormatter()
    f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    if let d = f.date(from: s) { return d }
    f.formatOptions = [.withInternetDateTime]
    return f.date(from: s)
}

/// Group terminals by machine. `nodes` (the cluster's machines, this hub first) come first even with nothing open,
/// so a new terminal can go there; other hosts follow by name. Ended terminals show only when `keep` holds them
/// (they were opened in this app). Inside a machine, terminals are newest first (`newestFirst`), and so are the
/// conversations running elsewhere (higher id first).
public func machines(terminals: [(hub: String, terminal: Terminal)], live: [LiveSession], nodes: [String],
                     keep: Set<TerminalRef>) -> [Machine] {
    var byHost: [String: [Machine.Entry]] = [:]
    for (hub, t) in terminals {
        let ref = TerminalRef(hub: hub, id: t.id)
        if t.isOpen || keep.contains(ref) { byHost[t.host, default: []].append(.init(ref: ref, terminal: t)) }
    }
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
