import Foundation

/// Which address of each saved hub the app talks to: of the addresses that answer as the same hub (the same
/// node name), the fastest. A hub is named by its first address everywhere; `resolve` turns that name, or any
/// address of the hub (a peer's advertised one too), into the address in use.
public final class HubRoutes: @unchecked Sendable {
    public static let shared = HubRoutes()

    private let lock = NSLock()
    private var hubs: [String: [String]] = [:]   // first address -> every address
    private var owner: [String: String] = [:]    // any address -> first address
    private var active: [String: String] = [:]   // first address -> the one in use
    private var node: [String: String] = [:]     // first address -> the node it answered as
    private var probing: Set<String> = []

    public init() {}

    public func configure(_ saved: [HubConfig.Hub]) {
        lock.withLock {
            hubs = Dictionary(saved.map { ($0.url, $0.all) }, uniquingKeysWith: { a, _ in a })
            owner = [:]
            for (key, all) in hubs { for a in all { owner[a] = key } }
            active = active.filter { hubs[$0.key]?.contains($0.value) == true }
        }
    }

    /// Another address of a saved hub, learnt from the cluster (a peer's advertised URL).
    public func alias(_ address: String, toNode name: String) {
        lock.withLock {
            guard owner[address] == nil, let key = node.first(where: { $0.value == name })?.key else { return }
            owner[address] = key
        }
    }

    /// The address to use for a hub named by any of its addresses.
    public func resolve(_ base: String) -> String {
        lock.withLock {
            guard let key = owner[base] else { return base }
            return active[key] ?? key
        }
    }

    /// The hub's addresses, the one in use first: for brain-attach, which dials the fastest itself.
    public func addresses(_ base: String) -> [String] {
        lock.withLock {
            guard let key = owner[base], let all = hubs[key] else { return [base] }
            let now = active[key] ?? key
            return [now] + all.filter { $0 != now }
        }
    }

    /// The fastest answer from the expected node. With no node known yet, the first address's answer decides,
    /// else the fastest's: an address that answers as another node is never used.
    public static func pick(_ answers: [(address: String, node: String, seconds: Double)], first: String,
                            expected: String?) -> (address: String, node: String)? {
        let want = expected ?? answers.first { $0.address == first }?.node ?? answers.min { $0.seconds < $1.seconds }?.node
        guard let best = answers.filter({ $0.node == want }).min(by: { $0.seconds < $1.seconds }) else { return nil }
        return (best.address, best.node)
    }

    /// Ask every address of a hub at once and switch to the fastest. Probes of one hub don't overlap.
    public func probe(_ base: String, timeout: TimeInterval = 3) async {
        let (key, all, expected): (String?, [String], String?) = lock.withLock {
            guard let key = owner[base], let all = hubs[key], all.count > 1, !probing.contains(key) else { return (nil, [], nil) }
            probing.insert(key)
            return (key, all, node[key])
        }
        guard let key else { return }
        defer { _ = lock.withLock { probing.remove(key) } }
        let answers = await withTaskGroup(of: (address: String, node: String, seconds: Double)?.self) { g in
            for a in all { g.addTask { await Self.ask(a, timeout: timeout) } }
            var out: [(address: String, node: String, seconds: Double)] = []
            for await r in g { if let r { out.append(r) } }
            return out
        }
        guard let best = Self.pick(answers, first: key, expected: expected) else { return }
        lock.withLock { (active[key], node[key]) = (best.address, best.node) }
    }

    /// Probe every hub that has more than one address.
    public func probeAll() async {
        let keys = lock.withLock { hubs.filter { $0.value.count > 1 }.map(\.key) }
        await withTaskGroup(of: Void.self) { g in for k in keys { g.addTask { await self.probe(k) } } }
    }

    /// Learn the node name of a hub with one address, so its peers' advertised URLs map back to it.
    public func learn(_ base: String, node name: String) {
        lock.withLock { if let key = owner[base], node[key] == nil { node[key] = name } }
    }

    private static func ask(_ address: String, timeout: TimeInterval) async -> (address: String, node: String, seconds: Double)? {
        guard let url = try? HubURL.join(address, Paths.nodes) else { return nil }
        var req = URLRequest(url: url, timeoutInterval: timeout)
        req.setValue("application/json", forHTTPHeaderField: "Accept")
        let start = Date()
        guard let (data, resp) = try? await HubClient.session.data(for: req),
              (resp as? HTTPURLResponse)?.statusCode == 200,
              let c = try? JSONDecoder().decode(ClusterNodes.self, from: data) else { return nil }
        return (address, c.me.node, Date().timeIntervalSince(start))
    }
}
