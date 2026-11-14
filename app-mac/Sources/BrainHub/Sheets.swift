import BrainHubCore
import SwiftUI

/// Cmd+T: machine, agent CLI, folder.
struct NewTerminalSheet: View {
    var opened: (TerminalRef) -> Void
    @Environment(\.dismiss) private var dismiss
    private var model: AppModel { AppModel.shared }
    @AppStorage("newTerminal.cli") private var cli: CLI = .claude
    @AppStorage("newTerminal.machine") private var machine = ""
    @ViewState private var path = ""
    @ViewState private var title = ""
    @ViewState private var busy = false
    @ViewState private var failure: String?

    var body: some View {
        Form {
            Picker("Machine", selection: $machine) {
                ForEach(model.machineNames, id: \.self) { Text($0).tag($0) }
            }
            Picker("Run", selection: $cli) {
                ForEach(CLI.allCases) { Text($0.label).tag($0) }
            }
            .pickerStyle(.segmented)
            HStack {
                TextField("Folder", text: $path, prompt: Text("~ (home)"))
                if !recentFolders.isEmpty {
                    Menu {
                        ForEach(recentFolders, id: \.self) { f in Button(f) { path = f } }
                    } label: { Image(systemName: "clock") }
                        .menuStyle(.borderlessButton).fixedSize()
                        .help("Recent folders on this machine")
                }
            }
            TextField("Title", text: $title, prompt: Text("optional"))
            if let failure { Text(failure).foregroundStyle(.red).font(.callout) }
        }
        .formStyle(.grouped)
        .frame(width: 460)
        .toolbar {
            ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
            ToolbarItem(placement: .confirmationAction) {
                Button(busy ? "Starting…" : "Start") { start() }.disabled(busy || machine.isEmpty)
            }
        }
        .onAppear {
            if !model.machineNames.contains(machine) { machine = model.nodes?.me.node ?? model.machineNames.first ?? "" }
        }
    }

    private var recentFolders: [String] {
        var seen: [String] = []
        for (_, t) in model.terminals where t.host == machine && !t.cwd.isEmpty && !seen.contains(t.cwd) { seen.append(t.cwd) }
        return Array(seen.prefix(10))
    }

    private func start() {
        busy = true
        failure = nil
        Task {
            do {
                let ref = try await model.create(host: machine, cli: cli, path: path.trimmingCharacters(in: .whitespaces), title: title)
                opened(ref)
                dismiss()
            } catch {
                failure = error.localizedDescription
                busy = false
            }
        }
    }
}

