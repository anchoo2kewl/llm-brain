import BrainHubCore
import SwiftUI

/// One terminal in the sidebar: its state, which CLI it runs, what it asks, and (opened) the detail of its agent.
struct TerminalRow: View {
    let ref: TerminalRef
    let terminal: Terminal
    let number: Int?
    /// The one row the window shows. Only it gets the accent bar and the bold title.
    var selected = false
    @Binding var expanded: Set<TerminalRef>
    /// Where the row sits in the session tree.
    struct Tree {
        var depth = 0
        var children = 0
        var open = false
        var hub: String?
        var toggle: (() -> Void)?
    }
    var tree = Tree()
    /// One click on × ends the terminal (its CLI stops on its machine).
    var end: (() -> Void)?
    @ViewState private var hovering = false
    private var model: AppModel { AppModel.shared }

    var body: some View {
        let t = terminal
        let state = model.state(ref), wants = model.wantsYou(ref), open = expanded.contains(ref)
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline, spacing: 6) {
                Button { if open { expanded.remove(ref) } else { expanded.insert(ref) } } label: {
                    Image(systemName: open ? "chevron.down" : "chevron.right").font(.appCaption2).frame(width: 10)
                }
                .buttonStyle(.plain).foregroundStyle(.tertiary)
                .accessibilityLabel(open ? "Hide detail" : "Show detail")
                StateDot(state: state, wants: wants, pulse: model.pulses(ref), age: t.lastActive).help(Self.help(state, t))
                // Attention is the dot and the title colour, never a row background: only the selection looks selected.
                Text(Self.name(t)).lineLimit(1).fontWeight(selected ? .bold : .regular)
                    .foregroundStyle(wants ? AnyShapeStyle(SessionStyle.attention) : AnyShapeStyle(.primary))
                if let hub = tree.hub {
                    Text(hub).font(.appCaption2).foregroundStyle(.secondary).help("This child session runs on the hub \(hub)")
                }
                if tree.children > 0 {
                    Button { tree.toggle?() } label: {
                        Label("\(tree.children)", systemImage: tree.open ? "chevron.down" : "chevron.right")
                            .font(.appCaption2).labelStyle(.titleAndIcon)
                    }
                    .buttonStyle(.plain).foregroundStyle(.secondary)
                    .help(tree.open ? "Hide the child sessions (←)" : "Show the \(tree.children) child session\(tree.children == 1 ? "" : "s") (→)")
                    .accessibilityLabel("\(tree.children) child sessions, \(tree.open ? "shown" : "hidden")")
                }
                Spacer(minLength: 0)
                if let n = t.subagents, n > 0 {
                    Label("\(n)", systemImage: "arrow.triangle.branch").font(.appCaption2).foregroundStyle(.secondary)
                        .help("\(n) subagent\(n == 1 ? "" : "s") running")
                }
                // How long ago it ran or got input. It never moves the row.
                TimelineView(.periodic(from: .now, by: 30)) { ctx in
                    Text(Recency.short(t.lastActive, now: ctx.date)).font(.appCaption2.monospacedDigit()).foregroundStyle(.secondary)
                }
                .help("Last activity or input")
                if let number, number < 9 { Text("⌘\(number + 1)").font(.appCaption2).foregroundStyle(.tertiary) }
                if let end {
                    SessionCloseButton(closing: model.closingTerminals.contains(ref), action: end)
                        .opacity(hovering || model.closingTerminals.contains(ref) ? 1 : 0)
                        .help("End \(Self.name(t)): stops its CLI on \(t.host)")
                        .accessibilityLabel("End \(Self.name(t))")
                }
            }
            HStack(spacing: 6) {
                Badge(provider: t.provider, text: t.badge)
                if let p = t.profile, !p.isEmpty {
                    Text(p).font(.app(10, .medium)).padding(.horizontal, 5).padding(.vertical, 1)
                        .overlay(Capsule().stroke(.secondary.opacity(0.5))).foregroundStyle(.secondary)
                        .help("Account: \(p)\(t.account.map { " · \($0)" } ?? "")")
                }
                Text(t.branch.map { $0.isEmpty || $0 == "HEAD" ? t.cwd : "\(t.cwd) · \($0)" } ?? t.cwd)
                    .font(.appCaption).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
                Spacer(minLength: 0)
                if let f = t.contextFraction, f >= 0.7 {
                    Text("\(Int(f * 100))%").font(.appCaption2).foregroundStyle(f >= 0.9 ? .red : .orange)
                        .help("Context used")
                }
            }
            .padding(.leading, 16)
            // Always two lines, empty or not, so a row keeps its height when its agent starts or stops asking.
            let ask = (state == .waiting || state == .blocked) ? (t.ask ?? t.fix).flatMap { $0.isEmpty ? nil : $0 } : nil
            Text(ask.map { "\(AppModel.headline(t)): \($0)" } ?? " ").font(.appCaption)
                .lineLimit(open ? 6 : 2, reservesSpace: !open)
                .foregroundStyle(wants ? AnyShapeStyle(state == .blocked || t.askKind == "error" ? .red : SessionStyle.attention) : AnyShapeStyle(.secondary))
                .padding(.leading, 16)
            if let m = model.migrations[ref], let p = m.current {
                Label(m.error.map { "\(m.isClone ? "Clone" : "Move") failed: \($0)" } ?? (m.done ? "\(m.isClone ? "Cloned" : "Moved") to \(model.machineLabel(m.to))" : "\(Migration.label(p.name, to: model.machineLabel(m.to)))…"),
                      systemImage: m.error != nil ? "exclamationmark.triangle" : m.done ? "checkmark.circle" : "arrow.right.circle")
                    .font(.appCaption).foregroundStyle(m.error != nil ? AnyShapeStyle(.red) : AnyShapeStyle(.secondary))
                    .lineLimit(3).padding(.leading, 16).help(p.detail ?? "")
            }
            if open { TerminalDetailView(ref: ref, terminal: t).padding(.leading, 16) }
        }
        .padding(.vertical, 2)
        .padding(.leading, CGFloat(tree.depth) * 14)
        .opacity(state == .ended ? SessionStyle.endedOpacity : 1)
        .overlay(alignment: .leading) {
            if selected { RoundedRectangle(cornerRadius: 1.5).fill(Color.accentColor).frame(width: 3).padding(.leading, -8) }
        }
        .onHover { hovering = $0 }
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityAction(named: "End Terminal") { end?() }
    }

    static func name(_ t: Terminal) -> String { t.title.isEmpty ? t.badge : t.title }

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

