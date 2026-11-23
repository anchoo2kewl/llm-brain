import Foundation

/// A hub's shared memory: on only while the hub has a TaskAI API key (hub 0.1.10). The key never comes back.
public struct MemoryStatus: Decodable, Equatable, Sendable {
    public var enabled: Bool
    public var source: String?
    public var error: String?
    public var message: String

    public init(enabled: Bool, source: String? = nil, error: String? = nil, message: String) {
        (self.enabled, self.source, self.error, self.message) = (enabled, source, error, message)
    }
}

/// Setting, replacing and removing a hub's TaskAI API key. The app keeps no copy: the key goes in the POST body
/// to each chosen hub over the tailnet, and the field is cleared after.
public enum MemoryKey {
    /// One hub's answer: its status after, or why it failed.
    public struct Outcome: Equatable, Sendable {
        public var hub: String
        public var result: Result<MemoryStatus, HubError>
    }

    /// The key as typed, trimmed; nil when it can't be one (empty, spaces inside, too long).
    public static func clean(_ typed: String) -> String? {
        let k = typed.trimmingCharacters(in: .whitespacesAndNewlines)
        guard (8...512).contains(k.count), !k.contains(where: { $0.isWhitespace || $0.isNewline }) else { return nil }
        return k
    }

    /// A hub's error for a person. A hub before 0.1.10 has no key setting at all (404).
    public static func explain(_ error: Error) -> String {
        let text = (error as? HubError)?.message ?? error.localizedDescription
        if text.hasPrefix("The hub answered 404") { return "This hub has no TaskAI key setting: it needs hub 0.1.10 or later." }
        return text
    }

    /// Apply one change to every hub at once (`change` sets or removes the key on one hub) and say how each went.
    public static func apply(to hubs: [String], _ change: @escaping @Sendable (String) async throws -> MemoryStatus) async -> [Outcome] {
        await withTaskGroup(of: Outcome.self) { group in
            for hub in hubs {
                group.addTask {
                    do { return Outcome(hub: hub, result: .success(try await change(hub))) } catch {
                        return Outcome(hub: hub, result: .failure(HubError(explain(error))))
                    }
                }
            }
            var out: [Outcome] = []
            for await o in group { out.append(o) }
            return out.sorted { (hubs.firstIndex(of: $0.hub) ?? 0) < (hubs.firstIndex(of: $1.hub) ?? 0) }
        }
    }

    public static func status(_ hub: String) async throws -> MemoryStatus {
        try await HubClient(base: hub).get(Paths.memoryStatus, as: MemoryStatus.self, timeout: 8)
    }

    public static func set(_ key: String, on hub: String) async throws -> MemoryStatus {
        try await HubClient(base: hub).post(Paths.memoryKey, json: ["key": key], as: MemoryStatus.self, timeout: 30)
    }

    public static func remove(on hub: String) async throws -> MemoryStatus {
        try await HubClient(base: hub).delete(Paths.memoryKey, as: MemoryStatus.self)
    }
}