/// Cmd+K: conversations across the cluster, with Continue and Move.
struct SearchPalette: View {
    var opened: (TerminalRef) -> Void
    @Environment(\.dismiss) private var dismiss
    private var model: AppModel { AppModel.shared }
    @ViewState private var query = ""
    @ViewState private var hits: [SessionHit] = []
    @ViewState private var partial: [String] = []
    @ViewState private var highlighted = 0
    @ViewState private var searching = false
    @ViewState private var busy: String?
    @ViewState private var failure: String?
    @AppStorage("move.closeSource") private var closeSource = false
    @FocusState private var fieldFocused: Bool

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Image(systemName: "magnifyingglass").foregroundStyle(.secondary)
                TextField("Search conversations on every machine", text: $query)
                    .textFieldStyle(.plain).font(.title3)
                    .focused($fieldFocused)
                    .onKeyPress(.downArrow) { highlighted = min(highlighted + 1, max(hits.count - 1, 0)); return .handled }
                    .onKeyPress(.upArrow) { highlighted = max(highlighted - 1, 0); return .handled }
                    .onSubmit { if hits.indices.contains(highlighted) { continueHit(hits[highlighted]) } }
                if searching { ProgressView().controlSize(.small) }
            }
            .padding(12)
            Divider()
            ScrollViewReader { proxy in
                List(Array(hits.enumerated()), id: \.element.id) { i, hit in
                    row(hit, selected: i == highlighted)
                        .id(i)
                        .contentShape(Rectangle())
                        .onTapGesture { highlighted = i }
                        .onTapGesture(count: 2) { continueHit(hit) }
                }
                .listStyle(.plain)
                .onChange(of: highlighted) { proxy.scrollTo(highlighted) }
            }
            .overlay {
                if hits.isEmpty && !query.isEmpty && !searching && failure == nil {
                    Text("No conversations match.").foregroundStyle(.secondary)
                }
            }
            Divider()
            HStack {
                if let busy { ProgressView().controlSize(.small); Text(busy) }
                else if let failure { Text(failure).foregroundStyle(.red).lineLimit(2) }
                else if !partial.isEmpty { Text("Not answering: \(partial.joined(separator: ", "))").foregroundStyle(.secondary) }
                else { Text("↩ Continue · double-click a result").foregroundStyle(.secondary) }
                Spacer()
                Toggle("End the old terminal on a move", isOn: $closeSource).toggleStyle(.checkbox)
                Button("Close") { dismiss() }.keyboardShortcut(.cancelAction)
            }
            .font(.callout)
            .padding(10)
        }
        .frame(width: 680, height: 460)
        .onAppear { fieldFocused = true }
        .task(id: query) { await search() }
    }

    private func row(_ hit: SessionHit, selected: Bool) -> some View {
        HStack(alignment: .top) {
            VStack(alignment: .leading, spacing: 2) {
                Text(hit.title.isEmpty ? "(untitled)" : hit.title).lineLimit(1).font(.headline)
                Text("\(hit.host) · \(hit.provider) · \(hit.cwd)\(hit.branch.isEmpty ? "" : " · \(hit.branch)") · \(ago(hit.lastAt))")
                    .font(.caption).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
                if !hit.snippet.isEmpty { Text(hit.snippet).font(.callout).foregroundStyle(.secondary).lineLimit(2) }
            }
            Spacer()
            if selected {
                Button("Continue") { continueHit(hit) }
                Menu("Move") {
                    ForEach(model.machineNames.filter { $0 != hit.host }, id: \.self) { m in
                        Button("Move to \(m)") { move(hit, to: m) }
                    }
                }
                .fixedSize()
                .disabled(hit.provider != "claude" && hit.provider != "codex")
                .help("Move this conversation, with all its context, to another machine")
            }
        }
        .padding(.vertical, 4)
        .listRowBackground(selected ? Color.accentColor.opacity(0.15) : Color.clear)
    }

    private func search() async {
        let q = query.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty, let client = model.client else { hits = []; partial = []; return }
        try? await Task.sleep(for: .milliseconds(150))
        if Task.isCancelled { return }
        searching = true
        defer { searching = false }
        do {
            let r = try await client.get(Paths.search(q), as: SearchReply.self)
            if Task.isCancelled { return }
            (hits, partial, highlighted, failure) = (r.sessions, r.partial, 0, nil)
        } catch {
            if !Task.isCancelled { failure = error.localizedDescription }
        }
    }

    private func continueHit(_ hit: SessionHit) {
        run("Opening a terminal that continues it…") { try await model.continueSession(hit) }
    }

    private func move(_ hit: SessionHit, to machine: String) {
        run("Moving to \(machine)…") { try await model.move(hit, to: machine, closeSource: closeSource) }
    }

    private func run(_ what: String, _ op: @escaping () async throws -> TerminalRef) {
        guard busy == nil else { return }
        busy = what
        failure = nil
        Task {
            do {
                opened(try await op())
                dismiss()
            } catch {
                failure = error.localizedDescription
            }
            busy = nil
        }
    }
}

func ago(_ iso: String) -> String {
    let f = ISO8601DateFormatter()
    f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    guard let d = f.date(from: iso) ?? ISO8601DateFormatter().date(from: iso) else { return iso }
    return RelativeDateTimeFormatter().localizedString(for: d, relativeTo: Date())
}
