import Foundation

/// A machine in the sidebar: its open terminals, its ended ones, and the conversations running there outside the hub.
public struct Machine: Identifiable, Equatable, Sendable {
    public struct Entry: Identifiable, Hashable, Sendable {
        public var ref: TerminalRef
        public var terminal: Terminal
        /// When this session last worked or got input (`recentKey`).
        public var recent: Date = .distantPast
        /// The latest `recent` of this row and every row under it: a parent rises when a child works.
        public var rank: Date = .distantPast
        /// Its child sessions, most recent first. A child whose parent is not listed shows at the top level.
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

/// The sidebar's order key, strictly the latest activity or interaction: the agent's last output or state change
/// (`last_activity_at`) and the owner's last input (`last_input_at`, or `input`: what this app sent). An older hub
/// has neither field: then its `updated` time. Start time only when there is nothing else. Whole seconds, so a
/// change smaller than one second never reorders the list.
public func recentKey(_ t: Terminal, input: Date? = nil) -> Date {
    let hub = [t.lastActivity, t.lastInput].compactMap { $0.flatMap(parseHubTime) }
    let base = hub.max() ?? parseHubTime(t.updated)
    let d = [base, input].compactMap { $0 }.max() ?? t.started.flatMap(parseHubTime) ?? .distantPast
    return Date(timeIntervalSince1970: d.timeIntervalSince1970.rounded(.down))
}

/// Most recent first; ties: the higher id.
public func mostRecentFirst(_ a: Machine.Entry, _ b: Machine.Entry) -> Bool {
    a.rank != b.rank ? a.rank > b.rank : a.terminal.id > b.terminal.id
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
/// (they were opened in this app), in `endedSessions`. Rows are most recent first (`recentKey`, with `inputs`: when this app
/// last sent input to each terminal); the conversations running elsewhere: higher id first.
public func machines(terminals: [(hub: String, terminal: Terminal)], live: [LiveSession], nodes: [String],
                     keep: Set<TerminalRef>, inputs: [TerminalRef: Date] = [:]) -> [Machine] {
    var byHost: [String: [Machine.Entry]] = [:]
    for e in sessionTree(entries(terminals, open: true, keep: keep, inputs: inputs)) { byHost[e.terminal.host, default: []].append(e) }
    let liveByHost = Dictionary(grouping: live, by: \.host)
    var names: [String] = []
    for n in nodes where !names.contains(n) { names.append(n) }
    names += Set(byHost.keys).union(liveByHost.keys).subtracting(names).sorted()
    return names.map { name in
        Machine(name: name, terminals: byHost[name] ?? [],
                // Conversations outside the hub: their file time is their activity. Ties: the higher id.
                elsewhere: (liveByHost[name] ?? []).sorted { a, b in
                    let x = parseHubTime(a.updated) ?? .distantPast, y = parseHubTime(b.updated) ?? .distantPast
                    return x != y ? x > y : a.id > b.id
                })
    }
}

/// Ended and failed terminals that this app still holds (`keep`), for the one "Ended" group: most recent first.
public func endedSessions(terminals: [(hub: String, terminal: Terminal)], keep: Set<TerminalRef>,
                          inputs: [TerminalRef: Date] = [:]) -> [Machine.Entry] {
    entries(terminals, open: false, keep: keep, inputs: inputs).sorted(by: mostRecentFirst)
}

/// The open (or the kept ended) terminals as rows with their order key.
func entries(_ terminals: [(hub: String, terminal: Terminal)], open: Bool, keep: Set<TerminalRef>,
             inputs: [TerminalRef: Date]) -> [Machine.Entry] {
    terminals.compactMap { hub, t in
        let ref = TerminalRef(hub: hub, id: t.id)
        guard t.isOpen == open, open || keep.contains(ref) else { return nil }
        let key = recentKey(t, input: inputs[ref])
        return Machine.Entry(ref: ref, terminal: t, recent: key, rank: key)
    }
}

/// Nest each session under its parent: (`parent_hub`, `parent_id`) = (`hub`, `id`). A hub without these fields, or
/// a child whose parent is not in the list, gives a flat list. A parent ranks by the most recent row under it;
/// children rank by their own activity. Every level is most recent first.
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
        e.rank = e.recent
        guard let node = e.terminal.hubNode else { return e }
        let kids = (byParent[Key(hub: node, id: e.terminal.id)] ?? []).filter { !placed.contains(row($0)) }
        let built = kids.map(build)
        // Children by their own activity; the parent by the most recent row under it.
        e.children = built.sorted { $0.recent != $1.recent ? $0.recent > $1.recent : $0.terminal.id > $1.terminal.id }
        e.rank = ([e.recent] + built.map(\.rank)).max()!
        return e
    }
    var top = entries.filter { parent($0.terminal) == nil }.map(build)
    // Bad data (a session that is its own ancestor) has no root: show those sessions at the top level, never hide them.
    for e in entries where !placed.contains(row(e)) { top.append(build(e)) }
    return top.sorted(by: mostRecentFirst)
}

/// The quick switcher (⌘K): which open sessions match what was typed. `names` are in sidebar order (most recent
/// first). An empty query gives the first `limit`; names that start with the query come before names that contain it.
/// Returns indices into `names`.
public func switcherMatches(_ names: [String], query: String, limit: Int = 8) -> [Int] {
    let q = query.trimmingCharacters(in: .whitespaces).lowercased()
    guard !q.isEmpty else { return Array(names.indices.prefix(limit)) }
    let lower = names.map { $0.lowercased() }
    let starts = lower.indices.filter { lower[$0].hasPrefix(q) }
    let inside = lower.indices.filter { !lower[$0].hasPrefix(q) && lower[$0].contains(q) }
    return Array((starts + inside).prefix(limit))
}
