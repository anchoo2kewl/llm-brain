import Foundation

// The hub's JSON, field for field (hub/internal/web: terminals.go, find.go, inbox.go, cluster).

/// What a terminal's agent does now, as the hub decides it (hub/internal/web/termstate.go).
public enum AgentState: String, Codable, Sendable { case working, waiting, blocked, idle, ended }

/// One terminal from `GET /api/terminals`. `status` is starting | live | ended | failed. The fields after `updated`
/// come from a hub that decides the agent's state; an older hub sends none of them.
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
    public var state: AgentState?
    /// What the agent asks or last said, in one line, and its kind: question | done | error.
    public var ask: String?
    public var askKind: String?
    public var fix: String?
    public var model: String?
    public var branch: String?
    /// Tokens in the newest request, and the model's limit.
    public var contextUsed: Int?
    public var contextWindow: Int?
    /// Subagents that run now.
    public var subagents: Int?
    public var started: String?
    /// Since when the agent is in its current state.
    public var stateSince: String?
    /// The agent's last output and the owner's last input (hub 0.1.12); an older hub sends only `updated`.
    public var lastActivity: String?
    public var lastInput: String?
    /// The session tree (hub 0.1.12): the node of the hub that owns this row, and its parent session's hub node and
    /// terminal id there. An id alone is not unique across hubs. `childCount` is the parent's live children.
    public var hubNode: String?
    public var parentHub: String?
    public var parentId: Int64?
    public var childCount: Int?
    /// Set by the hub only when the session waits on you: a question or a permission prompt.
    public var needsOwner: Bool?
    /// The login profile it runs with (nil = the machine's default login), and who that login is.
    public var profile: String?
    public var account: String?

    enum CodingKeys: String, CodingKey {
        case id, title, host, cli, provider, cwd, status, error, updated, state, ask, askKind = "ask_kind", fix, model, branch
        case contextUsed = "context_used", contextWindow = "context_window", subagents, started, profile, account
        case stateSince = "state_since"
        case lastActivity = "last_activity_at", lastInput = "last_input_at"
        case hubNode = "hub", parentHub = "parent_hub", parentId = "parent_id", childCount = "children", needsOwner = "needs_owner"
    }

    public init(id: Int64, title: String, host: String, cli: String, provider: String, cwd: String,
                status: String, error: String? = nil, updated: String, state: AgentState? = nil, ask: String? = nil,
                askKind: String? = nil) {
        (self.id, self.title, self.host, self.cli, self.provider, self.cwd, self.status, self.error, self.updated) =
            (id, title, host, cli, provider, cwd, status, error, updated)
        (self.state, self.ask, self.askKind) = (state, ask, askKind)
    }

    public var isOpen: Bool { status == "live" || status == "starting" }

    /// The CLI's short name for a badge: the hub's raw `provider`, never its long label.
    public var badge: String { CLI(rawValue: provider)?.label ?? cli }

    /// How full the context is, 0…1; nil when the hub does not know.
    public var contextFraction: Double? {
        guard let used = contextUsed, let window = contextWindow, used > 0, window > 0 else { return nil }
        return min(1, Double(used) / Double(window))
    }

    /// The agent needs an answer or hit an error: it stays marked until that changes. A finished turn is marked
    /// only until you look at it.
    public var needsAnswer: Bool { state == .blocked || (state == .waiting && askKind != "done") }

    /// When it last ran or got input: the later of the two, else `updated`.
    public var lastActive: Date? {
        [lastActivity, lastInput].compactMap { $0.flatMap(parseHubTime) }.max() ?? parseHubTime(updated)
    }
}

/// The session list's recency and "needs you" signals (docs/design.md). They never change the order.
public enum Recency {
    /// "now", "2m", "1h", "3d".
    public static func short(_ date: Date?, now: Date = Date()) -> String {
        guard let date else { return "" }
        let s = max(0, Int(now.timeIntervalSince(date)))
        switch s {
        case ..<60: return "now"
        case ..<3600: return "\(s / 60)m"
        case ..<86400: return "\(s / 3600)h"
        default: return "\(s / 86400)d"
        }
    }

    /// The status dot fades with age: bright when active now, dimmer as it gets older.
    public static func dotOpacity(_ date: Date?, now: Date = Date()) -> Double {
        guard let date else { return 0.4 }
        switch now.timeIntervalSince(date) {
        case ..<60: return 1
        case ..<600: return 0.85
        case ..<3600: return 0.7
        case ..<86400: return 0.55
        default: return 0.4
        }
    }

    /// The dot pulses red only while the agent waits for the owner (a question or a permission prompt) and the owner
    /// has not opened it yet. The hub's `needs_owner` decides; an older hub: blocked, or waiting on a question.
    /// A finished turn or an error is a report: it never pulses.
    public static func pulses(_ t: Terminal, opened: Bool) -> Bool {
        !opened && (t.needsOwner ?? (t.state == .blocked || (t.state == .waiting && t.askKind == "question")))
    }
}

/// `GET /api/terminals/{id}/detail`: what an expanded row shows.
public struct TerminalDetail: Decodable, Sendable {
    public struct Agent: Decodable, Sendable, Hashable {
        public var title: String
        public var updated: String
        public var messages: Int
        public var live: Bool
    }
    public struct Stats: Decodable, Sendable {
        public var dollars: Double
        public var toolCalls: Int
        public var toolErrors: Int
        public var prompts: Int
        /// input, output, cache_read and cache_write.
        public var tokens: [String: Int]?
        enum CodingKeys: String, CodingKey { case dollars, toolCalls = "tool_calls", toolErrors = "tool_errors", prompts, tokens }
    }
    public var lastPrompt: String?
    public var lastReply: String?
    public var tool: String?
    public var agents: [Agent]?
    public var stats: Stats?
    enum CodingKeys: String, CodingKey { case lastPrompt = "last_prompt", lastReply = "last_reply", tool, agents, stats }
}

