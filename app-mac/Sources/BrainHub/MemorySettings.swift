import BrainHubCore
import SwiftUI

/// The TaskAI API key that turns shared memory on, set on one hub or every hub. The key is typed into a secure
/// field, sent in the request body to each chosen hub, and cleared: the app keeps no copy and never shows it.
struct MemorySettings: View {
    private var model: AppModel { AppModel.shared }
    private static let allHubs = "*"
    @ViewState private var target = MemorySettings.allHubs
    @ViewState private var key = ""
    @ViewState private var statuses: [String: Result<MemoryStatus, HubError>] = [:]
    @ViewState private var results: [MemoryKey.Outcome] = []
    @ViewState private var busy = false
    @ViewState private var invalid = false

    /// Every hub this app can reach: the saved ones, then the cluster's others (named by node).
    private var hubs: [(name: String, url: String)] {
        model.config.hubs.map { ($0.name, $0.url) } + model.unsavedClusterHubs.map { ($0.node, $0.url) }
    }

    /// The hubs a change goes to; a hub gone meanwhile is never one of them.
    private var chosen: [String] {
        let all = hubs.map(\.url)
        return target == Self.allHubs ? all : all.filter { $0 == target }
    }

    var body: some View {
        Form {
            Section {
                Text("Agents share lessons through a hub only while it has a TaskAI API key (TaskAI → Settings → API Keys). The hub checks the key with TaskAI and stores it owner-only; this app keeps no copy.")
                    .font(.callout).foregroundStyle(.secondary)
            }
            Section("Hubs") {
                if hubs.isEmpty { Text("Add a hub first.").foregroundStyle(.secondary) }
                ForEach(hubs, id: \.url) { h in
                    HStack(alignment: .firstTextBaseline) {
                        VStack(alignment: .leading) {
                            Text(h.name)
                            Text(h.url).font(.caption).foregroundStyle(.secondary)
                        }
                        Spacer()
                        statusLabel(statuses[h.url])
                    }
                }
            }
            Section("Set, replace or remove the key") {
                Picker("On", selection: $target) {
                    Text("Every hub in the cluster").tag(Self.allHubs)
                    ForEach(hubs, id: \.url) { h in Text(h.name).tag(h.url) }
                }
                SecureField("TaskAI API key", text: $key)
                    .onSubmit(save)
                if invalid { Text("That is not a TaskAI API key.").foregroundStyle(.red).font(.callout) }
                HStack {
                    Button("Remove", role: .destructive, action: remove).disabled(busy || chosen.isEmpty)
                    Spacer()
                    if busy { ProgressView().controlSize(.small) }
                    Button("Save", action: save).keyboardShortcut(.defaultAction).disabled(busy || key.isEmpty || chosen.isEmpty)
                }
                ForEach(results, id: \.hub) { r in
                    HStack(alignment: .firstTextBaseline) {
                        Text(name(of: r.hub))
                        Spacer()
                        switch r.result {
                        case .failure(let e): Text(e.message).foregroundStyle(.red).font(.callout).multilineTextAlignment(.trailing)
                        case .success(let s):
                            Text(s.error ?? s.message).foregroundStyle(s.error != nil ? .orange : s.enabled ? .green : .secondary)
                                .font(.callout).multilineTextAlignment(.trailing)
                        }
                    }
                }
            }
        }
        .formStyle(.grouped)
        .frame(width: 560)
        .fixedSize(horizontal: false, vertical: true)
        .task(id: hubs.map(\.url)) {
            if !chosen.contains(target) && target != Self.allHubs { target = Self.allHubs }
            await refresh()
        }
    }

    @ViewBuilder private func statusLabel(_ r: Result<MemoryStatus, HubError>?) -> some View {
        switch r {
        case nil: ProgressView().controlSize(.small)
        case .failure(let e): Text(e.message).foregroundStyle(.secondary).font(.caption).multilineTextAlignment(.trailing)
        case .success(let s):
            VStack(alignment: .trailing) {
                Text(s.enabled ? "Memory on" : "Memory off").foregroundStyle(s.enabled ? .green : .orange)
                Text(s.error ?? (s.enabled ? (s.source.map { "key from \($0)" } ?? "") : s.message))
                    .font(.caption).foregroundStyle(.secondary).multilineTextAlignment(.trailing)
            }
        }
    }

    private func name(of hub: String) -> String { hubs.first { $0.url == hub }?.name ?? hub }

    private func refresh() async {
        for o in await MemoryKey.apply(to: hubs.map(\.url), MemoryKey.status) { statuses[o.hub] = o.result }
    }

    private func save() {
        guard !busy, !chosen.isEmpty else { return } // Return in the field while a change is under way
        guard let clean = MemoryKey.clean(key) else { invalid = true; return }
        invalid = false
        key = "" // never kept: the field is cleared before the hubs answer
        run { hub in try await MemoryKey.set(clean, on: hub) }
    }

    private func remove() {
        guard !busy, !chosen.isEmpty else { return }
        run { hub in try await MemoryKey.remove(on: hub) }
    }

    /// One change at a time: busy until every hub answered and the statuses are read again.
    private func run(_ change: @escaping @Sendable (String) async throws -> MemoryStatus) {
        let hubs = chosen
        busy = true
        Task {
            results = await MemoryKey.apply(to: hubs, change)
            for o in results { statuses[o.hub] = o.result }
            await refresh()
            busy = false
        }
    }
}
