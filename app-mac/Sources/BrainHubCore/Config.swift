import Foundation

/// The saved hub list. Same file and format as the Tauri app (app/src-tauri/src/config.rs), so a Mac that ran
/// it keeps its hubs: `~/Library/Application Support/app.llmbrain.hub/hubs.json`.
public struct HubConfig: Codable, Equatable, Sendable {
    public struct Hub: Codable, Equatable, Sendable, Identifiable {
        public var name: String
        /// The hub's first address. It names the hub everywhere (terminals, panes, sidebar order), whichever
        /// address is in use.
        public var url: String
        /// More addresses of the same hub (another DNS name or IP). The app uses whichever answers fastest.
        public var addresses: [String]? = nil
        public var id: String { url }
        /// Every address: `url` first.
        public var all: [String] { [url] + (addresses ?? []).filter { $0 != url } }
    }

    public var hubs: [Hub] = []
    /// URL of the selected hub. Always one of `hubs`.
    public var selected: String?
    /// The Tauri app's quick-search hotkey. Kept so both apps can share the file.
    public var hotkey: String?

    public init() {}

    public static var defaultPath: URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("app.llmbrain.hub/hubs.json")
    }

    /// A missing file gives an empty list. A broken file is kept as `*.bad`. Hubs with a disallowed host are dropped,
    /// so a hand-edited file cannot point the app elsewhere.
    public static func load(_ path: URL) -> HubConfig {
        guard let data = try? Data(contentsOf: path) else { return HubConfig() }
        guard var c = try? JSONDecoder().decode(HubConfig.self, from: data) else {
            try? FileManager.default.removeItem(at: path.appendingPathExtension("bad"))
            try? FileManager.default.moveItem(at: path, to: path.appendingPathExtension("bad"))
            return HubConfig()
        }
        c.hubs.removeAll { (try? HubURL.validate($0.url)) != $0.url }
        for i in c.hubs.indices {
            c.hubs[i].addresses = c.hubs[i].addresses?.filter { (try? HubURL.validate($0)) == $0 }
        }
        c.fixSelection()
        return c
    }

    public func save(_ path: URL) throws {
        try FileManager.default.createDirectory(at: path.deletingLastPathComponent(), withIntermediateDirectories: true)
        let enc = JSONEncoder()
        enc.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        try (enc.encode(self) + Data("\n".utf8)).write(to: path, options: .atomic)
    }

    public var selectedHub: Hub? { hubs.first { $0.url == selected } }

    /// Add a hub; the first one becomes the selected one. Returns the normalised URL.
    @discardableResult
    public mutating func add(name: String, url: String) throws -> String {
        let url = try HubURL.validate(url)
        if hubs.contains(where: { $0.url == url }) { throw HubError("\(url) is already in the list.") }
        hubs.append(Hub(name: try Self.cleanName(name, url: url), url: url))
        fixSelection()
        return url
    }

    /// Set a hub's other addresses (any separator: spaces, commas or new lines). Returns them normalised.
    @discardableResult
    public mutating func setAddresses(_ url: String, _ text: String) throws -> [String] {
        guard let i = hubs.firstIndex(where: { $0.url == url }) else { throw HubError("No such hub.") }
        var out: [String] = []
        for raw in text.split(whereSeparator: { $0 == "," || $0.isWhitespace }) {
            let a = try HubURL.validate(String(raw))
            if a != url && !out.contains(a) { out.append(a) }
        }
        hubs[i].addresses = out.isEmpty ? nil : out
        return out
    }

    public mutating func remove(_ url: String) {
        hubs.removeAll { $0.url == url }
        fixSelection()
    }

    public mutating func select(_ url: String) {
        if hubs.contains(where: { $0.url == url }) { selected = url }
    }

    mutating func fixSelection() {
        if !hubs.contains(where: { $0.url == selected }) { selected = hubs.first?.url }
    }

    static func cleanName(_ name: String, url: String) throws -> String {
        let n = name.trimmingCharacters(in: .whitespaces)
        let out = n.isEmpty ? String(url.split(separator: "/").last ?? Substring(url)) : n
        if out.count > 64 { throw HubError("The name is too long (at most 64 characters).") }
        if out.unicodeScalars.contains(where: CharacterSet.controlCharacters.contains) {
            throw HubError("The name must not contain control characters.")
        }
        return out
    }
}
