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

/// Group terminals by machine. `nodes` (the cluster's machines, this hub first) come first even with nothing open,
/// so a new terminal can go there; other hosts follow by name. Ended terminals show only when `keep` holds them
/// (they were opened in this app). Inside a machine, the terminals in `order` (the order you dragged them to) come
/// first, then the others, newest first.
public func machines(terminals: [(hub: String, terminal: Terminal)], live: [LiveSession], nodes: [String],
                     keep: Set<TerminalRef>, order: [TerminalRef] = []) -> [Machine] {
    let rank = Dictionary(order.enumerated().map { ($1, $0) }, uniquingKeysWith: { first, _ in first })
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
                terminals: (byHost[name] ?? []).sorted { a, b in
                    switch (rank[a.ref], rank[b.ref]) {
                    case let (x?, y?): x < y
                    case (.some, nil): true
                    case (nil, .some): false
                    case (nil, nil): a.terminal.updated > b.terminal.updated
                    }
                },
                elsewhere: (liveByHost[name] ?? []).sorted { $0.updated > $1.updated })
    }
}