/// A generous, visible destructive target with immediate feedback while the hub stops its process.
struct SessionCloseButton: View {
    let closing: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            HStack(spacing: 4) {
                if closing {
                    ProgressView().controlSize(.small)
                    Text("Closing…").font(.appCaption)
                } else {
                    Image(systemName: "xmark.circle.fill").font(.system(size: 24, weight: .semibold))
                }
            }
            .frame(minWidth: 32, minHeight: 32)
            .contentShape(Rectangle())
        }
        .buttonStyle(.borderless)
        .foregroundStyle(.red)
        .disabled(closing)
        .accessibilityLabel(closing ? "Closing session" : "Close session")
    }
}

/// The state as a dot. It fades with age, beats while the agent works, and pulses red, slowly, while the agent waits
/// for you (docs/design.md). With Reduce Motion it stays still.
struct StateDot: View {
    let state: AgentState
    let wants: Bool
    var pulse = false
    var age: Date?
    @ViewState private var dim = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        let moving = !reduceMotion && (pulse || state == .working)
        TimelineView(.periodic(from: .now, by: 30)) { ctx in
            Circle().fill(color).frame(width: 8, height: 8)
                .opacity(pulse || state == .working ? 1 : Recency.dotOpacity(age, now: ctx.date))
        }
        .opacity(moving && dim ? (pulse ? 0.45 : 0.3) : 1)
        // Start again whenever the dot starts or stops moving, or changes from a beat to a pulse.
        .onChange(of: [moving, pulse], initial: true) { _, now in
            var still = Transaction()
            still.disablesAnimations = true
            withTransaction(still) { dim = false }
            guard now[0] else { return }
            DispatchQueue.main.async { // a later turn: in the same one SwiftUI would see true -> true and not restart
                withAnimation(.easeInOut(duration: now[1] ? 1 : 0.9).repeatForever(autoreverses: true)) { dim = true }
            }
        }
    }

    private var color: SwiftUI.Color {
        if pulse { return SessionStyle.needsYou }
        return switch state {
        case .working: .blue
        case .waiting: wants ? SessionStyle.attention : SessionStyle.attention.opacity(0.5)
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
        Text(text).font(.app(10, .medium)).padding(.horizontal, 5).padding(.vertical, 1)
            .foregroundStyle(color).background(color.opacity(0.16), in: Capsule())
    }

    private var color: SwiftUI.Color {
        switch provider {
        case "claude": .orange
        case "codex": .teal
        case "antigravity": .blue
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
            if let a = t.account, !a.isEmpty { line("Account", (t.profile.map { $0.isEmpty ? "" : "\($0) · " } ?? "") + a) }
            if let tool = detail?.tool { line("Now", tool) }
            if let agents = detail?.agents, !agents.isEmpty {
                let live = agents.filter(\.live)
                line("Subagents", live.isEmpty ? "\(agents.count), none running" : "\(live.count) running of \(agents.count)")
                ForEach(live, id: \.self) { a in
                    Text("• \(a.title)").font(.appCaption2).foregroundStyle(.secondary).lineLimit(1)
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
        .font(.appCaption)
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