/// 63499 → "63k", 1200000 → "1.2M".
public func shortCount(_ n: Int) -> String {
    n >= 1_000_000 ? String(format: "%.1fM", Double(n) / 1_000_000) : n >= 1000 ? "\(n / 1000)k" : "\(n)"
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

extension TerminalRef {
    /// A sidebar row dragged onto a machine: "<hub>#<id>".
    public var dragText: String { "\(hub)#\(id)" }
    public init?(dragText: String) {
        guard let i = dragText.lastIndex(of: "#"), let id = Int64(dragText[dragText.index(after: i)...]), id > 0 else { return nil }
        self.init(hub: String(dragText[..<i]), id: id)
    }
}

/// `GET /api/migrations/{job}`: a terminal's conversation moving to another machine, phase by phase. The hub runs
/// it; the source terminal ends only after the copy is live there.
public struct Migration: Decodable, Sendable, Equatable {
    public struct Phase: Decodable, Sendable, Equatable {
        public var name: String
        public var state: String // pending | running | done | failed | skipped
        public var detail: String?
    }
    public var id: String
    public var to: String
    /// move | clone (a clone forks the conversation there and the source keeps running); older hubs send none.
    public var mode: String?
    public var phases: [Phase]
    public var dest: Int64?
    public var done: Bool
    public var error: String?
    enum CodingKeys: String, CodingKey { case id, to, mode, phases, dest = "dest_terminal", done, error }
    public var isClone: Bool { mode == "clone" }

    /// The phase to show: the one running or failed, else the last one done.
    public var current: Phase? {
        phases.first { $0.state == "running" || $0.state == "failed" } ?? phases.last { $0.state == "done" }
    }

    public static func label(_ phase: String, to: String) -> String {
        switch phase {
        case "check": "Checking readiness"
        case "export": "Exporting the conversation"
        case "copy": "Copying it to \(to)"
        case "resume": "Resuming on \(to)"
        case "verify": "Checking it is live"
        case "close": "Ending the source"
        default: phase
        }
    }
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
    public static let profiles = "/api/profiles"
    public static func status(_ id: Int64) -> String { "/terminals/\(id)/status" }
    public static func rename(_ id: Int64) -> String { "/terminals/\(id)/rename" }
    public static func close(_ id: Int64) -> String { "/api/terminals/\(id)/close" }
    public static func detail(_ id: Int64) -> String { "/api/terminals/\(id)/detail" }
    public static func send(_ id: Int64) -> String { "/api/terminals/\(id)/send" }
    public static func move(_ id: Int64) -> String { "/api/sessions/\(id)/move" }
    public static func continueSession(_ id: Int64) -> String { "/api/sessions/\(id)/continue" }
    public static func migrate(_ id: Int64) -> String { "/api/terminals/\(id)/migrate" }
    public static func workspace(since: Int64) -> String { "/api/workspace?since=\(since)" }
    public static let workspaceState = "/api/workspace/state"
    public static let memoryStatus = "/api/memory/status"
    public static let memoryKey = "/api/memory/key"
    public static func migration(_ job: String) -> String {
        "/api/migrations/" + (job.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? "")
    }
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

/// One set of CLI logins on a machine (`GET /api/profiles`). The empty name is the machine's default login.
public struct Profile: Decodable, Hashable, Sendable {
    public var name: String
    public var claude: String?
    public var codex: String?

    public init(name: String, claude: String? = nil, codex: String? = nil) {
        (self.name, self.claude, self.codex) = (name, claude, codex)
    }

    /// The profile in an account picker, with who is logged in for that CLI.
    public func label(for cli: CLI) -> String {
        let who: String? = switch cli {
        case .claude: claude
        case .codex: codex
        default: nil
        }
        let title = name.isEmpty ? "Default" : name
        guard cli == .claude || cli == .codex else { return title }
        return title + " · " + (who.flatMap { $0.isEmpty ? nil : $0 } ?? "not logged in")
    }
}

/// The CLIs a new terminal can run (`cli` form field of `POST /terminals/new`).
public enum CLI: String, CaseIterable, Identifiable, Sendable {
    case claude, codex, antigravity, shell
    public var id: String { rawValue }
    public var label: String {
        switch self {
        case .claude: "Claude Code"
        case .codex: "Codex"
        case .antigravity: "Antigravity"
        case .shell: "Shell"
        }
    }
}

/// `GET /api/workspace`: what agents (the Brain MCP) asked the open workspaces to show, after `since`. Terminal ids
/// are that hub's own.
public struct WorkspaceUpdate: Decodable, Sendable, Equatable {
    public struct Intent: Decodable, Sendable, Equatable {
        public var seq: Int64
        public var action: String // open | focus | layout | zoom | unzoom
        public var terminal: Int64?
        public var layout: String?
        public var client: String?
    }
    public var seq: Int64
    public var intents: [Intent]?

    /// The requests for every client or for `kind` ("mac" takes "mac:2" too), in order. The first answer
    /// (`last` < 0) only marks where to start, so old requests don't replay.
    public func pending(after last: Int64, kind: String) -> [Intent] {
        guard last >= 0 else { return [] }
        return (intents ?? []).filter { i in
            let c = i.client ?? ""
            return i.seq > last && (c.isEmpty || c == kind || c.hasPrefix(kind + ":"))
        }
    }
}
