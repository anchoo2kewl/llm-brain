import Foundation

// What a sidebar row shows (docs/design.md). The same rules as hub/internal/web/static/wslist.js, with the same tests.

/// A model id as a short name: "claude-opus-5-5" → "Opus 5.5", "gpt-6.1-sol" → "GPT-6.1-Sol",
/// "gemini-4-carbon" → "Gemini 4 Carbon". Date stamps and "[1m]" go. Any other id stays as it is.
public func modelShort(_ id: String?) -> String {
    let raw = (id ?? "").trimmingCharacters(in: .whitespaces)
    var m = raw.lowercased()
    if m.hasSuffix("]"), let open = m.lastIndex(of: "[") { m = String(m[..<open]) }
    let isNum = { (p: Substring) in p.first?.isNumber == true && Double(p) != nil }
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
    let s = max(0, Int(seconds.isFinite ? seconds : 0))
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
        tool = cli?.label ?? t.cli
        let short = modelShort(t.model)
        model = short.isEmpty ? tool : short
        percent = t.contextFraction.map { Int(($0 * 100).rounded()) }
    }
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

    /// The selected row's details: always the same rows, so its height never changes when data arrives. `d` is the
    /// hub's `/api/terminals/{id}/detail`, nil until it comes.
    func detailRows(_ d: TerminalDetail?, now: Date = Date()) -> [DetailRow] {
        let wait = d == nil ? "…" : "–"
        let one = { (s: String?) in (s ?? "").split(whereSeparator: \.isWhitespace).joined(separator: " ") }
        let plural = { (n: Int, w: String) in "\(n) \(w)\(n == 1 ? "" : "s")" }
        let tool = one(d?.tool)
        let since = stateSince.flatMap(parseHubTime)
        let state = [stateLabel, since.map { duration(now.timeIntervalSince($0)) } ?? "",
                     (subagents ?? 0) > 0 ? plural(subagents!, "subagent") : "",
                     tool.split(whereSeparator: { " {(".contains($0) }).first.map(String.init) ?? ""]
            .filter { !$0.isEmpty }.joined(separator: " · ")
        let totals: String = d?.stats.map { s in
            let tok = s.tokens?.values.reduce(0, +) ?? 0
            return [s.dollars > 0 ? String(format: "$%.2f", s.dollars) : "", tok > 0 ? shortCount(tok) + " tokens" : "",
                    plural(s.prompts, "prompt"), plural(s.toolCalls, "tool call") + (s.toolErrors > 0 ? " (\(s.toolErrors) failed)" : "")]
                .filter { !$0.isEmpty }.joined(separator: " · ")
        } ?? wait
        let meta = RowMeta(self)
        let folder = cwd == "~" || cwd.isEmpty ? "~" : (cwd as NSString).lastPathComponent
        let br = branch.flatMap { ["", "HEAD", "main", "master"].contains($0) ? nil : $0 }
        let account = [profile, self.account].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · ")
        let started = self.started.flatMap(parseHubTime)
        func row(_ l: String, _ v: String, _ h: String? = nil) -> DetailRow { DetailRow(label: l, value: v, help: h ?? v) }
        return [
            row("Context", meta.percent.map { "\(shortCount(contextUsed!)) of \(shortCount(contextWindow!)) · \($0)%" } ?? "not reported"),
            row("State", state, tool.isEmpty ? nil : tool),
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
