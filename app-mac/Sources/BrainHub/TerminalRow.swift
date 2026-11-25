import BrainHubCore
import SwiftUI

/// One terminal in the sidebar, on two short lines (docs/design.md): the state dot, the name and the age; then the
/// tool, the model and the context used. The selected row adds the context bar and the details grid. The rest is in
/// the tooltip.
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
        /// Its subtree in numbers (rows with children).
        var roll: Rollup?
    }
    var tree = Tree()
    /// One click on × ends the terminal (its CLI stops on its machine).
    var end: (() -> Void)?
    @ViewState private var hovering = false
    private var model: AppModel { AppModel.shared }

    var body: some View {
        let t = terminal
        let state = model.state(ref), wants = model.wantsYou(ref), open = expanded.contains(ref)
        let recent = model.recent(ref)
        // A closed parent shows its subtree in numbers and pulses for a child that waits for you.
        let roll = tree.open ? nil : tree.roll
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .center, spacing: 8) {
                // One control for the tree: the chevron on the left. Closed hides every row under it.
                HStack(spacing: 4) {
                    if tree.children > 0 {
                        Button { tree.toggle?() } label: {
                            Image(systemName: tree.open ? "chevron.down" : "chevron.right").font(.system(size: 9, weight: .semibold))
                                .frame(width: 10, height: 16).contentShape(Rectangle())
                        }
                        .buttonStyle(.plain).foregroundStyle(.secondary)
                        .help(tree.open ? "Hide the child sessions (←)" : "Show the \(tree.children) child session\(tree.children == 1 ? "" : "s") (→)")
                        .accessibilityLabel("\(tree.children) child sessions, \(tree.open ? "shown" : "hidden")")
                    } else {
                        Color.clear.frame(width: 10, height: 1) // the names line up
                    }
                    StateDot(state: state, wants: wants, pulse: model.pulses(ref) || (roll?.needs ?? 0) > 0, age: recent)
                }
                // Neutral text: colour is only for the selection, the dot ("needs you" pulses red) and errors.
                Text(Self.name(t)).lineLimit(1).fontWeight(selected ? .bold : .regular)
                if tree.children > 0 {
                    Text("\(tree.children)").font(.appCaption2.monospacedDigit()).foregroundStyle(.secondary).fixedSize()
                        .help("\(tree.children) child session\(tree.children == 1 ? "" : "s")").accessibilityHidden(true)
                }
                if let hub = tree.hub { Text(hub).font(.appCaption2).foregroundStyle(.secondary).fixedSize() }
                Spacer(minLength: 0)
                ZStack(alignment: .trailing) {
                    TimelineView(.periodic(from: .now, by: 30)) { ctx in
                        Text(Recency.short(recent, now: ctx.date)).font(.appCaption2.monospacedDigit()).foregroundStyle(.secondary)
                    }
                    .opacity(end != nil && (hovering || model.closingTerminals.contains(ref)) ? 0 : 1)
                    if let end, hovering || model.closingTerminals.contains(ref) {
                        SessionCloseButton(closing: model.closingTerminals.contains(ref), action: end)
                            .help("End \(Self.name(t)): stops its CLI on \(t.host)")
                    }
                }
                .frame(minWidth: 26, alignment: .trailing).fixedSize()
            }
            .frame(minHeight: 22)
            let meta = RowMeta(t)
            HStack(spacing: 6) {
                ProviderMark(mark: meta.mark, tool: meta.tool)
                if let roll, !roll.label.isEmpty {
                    Button(roll.label) { if let f = roll.first { model.reveal = f } else { tree.toggle?() } }.buttonStyle(.plain).lineLimit(1)
                        .foregroundStyle(roll.needs > 0 ? AnyShapeStyle(SessionStyle.needsYou) : AnyShapeStyle(.secondary))
                        .help("Open the tree at the child that has waited longest")
                } else {
                    Text(meta.model).lineLimit(1).truncationMode(.tail)
                }
                Spacer(minLength: 0)
                if let p = meta.percent {
                    Text("\(p)%").monospacedDigit().foregroundStyle(ContextLevel(percent: p).style).fixedSize().help("Context used")
                }
            }
            .font(.appCaption2).foregroundStyle(.secondary).padding(.leading, 30).frame(minHeight: 14)
            if let m = model.migrations[ref], let p = m.current {
                Label(m.error.map { "\(m.isClone ? "Clone" : "Move") failed: \($0)" } ?? (m.done ? "\(m.isClone ? "Cloned" : "Moved") to \(model.machineLabel(m.to))" : "\(Migration.label(p.name, to: model.machineLabel(m.to)))…"),
                      systemImage: m.error != nil ? "exclamationmark.triangle" : m.done ? "checkmark.circle" : "arrow.right.circle")
                    .font(.appCaption).foregroundStyle(m.error != nil ? AnyShapeStyle(.red) : AnyShapeStyle(.secondary))
                    .lineLimit(3).padding(.leading, 30).help(p.detail ?? "")
            }
            if selected || open { TerminalDetailView(ref: ref, terminal: t, kids: tree.roll).padding(.leading, 30) }
        }
        .padding(.vertical, 2)
        .padding(.leading, CGFloat(tree.depth) * 16)
        .opacity(state == .ended ? SessionStyle.endedOpacity : 1)
        .overlay(alignment: .leading) {
            if selected { RoundedRectangle(cornerRadius: 1.5).fill(Color.accentColor).frame(width: 3).padding(.leading, -8) }
        }
        .contentShape(Rectangle())
        .onHover { hovering = $0 }
        .help(Self.tooltip(t, state: state, number: number))
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityAction(named: "End Terminal") { end?() }
    }

    /// The last folder of the working directory, and the branch when it is not the default.
    static func folder(_ t: Terminal) -> String {
        let last = (t.cwd as NSString).lastPathComponent
        let dir = t.cwd == "~" || last.isEmpty ? (t.cwd.hasPrefix("/") ? "/" : "~") : last
        guard let b = t.branch, !b.isEmpty, b != "HEAD", b != "main", b != "master" else { return dir }
        return "\(dir) · \(b)"
    }

    /// Everything the one line leaves out.
    static func tooltip(_ t: Terminal, state: AgentState, number: Int?) -> String {
        var lines = [name(t) + " · " + help(state, t)]
        if state == .waiting || state == .blocked, let ask = [t.ask, t.fix].compactMap({ $0 }).first(where: { !$0.isEmpty }) { lines.append(ask) }
        lines.append("\(t.badge) on \(t.host) · \(t.cwd)" + (t.branch.map { $0.isEmpty ? "" : " · \($0)" } ?? ""))
        if let p = t.profile, !p.isEmpty { lines.append("Account: \(p)" + (t.account.map { " · \($0)" } ?? "")) }
        if let n = t.subagents, n > 0 { lines.append("\(n) subagent\(n == 1 ? "" : "s") running") }
        if let number, number < 9 { lines.append("⌘\(number + 1) shows it") }
        return lines.joined(separator: "\n")
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
                    Image(systemName: "xmark.circle.fill").font(.system(size: 13))
                }
            }
            .frame(minWidth: 22, minHeight: 22)
            .contentShape(Rectangle())
        }
        .buttonStyle(.borderless)
        .foregroundStyle(.secondary)
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

