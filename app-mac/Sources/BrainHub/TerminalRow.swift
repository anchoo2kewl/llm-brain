import BrainHubCore
import SwiftUI

/// One terminal in the sidebar: its state, which CLI it runs, what it asks, and (opened) the detail of its agent.
struct TerminalRow: View {
    let ref: TerminalRef
    let terminal: Terminal
    let number: Int?
    @Binding var expanded: Set<TerminalRef>
    private var model: AppModel { AppModel.shared }

    var body: some View {
        let t = terminal
        let state = model.state(ref), wants = model.wantsYou(ref), open = expanded.contains(ref)
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Button { if open { expanded.remove(ref) } else { expanded.insert(ref) } } label: {
                    Image(systemName: open ? "chevron.down" : "chevron.right").font(.caption2).frame(width: 10)
                }
                .buttonStyle(.plain).foregroundStyle(.tertiary)
                .accessibilityLabel(open ? "Hide detail" : "Show detail")
                StateDot(state: state, wants: wants).help(Self.help(state, t))
                Text(t.title.isEmpty ? t.badge : t.title).lineLimit(1).fontWeight(wants ? .semibold : .regular)
                Spacer(minLength: 0)
                if let n = t.subagents, n > 0 {
                    Label("\(n)", systemImage: "arrow.triangle.branch").font(.caption2).foregroundStyle(.secondary)
                        .help("\(n) subagent\(n == 1 ? "" : "s") running")
                }
                if let number, number < 9 { Text("⌘\(number + 1)").font(.caption2).foregroundStyle(.tertiary) }
            }
            HStack(spacing: 6) {
                Badge(provider: t.provider, text: t.badge)
                Text(t.branch.map { $0.isEmpty || $0 == "HEAD" ? t.cwd : "\(t.cwd) · \($0)" } ?? t.cwd)
                    .font(.caption).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
                Spacer(minLength: 0)
                if let f = t.contextFraction, f >= 0.7 {
                    Text("\(Int(f * 100))%").font(.caption2.monospacedDigit()).foregroundStyle(f >= 0.9 ? .red : .orange)
                        .help("Context used")
                }
            }
            .padding(.leading, 16)
            if state == .waiting || state == .blocked, let ask = t.ask ?? t.fix, !ask.isEmpty {
                Text("\(AppModel.headline(t)): \(ask)").font(.caption).lineLimit(open ? 6 : 2)
                    .foregroundStyle(wants ? AnyShapeStyle(state == .blocked || t.askKind == "error" ? .red : .orange) : AnyShapeStyle(.secondary))
                    .padding(.leading, 16)
            }
            if open { TerminalDetailView(ref: ref, terminal: t).padding(.leading, 16) }
        }
        .padding(.vertical, 2)
        .listRowBackground(wants ? RoundedRectangle(cornerRadius: 6).fill(.orange.opacity(0.13)).padding(.horizontal, 6) : nil)
    }

    static func help(_ s: AgentState, _ t: Terminal) -> String {
        switch s {
        case .working: "Working"
        case .waiting: AppModel.headline(t)
        case .blocked: "Blocked: \(t.fix ?? "its machine shows a prompt")"
        case .idle: "Idle"
        case .ended: "Ended"
        }
    }
}

/// The state as a dot: it pulses while the agent works.
struct StateDot: View {
    let state: AgentState
    let wants: Bool
    @ViewState private var dim = false

    var body: some View {
        Circle().fill(color).frame(width: 8, height: 8)
            .opacity(state == .working && dim ? 0.3 : 1)
            .animation(state == .working ? .easeInOut(duration: 0.9).repeatForever(autoreverses: true) : .default, value: dim)
            .onAppear { dim = true }
    }

    private var color: SwiftUI.Color {
        switch state {
        case .working: .blue
        case .waiting: wants ? .orange : .orange.opacity(0.5)
        case .blocked: .red
        case .idle: .green.opacity(0.6)
        case .ended: .gray
        }
    }
}

/// Which CLI a terminal runs, in its own colour.
struct Badge: View {
    let provider: String
    let text: String

    var body: some View {
        Text(text).font(.caption2.weight(.medium)).padding(.horizontal, 5).padding(.vertical, 1)
            .foregroundStyle(color).background(color.opacity(0.16), in: Capsule())
    }

    private var color: SwiftUI.Color {
        switch provider {
        case "claude": .orange
        case "codex": .teal
        case "gemini": .blue
        default: .gray
        }
    }
}

/// The opened row: context, model, subagents, the running tool, the last prompt and reply, and totals. It asks the
/// hub again every 5 seconds while it shows.
struct TerminalDetailView: View {
    let ref: TerminalRef
    let terminal: Terminal
    @ViewState private var detail: TerminalDetail?

    var body: some View {
        let t = terminal
        VStack(alignment: .leading, spacing: 4) {
            if let f = t.contextFraction, let used = t.contextUsed, let window = t.contextWindow {
                ProgressView(value: f).tint(f >= 0.9 ? .red : f >= 0.7 ? .orange : .accentColor)
                line("Context", "\(shortCount(used)) of \(shortCount(window)) (\(Int(f * 100))%)")
            }
            if let m = t.model, !m.isEmpty { line("Model", m) }
            if let tool = detail?.tool { line("Now", tool) }
            if let agents = detail?.agents, !agents.isEmpty {
                let live = agents.filter(\.live)
                line("Subagents", live.isEmpty ? "\(agents.count), none running" : "\(live.count) running of \(agents.count)")
                ForEach(live, id: \.self) { a in
                    Text("• \(a.title)").font(.caption2).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            if let p = detail?.lastPrompt, !p.isEmpty { line("You", p) }
            if let r = detail?.lastReply, !r.isEmpty { line("Agent", r) }
            if let s = detail?.stats {
                line("Totals", "\(s.prompts) prompts · \(s.toolCalls) tool calls" + (s.toolErrors > 0 ? " (\(s.toolErrors) failed)" : "")
                    + (s.dollars > 0 ? String(format: " · about $%.2f", s.dollars) : ""))
            }
            if let started = t.started.flatMap(Self.ago) { line("Started", started) }
        }
        .font(.caption)
        .task(id: ref) {
            while !Task.isCancelled {
                if let d = try? await HubClient(base: ref.hub).get(Paths.detail(ref.id), as: TerminalDetail.self) { detail = d }
                try? await Task.sleep(for: .seconds(5))
            }
        }
    }

    private func line(_ name: String, _ value: String) -> some View {
        (Text(name + "  ").foregroundStyle(.tertiary) + Text(value).foregroundStyle(.secondary)).lineLimit(3)
    }

    /// "3 hours ago" from the hub's UTC time.
    static func ago(_ s: String) -> String? {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        guard let d = f.date(from: s) else { return nil }
        return RelativeDateTimeFormatter().localizedString(for: d, relativeTo: Date())
    }
}
