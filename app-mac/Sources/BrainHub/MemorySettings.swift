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

    private var chosen: [String] {
        target == Self.allHubs ? model.config.hubs.map(\.url) : [target]
    }

    var body: some View {
        Form {
            Section {
                Text("Agents share lessons through a hub only while it has a TaskAI API key (TaskAI → Settings → API Keys). The hub checks the key with TaskAI and stores it owner-only; this app keeps no copy.")
                    .font(.callout).foregroundStyle(.secondary)
            }
            Section("Hubs") {
                if model.config.hubs.isEmpty { Text("Add a hub first.").foregroundStyle(.secondary) }
                ForEach(model.config.hubs) { h in
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
                    Text("Every hub").tag(Self.allHubs)
                    ForEach(model.config.hubs) { h in Text(h.name).tag(h.url) }
                }
                SecureField("TaskAI API key", text: $key)
                    .onSubmit(save)
                if invalid { Text("That is not a TaskAI API key.").foregroundStyle(.red).font(.callout) }
                HStack {
                    Button("Remove", role: .destructive, action: remove).disabled(busy || model.config.hubs.isEmpty)
                    Spacer()
                    if busy { ProgressView().controlSize(.small) }
                    Button("Save", action: save).keyboardShortcut(.defaultAction).disabled(busy || key.isEmpty || model.config.hubs.isEmpty)
                }
                ForEach(results, id: \.hub) { r in
                    HStack(alignment: .firstTextBaseline) {
                        Text(name(of: r.hub))
                        Spacer()
                        if let e = r.error { Text(e).foregroundStyle(.red).font(.callout).multilineTextAlignment(.trailing) }
                        else if let s = r.status { Text(s.enabled ? "On" : s.message).foregroundStyle(s.enabled ? .green : .orange).font(.callout) }
                    }
                }
            }
        }
        .formStyle(.grouped)
        .frame(width: 560)
        .fixedSize(horizontal: false, vertical: true)
        .task { await refresh() }
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

    private func name(of hub: String) -> String { model.config.hubs.first { $0.url == hub }?.name ?? hub }

    private func refresh() async {
        let hubs = model.config.hubs.map(\.url)
        await withTaskGroup(of: (String, Result<MemoryStatus, HubError>).self) { group in
            for hub in hubs {
                group.addTask {
                    do { return (hub, .success(try await MemoryKey.status(hub))) } catch { return (hub, .failure(HubError(MemoryKey.explain(error)))) }
                }
            }
            for await (hub, r) in group { statuses[hub] = r }
        }
    }

    private func save() {
        guard let clean = MemoryKey.clean(key) else { invalid = true; return }
        invalid = false
        key = "" // never kept: the field is cleared before the hubs answer
        run { hub in try await MemoryKey.set(clean, on: hub) }
    }

    private func remove() { run { hub in try await MemoryKey.remove(on: hub) } }

    private func run(_ change: @escaping @Sendable (String) async throws -> MemoryStatus) {
        let hubs = chosen
        busy = true
        Task {
            results = await MemoryKey.apply(to: hubs, change)
            busy = false
            await refresh()
        }
    }
}
