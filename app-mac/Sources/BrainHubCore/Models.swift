import Foundation

// The hub's JSON, field for field (hub/internal/web: terminals.go, find.go, inbox.go, cluster).

/// One terminal from `GET /api/terminals`. `status` is starting | live | ended | failed.
public struct Terminal: Codable, Hashable, Sendable, Identifiable {
    public var id: Int64
    public var title: String
    public var host: String
    public var cli: String
    public var provider: String
    public var cwd: String
    public var status: String
    public var error: String?
    public var updated: String

    public init(id: Int64, title: String, host: String, cli: String, provider: String, cwd: String,
                status: String, error: String? = nil, updated: String) {
        (self.id, self.title, self.host, self.cli, self.provider, self.cwd, self.status, self.error, self.updated) =
            (id, title, host, cli, provider, cwd, status, error, updated)
    }

    public var isOpen: Bool { status == "live" || status == "starting" }
}

/// A Claude Code or Codex conversation running outside the hub's terminals (`GET /api/live-sessions`).
public struct LiveSession: Codable, Hashable, Sendable, Identifiable {
    public var id: Int64
    public var title: String
    public var host: String
    public var cli: String
    public var cwd: String
    public var updated: String
    public var messages: Int
}

/// `GET /terminals/{id}/status`.
public struct TerminalStatus: Codable, Equatable, Sendable {
    public var status: String
    public var error: String?
    public var cwd: String?
    public var hostLive: Bool?

    enum CodingKeys: String, CodingKey { case status, error, cwd, hostLive = "host_live" }

    public init(status: String, error: String? = nil, cwd: String? = nil, hostLive: Bool? = nil) {
        (self.status, self.error, self.cwd, self.hostLive) = (status, error, cwd, hostLive)
    }
}

/// `GET /api/search`.
public struct SearchReply: Codable, Sendable {
    public var tookMs: Double
    public var total: Int
    public var partial: [String]
    public var sessions: [SessionHit]

    enum CodingKeys: String, CodingKey { case tookMs = "took_ms", total, partial, sessions }

    public init(from d: Decoder) throws {
        let c = try d.container(keyedBy: CodingKeys.self)
        tookMs = try c.decodeIfPresent(Double.self, forKey: .tookMs) ?? 0
        total = try c.decodeIfPresent(Int.self, forKey: .total) ?? 0
        partial = try c.decodeIfPresent([String].self, forKey: .partial) ?? []
        sessions = try c.decodeIfPresent([SessionHit].self, forKey: .sessions) ?? []
    }
}

public struct SessionHit: Codable, Hashable, Sendable, Identifiable {
    public var id: Int64
    public var node: String
    public var host: String
    public var provider: String
    public var title: String
    public var cwd: String
    public var branch: String
    public var lastAt: String
    public var snippet: String
    /// `/api/sessions/{id}/continue` on this hub, or `/api/cluster/continue?...` for a peer's session.
    public var continueURL: String

    enum CodingKeys: String, CodingKey {
        case id, node, host, provider, title, cwd, branch, snippet
        case lastAt = "last_at", continueURL = "continue_url"
    }
}

/// `GET /api/cluster/nodes`.
public struct ClusterNodes: Codable, Sendable {
    public struct Info: Codable, Hashable, Sendable {
        public var node: String
        public var role: String?
        public var cluster: String?
        public var version: String?
    }
    public struct Peer: Codable, Hashable, Sendable {
        public var url: String
        public var info: Info
        public var ok: Bool
        public var error: String?
    }
    /// This hub (`self` in the JSON).
    public var me: Info
    public var peers: [Peer]
    enum CodingKeys: String, CodingKey { case me = "self", peers }

    public init(me: Info, peers: [Peer]) { (self.me, self.peers) = (me, peers) }

    /// Machine names: this hub's node first, then its peers.
    public var names: [String] { [me.node] + peers.map(\.info.node) }
}

/// `GET /api/inbox`. The hub encodes this struct without JSON tags, so the keys are capitalised.
public struct Inbox: Decodable, Sendable {
    public struct Item: Decodable, Sendable {
        public var key: String
        public var state: String
        public var kind: String
        public var title: String
        public var termID: Int64?
        enum CodingKeys: String, CodingKey { case key = "Key", state = "State", kind = "Kind", title = "Title", termID = "TermID" }
    }
    public var items: [Item]
    enum CodingKeys: String, CodingKey { case items = "Items" }

