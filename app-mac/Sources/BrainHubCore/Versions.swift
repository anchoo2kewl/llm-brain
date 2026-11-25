import Foundation

/// App and hub versions. They are separate (client-v* and hub-v* releases) and may differ.
public enum Versions {
    static func number(_ v: String) -> [Int]? {
        let parts = v.trimmingCharacters(in: .whitespaces).split(separator: ".", omittingEmptySubsequences: false)
        let n = parts.compactMap { Int($0) }
        return parts.count == 3 && n.count == 3 && n.allSatisfy { $0 >= 0 } ? n : nil
    }

    /// True when `a` is a higher version than `b`. Unreadable versions are never newer.
    public static func isNewer(_ a: String, than b: String) -> Bool {
        guard let x = number(a), let y = number(b) else { return false }
        return y.lexicographicallyPrecedes(x)
    }

    /// The version to restart into: the one installed on disk when it is newer than the running one.
    public static func restart(running: String?, installed: String?) -> String? {
        guard let running, let installed, isNewer(installed, than: running) else { return nil }
        return installed
    }

    /// The version in the update helper's check-only line "Version 0.1.13 is available: <url>".
    public static func available(inCheck line: String) -> String? {
        guard line.hasPrefix("Version ") else { return nil }
        let rest = line.dropFirst(8)
        guard let end = rest.range(of: " is available") else { return nil }
        let v = String(rest[..<end.lowerBound])
        return number(v) != nil ? v : nil
    }
}

/// When the app checks for a client release: on start, every hour, and after a wake from sleep (the app calls
/// `check` for a wake). A clock that moved back also starts a check.
public enum UpdateSchedule {
    public static let interval: TimeInterval = 60 * 60

    /// After a failed check (for example no network yet after a wake), try again sooner.
    public static let retry: TimeInterval = 5 * 60

    public static func isDue(now: Date, lastCheck: Date?, failed: Bool = false) -> Bool {
        guard let lastCheck else { return true }
        let since = now.timeIntervalSince(lastCheck)
        return since >= (failed ? retry : interval) || since < 0
    }
}

/// A hub's own version and its update state, for Settings only.
public struct HubVersions: Equatable, Sendable {
    /// The version the hub runs (`GET /api/hub-update/health`).
    public var running: String?
    /// Installed on disk and newer than `running`: the hub waits for a restart (`GET /api/hub-update`).
    public var installed: String?
    /// A newer release the hub's updater found.
    public var available: String?

    public var note: String {
        switch (running, installed, available) {
        case (nil, _, _): "Hub version unknown"
        case let (r?, i?, _): "Hub \(r) · \(i) installed, restart pending"
        case let (r?, nil, a?): "Hub \(r) · \(a) available"
        case let (r?, nil, nil): "Hub \(r)"
        }
    }

    public var needsAttention: Bool { installed != nil || available != nil }

    /// `GET /api/hub-update/health` and `GET /api/hub-update`: only the fields used here.
    public struct Reply: Decodable, Sendable {
        public var version: String?
        public var latest: String?
    }

    /// From the two replies (nil when the route failed: an older hub, no updater).
    public init(health: Reply?, status: Reply?) {
        func valid(_ v: String?) -> String? { v.flatMap { Versions.number($0) != nil ? $0 : nil } }
        running = valid(health?.version)
        installed = Versions.restart(running: running, installed: valid(status?.version))
        let base = installed ?? running ?? ""
        available = valid(status?.latest).flatMap { Versions.isNewer($0, than: base) ? $0 : nil }
    }
}