/// Which CLI a terminal runs, as a small muted mark.
struct ProviderMark: View {
    let mark: String
    let tool: String

    var body: some View {
        Text(mark).font(.app(9, .medium)).foregroundStyle(.secondary)
            .frame(width: 16, height: 14).overlay(RoundedRectangle(cornerRadius: 3).strokeBorder(.quaternary))
            .help(tool).accessibilityLabel(tool)
    }
}

/// The selected (or opened) row: the context bar, then always the same rows (`Terminal.detailRows`), one line each, so
/// the row's height never changes when data arrives. It asks the hub again every 5 seconds while it shows.
struct TerminalDetailView: View {
    let ref: TerminalRef
    let terminal: Terminal
    var kids: Rollup?
    @ViewState private var detail: TerminalDetail?
    @ViewState private var note: String?

    var body: some View {
        let f = terminal.contextFraction ?? 0, known = RowMeta(terminal).percent, p = known ?? 0 // colours follow the % the row shows
        let level = ContextLevel(percent: p)
        VStack(alignment: .leading, spacing: 4) {
            // Drawn, not a ProgressView: a selected list row turns a ProgressView's tint grey.
            HStack(spacing: 8) {
                Capsule().fill(.quaternary).frame(height: 4)
                    .overlay(alignment: .leading) {
                        GeometryReader { g in Capsule().fill(level == .high ? Color.red : level == .warn ? .orange : .blue).frame(width: g.size.width * f) }
                    }
                    .accessibilityElement().accessibilityLabel("Context used").accessibilityValue(known.map { "\($0) percent" } ?? "not reported")
                if terminal.handoffOffered { HandoffButton(ref: ref, say: { note = $0 }) }
            }
            if let note { Text(note).font(.appCaption2).foregroundStyle(.secondary).lineLimit(1) }
            TimelineView(.periodic(from: .now, by: 5)) { ctx in
                Grid(alignment: .leading, horizontalSpacing: 8, verticalSpacing: 2) {
                    ForEach(terminal.detailRows(detail, now: ctx.date, kids: kids), id: \.label) { r in
                        GridRow {
                            Text(r.label).foregroundStyle(.tertiary)
                            Text(r.value).foregroundStyle(.secondary).lineLimit(1).truncationMode(.tail).help(r.help)
                        }
                        .accessibilityElement(children: .combine)
                    }
                }
            }
        }
        .font(.appCaption)
        .onChange(of: terminal.handoffOffered) { if !terminal.handoffOffered { note = nil } }
        .task(id: ref) {
            detail = nil // never show another session's numbers
            while !Task.isCancelled {
                do {
                    let d = try await HubClient(base: ref.hub).get(Paths.detail(ref.id), as: TerminalDetail.self)
                    if !Task.isCancelled { detail = d }
                } catch { if !Task.isCancelled, detail == nil { detail = TerminalDetail() } } // keep the last answer, else show "–"
                try? await Task.sleep(for: .seconds(5))
            }
        }
    }
}

