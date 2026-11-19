import Foundation

/// An error with a sentence a person can read.
public struct HubError: Error, LocalizedError, Equatable, Sendable {
    public let message: String
    public init(_ message: String) { self.message = message }
    public var errorDescription: String? { message }
}

/// Where the app may talk to: the same allow-list as the Tauri app (app/src-tauri/src/allow.rs).
/// The hub must stay on loopback or the tailnet, so every saved hub and every request URL passes here.
public enum HubURL {
    public static let denyHint =
        "The hub must stay on loopback or the tailnet: use 127.0.0.1, a 100.64.0.0/10 address or a *.ts.net name."

    /// Loopback, Tailscale CGNAT (100.64.0.0/10), Tailscale IPv6 (fd7a:115c:a1e0::/48) and `*.ts.net`.
    public static func hostAllowed(_ rawHost: String) -> Bool {
        let host = rawHost.trimmingCharacters(in: CharacterSet(charactersIn: "[]")).lowercased()
        if let v4 = ipv4(host) {
            return v4[0] == 127 || (v4[0] == 100 && (v4[1] & 0xC0) == 64)
        }
        if host.contains(":") {
            guard let v6 = ipv6(host) else { return false }
            let loopback = v6.dropLast().allSatisfy { $0 == 0 } && v6.last == 1
            return loopback || (v6[0] == 0xfd && v6[1] == 0x7a && v6[2] == 0x11 && v6[3] == 0x5c && v6[4] == 0xa1 && v6[5] == 0xe0)
        }
        var d = host
        while d.hasSuffix(".") { d.removeLast() }
        let label = d.allSatisfy { $0.isASCII && ($0.isLetter || $0.isNumber || $0 == "-" || $0 == ".") }
        return label && d.count > ".ts.net".count && d.hasSuffix(".ts.net") && !d.hasPrefix(".") && !d.contains("..")
    }

    /// Check a hub URL typed by a person. Returns the origin, for example `http://127.0.0.1:8787`.
    public static func validate(_ input: String) throws -> String {
        let input = input.trimmingCharacters(in: .whitespacesAndNewlines)
        guard input.contains("://") else { throw HubError("Enter a full URL, for example http://127.0.0.1:8787") }
        guard let c = URLComponents(string: input), let scheme = c.scheme?.lowercased() else {
            throw HubError("Not a valid URL.")
        }
        guard scheme == "http" || scheme == "https" else { throw HubError("Only http and https hub URLs are allowed.") }
        guard c.user == nil, c.password == nil else { throw HubError("A hub URL must not contain a user name or password.") }
        guard let host = c.host, !host.isEmpty else { throw HubError("The URL has no host.") }
        guard hostAllowed(host) else { throw HubError("Host \(host) is not allowed. \(denyHint)") }
        return origin(scheme: scheme, host: host, port: c.port)
    }

    /// Join a hub base URL with a path on that hub. Refuses anything that is not a plain absolute path,
    /// so a hub reply cannot send the app to another host.
    public static func join(_ base: String, _ path: String) throws -> URL {
        if !path.hasPrefix("/") || path.hasPrefix("//") || path.contains("\\")
            || path.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }) {
            throw HubError("Refused link \(path): only paths on the hub are allowed.")
        }
        let origin = try validate(base)
        guard let url = URL(string: origin + path) else { throw HubError("Bad link \(path).") }
        guard let host = url.host, let scheme = url.scheme,
              Self.origin(scheme: scheme, host: host, port: url.port) == origin else {
            throw HubError("Refused link \(path): it leaves the hub.")
        }
        return url
    }

    static func origin(scheme: String, host: String, port: Int?) -> String {
        var h = host.lowercased()
        while h.hasSuffix(".") && !h.contains(":") { h.removeLast() }
        if h.contains(":") && !h.hasPrefix("[") { h = "[\(h)]" }
        let defaultPort = scheme == "https" ? 443 : 80
        if let port, port != defaultPort { return "\(scheme)://\(h):\(port)" }
        return "\(scheme)://\(h)"
    }

    private static func ipv4(_ s: String) -> [UInt8]? {
        let parts = s.split(separator: ".", omittingEmptySubsequences: false)
        guard parts.count == 4 else { return nil }
        var out: [UInt8] = []
        for p in parts {
            guard !p.isEmpty, p.count <= 3, p.allSatisfy(\.isASCII), p.allSatisfy(\.isNumber), let n = UInt8(p) else { return nil }
            out.append(n)
        }
        return out
    }

    private static func ipv6(_ s: String) -> [UInt8]? {
        var addr = in6_addr()
        guard inet_pton(AF_INET6, s, &addr) == 1 else { return nil }
        return withUnsafeBytes(of: &addr) { Array($0) }
    }
}

/// Percent-encoding for query values and form bodies: everything but unreserved characters is escaped.
public func formEncode(_ pairs: [(String, String)]) -> String {
    let unreserved = CharacterSet(charactersIn: "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~")
    func enc(_ s: String) -> String { s.addingPercentEncoding(withAllowedCharacters: unreserved) ?? "" }
    return pairs.map { "\(enc($0.0))=\(enc($0.1))" }.joined(separator: "&")
}
