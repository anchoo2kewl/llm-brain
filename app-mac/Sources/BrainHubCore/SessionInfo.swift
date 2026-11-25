import Foundation

// What a sidebar row shows (docs/design.md). The same rules as hub/internal/web/static/wslist.js, with the same tests.

/// A model id as a short name: "claude-opus-5-5" → "Opus 5.5", "gpt-6.1-sol" → "GPT-6.1-Sol",
/// "gemini-4-carbon" → "Gemini 4 Carbon". Date stamps and "[1m]" go. Any other id stays as it is.
public func modelShort(_ id: String?) -> String {
    let raw = (id ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    var m = raw.lowercased()
    if m.hasSuffix("]"), let open = m.lastIndex(of: "[") { m = String(m[..<open]) }
    let isNum = { (p: Substring) in p.range(of: #"^\d+(\.\d+)?$"#, options: .regularExpression) != nil }
    let parts: [Substring] = m.split(separator: "-").filter { (p: Substring) in !(p.count == 8 && p.allSatisfy(\.isNumber)) }
    let cap = { (p: Substring) in p.first?.isLetter == true ? p.prefix(1).uppercased() + p.dropFirst() : String(p) }
    guard parts.count > 1 else { return raw }
    switch parts[0] {
    case "claude":
        let rest = parts.dropFirst(), nums = rest.filter(isNum)
        let words = rest.filter { !isNum($0) }.map(cap)
        return (words.isEmpty ? "Claude" : words.joined(separator: " ")) + (nums.isEmpty ? "" : " " + nums.joined(separator: "."))
    case "gpt": return "GPT-" + parts.dropFirst().map(cap).joined(separator: "-")
    case "gemini": return parts.map(cap).joined(separator: " ")
    default: return raw
    }
}

/// A length of time: "45s", "3m", "2h 5m", "3d 4h".
public func duration(_ seconds: TimeInterval) -> String {
    let s = seconds.isFinite && seconds > 0 ? Int(min(seconds, 1e15)) : 0 // Int() traps outside its range
    switch s {
    case ..<60: return "\(s)s"
    case ..<3600: return "\(s / 60)m"
    case ..<86400: return "\(s / 3600)h" + (s % 3600 / 60 > 0 ? " \(s % 3600 / 60)m" : "")
    default: return "\(s / 86400)d" + (s % 86400 / 3600 > 0 ? " \(s % 86400 / 3600)h" : "")
    }
}

/// The second line of a row: the tool's mark, the model (else the tool's name) and the context used.
public struct RowMeta: Equatable, Sendable {
    public var mark: String
    public var tool: String
    public var model: String
    public var percent: Int?

    public init(_ t: Terminal) {
        let name = (t.provider.isEmpty ? t.cli : t.provider).lowercased()
        let cli = name == "claude code" ? .claude : CLI(rawValue: name)
        mark = switch cli { case .claude: "CC"; case .codex: "CX"; case .antigravity: "AG"; default: ">_" }
        tool = cli?.label ?? (t.cli.isEmpty ? t.provider : t.cli)
        let short = modelShort(t.model)
        model = short.isEmpty ? tool : short
        percent = t.contextFraction.map { Int(($0 * 100).rounded()) }
    }
}

/// Context colours, in one place: normal below 75 %, warn from 75 %, high from 85 %. Handoff is offered from 80 %.
public enum ContextLevel: String, Sendable {
    case normal, warn, high
    public static let handoffPercent = 80
    public init(percent: Int) { self = percent >= 85 ? .high : percent >= 75 ? .warn : .normal }
}

/// A parent's subtree in numbers, all depths: what a closed parent shows on its second line.
public struct Rollup: Equatable, Sendable {
    public var needs = 0, working = 0, waiting = 0, total = 0
    /// The child to open from the rollup: the longest wait.
    public var first: TerminalRef?
    public init(_ e: Machine.Entry) {
        let ds = Array(e.all.dropFirst())
        total = ds.count
        for c in ds {
            if Recency.pulses(c.terminal, opened: false) { needs += 1 } else if c.terminal.state == .working { working += 1 }
            else if c.terminal.state == .waiting { waiting += 1 }
        }
        first = needsQueue(ds).first
    }
    public var label: String {
        [needs > 0 ? "\(needs) need\(needs == 1 ? "s" : "") you" : "", working > 0 ? "\(working) working" : ""]
            .filter { !$0.isEmpty }.joined(separator: " · ")
    }
}

/// The sessions that wait for the owner, the longest wait first; ties by the lower id. One continuous period counts once.
public func needsQueue(_ entries: [Machine.Entry]) -> [TerminalRef] {
    func since(_ t: Terminal) -> Date { t.needsOwnerSince.flatMap(parseHubTime) ?? t.stateSince.flatMap(parseHubTime) ?? recentKey(t) }
    return entries.filter { $0.terminal.isOpen && Recency.pulses($0.terminal, opened: false) }
        .sorted { since($0.terminal) != since($1.terminal) ? since($0.terminal) < since($1.terminal)
            : $0.terminal.id != $1.terminal.id ? $0.terminal.id < $1.terminal.id : $0.ref.hub < $1.ref.hub }
        .map(\.ref)
}

/// Needs me: the longest wait first; from a session in the queue, the next one (after an answer it leaves the queue).
public func nextNeeds(_ queue: [TerminalRef], current: TerminalRef?) -> TerminalRef? {
    guard !queue.isEmpty else { return nil }
    guard let current, let i = queue.firstIndex(of: current) else { return queue[0] }
    return queue[(i + 1) % queue.count]
}

/// One line of the selected row's details.
public struct DetailRow: Equatable, Sendable {
    public var label: String
    public var value: String
    /// The whole value, for the tooltip.
    public var help: String
}

public extension Terminal {
    /// What the agent does, in words.
    var stateLabel: String {
        if status == "ended" || status == "failed" { return "Ended" }
        if Recency.pulses(self, opened: false) { return "Waiting for you" }
        switch state {
        case .working: return "Working"
        case .waiting: return askKind == "done" ? "Finished its turn" : askKind == "error" ? "Stopped on an error" : "Waiting"
        case .blocked: return "Blocked"
        case .idle: return "Idle"
        case .ended: return "Ended"
        case nil: return "Unknown"
        }
    }

    /// The hub answers POST /api/terminals/{id}/handoff: its own flag, else a hub that sends the 0.1.15 fields.
    var handoffOffered: Bool {
        guard isOpen, let p = RowMeta(self).percent, p >= ContextLevel.handoffPercent else { return false }
        return handoff ?? (needsOwnerSince != nil || git != nil)
    }

    /// The open row's one-line status: "Working 9m · 5 files (+112/−19)", "Waiting on 3 children",
    /// "Idle 14m · 2 commits ahead". The git part needs the hub's `git`; without it the line shows what it has.
    func statusLine(_ kids: Rollup? = nil, now: Date = Date()) -> String {
        let pulse = Recency.pulses(self, opened: false)
        if let k = kids, k.working > 0, isOpen, askKind != "error", !pulse, state == .waiting || state == .idle {
            return "Waiting on \(k.working) \(k.working == 1 ? "child" : "children")"
        }
        let dur = stateSince.flatMap(parseHubTime).map { " " + duration(now.timeIntervalSince($0)) } ?? ""
        let plural = { (n: Int, w: String) in "\(n) \(w)\(n == 1 ? "" : "s")" }
        let files = (git?.filesChanged ?? 0) > 0 ? plural(git!.filesChanged!, "file") + " (+\(git?.insertions ?? 0)/−\(git?.deletions ?? 0))" : ""
        let ahead = (git?.ahead ?? 0) > 0 ? plural(git!.ahead!, "commit") + " ahead" : ""
        let extra = state == .working ? files : (ahead.isEmpty ? files : ahead)
        return stateLabel + dur + (extra.isEmpty ? "" : " · " + extra)
    }

    /// The selected row's details: always the same rows, so its height never changes when data arrives. `d` is the
    /// hub's `/api/terminals/{id}/detail`, nil until it comes.
    func detailRows(_ d: TerminalDetail?, now: Date = Date(), kids: Rollup? = nil) -> [DetailRow] {
        let wait = d == nil ? "…" : "–"
        let one = { (s: String?) in (s ?? "").split(whereSeparator: \.isWhitespace).joined(separator: " ") }
        let plural = { (n: Int, w: String) in "\(n) \(w)\(n == 1 ? "" : "s")" }
        let tool = one(d?.tool)
        let state = [statusLine(kids, now: now),
                     (subagents ?? 0) > 0 ? plural(subagents!, "subagent") : "",
                     tool.split(whereSeparator: { " {(".contains($0) }).first.map(String.init) ?? ""]
            .filter { !$0.isEmpty }.joined(separator: " · ")
        let totals: String = d?.stats.map { s in
            let tok = s.tokens?.values.reduce(0, +) ?? 0
            return [s.dollars >= 0.005 ? String(format: "$%.2f", s.dollars) : "", tok > 0 ? shortCount(tok) + " tokens" : "",
                    plural(s.prompts, "prompt"), plural(s.toolCalls, "tool call") + (s.toolErrors > 0 ? " (\(s.toolErrors) failed)" : "")]
                .filter { !$0.isEmpty }.joined(separator: " · ")
        } ?? wait
        let meta = RowMeta(self)
        let folder = cwd == "~" || cwd.isEmpty ? "~" : (cwd as NSString).lastPathComponent
        let br = branch.flatMap { ["", "HEAD", "main", "master"].contains($0) ? nil : $0 }
        let account = [profile, self.account].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · ")
        let started = self.started.flatMap(parseHubTime)
        func row(_ l: String, _ v: String, _ h: String? = nil) -> DetailRow { DetailRow(label: l, value: v, help: h.flatMap { $0.isEmpty ? nil : $0 } ?? v) }
        return [
            row("Now", state, tool.isEmpty ? nil : tool),
            row("Context", meta.percent.map { "\(shortCount(contextUsed!)) of \(shortCount(contextWindow!)) · \($0)%" } ?? "not reported"),
            row("Where", [host, folder, br ?? ""].filter { !$0.isEmpty }.joined(separator: " · "),
                [host, cwd, branch ?? ""].filter { !$0.isEmpty }.joined(separator: " · ")),
            row("Account", account.isEmpty ? "default login" : account),
            row("Totals", totals),
            row("You", d.map { one($0.lastPrompt).isEmpty ? "–" : one($0.lastPrompt) } ?? wait),
            row("Agent", d.map { one($0.lastReply).isEmpty ? "–" : one($0.lastReply) } ?? wait),
            row("Started", started.map { duration(now.timeIntervalSince($0)) + " ago" } ?? "–", self.started),
        ]
    }
}