extension ContextLevel {
    /// The colour of the context percent: grey, amber, red.
    var style: AnyShapeStyle {
        switch self {
        case .normal: AnyShapeStyle(.secondary)
        case .warn: AnyShapeStyle(.orange)
        case .high: AnyShapeStyle(.red)
        }
    }
}

/// The sidebar's top line: "Needs me (N)" (⌘J opens the longest wait; again, the next one) and "order paused" while
/// the pointer holds the rows in place.
struct NeedsBar: View {
    let go: () -> Void
    private var model: AppModel { AppModel.shared }

    var body: some View {
        let n = model.needsQueue.count
        // Always the same height: nothing under the pointer moves when the button or the mark comes or goes.
        HStack(spacing: 8) {
            Button("Needs me (\(n))", action: go).controlSize(.small).opacity(n > 0 ? 1 : 0).disabled(n == 0)
                .help("Open the session that has waited longest for you (⌘J). Again: the next one.")
                .accessibilityHidden(n == 0)
            Spacer(minLength: 0)
            Text("order paused").font(.appCaption2).foregroundStyle(.secondary).opacity(model.held != nil ? 1 : 0)
                .help("The rows keep their places while the pointer is in the list")
        }
        .padding(.horizontal, 12).padding(.vertical, 4)
    }
}

/// Handoff, for a context that is nearly full: the hub compacts the conversation, or starts a successor session as a
/// child of this one.
struct HandoffButton: View {
    let ref: TerminalRef
    let say: (String?) -> Void
    private var model: AppModel { AppModel.shared }

    var body: some View {
        Button(model.handingOff.contains(ref) ? "Handing off…" : "Handoff") {
            say(nil)
            Task {
                do {
                    let r = try await model.handoff(ref)
                    say(r.map { $0.mode == "successor" ? "Successor session \($0.id ?? 0) started" : "Compacting the conversation" })
                } catch { say("Handoff failed: \(error.localizedDescription)") }
            }
        }
        .controlSize(.small).disabled(model.handingOff.contains(ref))
        .help("Context is nearly full: the hub compacts this conversation, or starts a successor session as a child")
    }
}