    public init(from d: Decoder) throws {
        items = try d.container(keyedBy: CodingKeys.self).decodeIfPresent([Item].self, forKey: .items) ?? []
    }

    /// Terminals whose agent finished its turn and waits for you.
    public var waitingTerminals: Set<Int64> {
        Set(items.filter { $0.kind == "terminal" && $0.state == "waiting" }.compactMap { $0.termID.flatMap { $0 > 0 ? $0 : nil } })
    }
}

/// The reply to `POST /api/sessions/{id}/continue` and `/move`.
public struct OpenedTerminal: Decodable, Sendable {
    public var terminalID: Int64?
    public var link: String?
    enum CodingKeys: String, CodingKey { case terminalID = "terminal_id", link }
}

/// A terminal on a given hub. Terminal ids are per hub.
public struct TerminalRef: Codable, Hashable, Sendable {
    public var hub: String
    public var id: Int64
    public init(hub: String, id: Int64) { (self.hub, self.id) = (hub, id) }
}

/// Where a continue or move reply's terminal lives. A local reply carries `terminal_id`; a peer's session continued
/// through this hub carries a link `/cluster/open?node=<node>&path=/terminals/<id>` instead.
public func openedTerminalRef(_ reply: OpenedTerminal, hub: String, nodes: ClusterNodes?) -> TerminalRef? {
    if let link = reply.link, link.hasPrefix("/cluster/open?"),
       let q = URLComponents(string: link)?.queryItems,
       let node = q.first(where: { $0.name == "node" })?.value,
       let path = q.first(where: { $0.name == "path" })?.value,
       path.hasPrefix("/terminals/"), let id = Int64(path.dropFirst("/terminals/".count)),
       let peer = nodes?.peers.first(where: { $0.info.node == node }),
       let base = try? HubURL.validate(peer.url) {
        return TerminalRef(hub: base, id: id)
    }
    if let id = reply.terminalID, id > 0 { return TerminalRef(hub: hub, id: id) }
    return nil
}

/// Request paths.
public enum Paths {
    public static let terminals = "/api/terminals"
    public static let liveSessions = "/api/live-sessions"
    public static let nodes = "/api/cluster/nodes"
    public static let inbox = "/api/inbox"
    public static let newTerminal = "/terminals/new"
    public static func status(_ id: Int64) -> String { "/terminals/\(id)/status" }
    public static func rename(_ id: Int64) -> String { "/terminals/\(id)/rename" }
    public static func close(_ id: Int64) -> String { "/api/terminals/\(id)/close" }
    public static func move(_ id: Int64) -> String { "/api/sessions/\(id)/move" }
    public static func continueSession(_ id: Int64) -> String { "/api/sessions/\(id)/continue" }
    /// Put a file on a machine, in the hub's uploads folder for this terminal (`t<id>`, as the web terminal does).
    public static func upload(host: String, terminal id: Int64) -> String {
        "/hosts/\(host.addingPercentEncoding(withAllowedCharacters: .alphanumerics.union(CharacterSet(charactersIn: "-._"))) ?? host)/upload?into=t\(id)"
    }
    public static func search(_ q: String) -> String {
        "/api/search?" + formEncode([("q", q), ("scope", "cluster"), ("limit", "20")])
    }
}

/// The text pasted after an upload: the paths on that machine, a path with a space in single quotes, then a space.
/// Claude Code and Codex attach an image when its path is pasted.
public func pastedPaths(_ paths: [String]) -> String {
    paths.map { $0.contains(where: \.isWhitespace) ? shellQuote($0) : $0 }.joined(separator: " ") + " "
}

/// The CLIs a new terminal can run (`cli` form field of `POST /terminals/new`).
public enum CLI: String, CaseIterable, Identifiable, Sendable {
    case claude, codex, gemini, shell
    public var id: String { rawValue }
    public var label: String {
        switch self {
        case .claude: "Claude Code"
        case .codex: "Codex"
        case .gemini: "Gemini"
        case .shell: "Shell"
        }
    }
}
