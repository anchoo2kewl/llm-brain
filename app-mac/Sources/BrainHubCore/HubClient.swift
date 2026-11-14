import Foundation

/// HTTP to one hub. No redirects, no Origin or Sec-Fetch-Site header (the hub accepts a native client's POST
/// without them), and every URL passes the allow-list first.
public struct HubClient: Sendable {
    public let base: String

    public init(base: String) throws { self.base = try HubURL.validate(base) }

    public static let session: URLSession = {
        let c = URLSessionConfiguration.ephemeral
        c.connectionProxyDictionary = [:]
        c.timeoutIntervalForRequest = 15
        return URLSession(configuration: c, delegate: NoRedirects(), delegateQueue: nil)
    }()

    public func get<T: Decodable>(_ path: String, as: T.Type = T.self, timeout: TimeInterval = 8) async throws -> T {
        try decode(try await send(path, method: "GET", body: nil, contentType: nil, timeout: timeout))
    }

    /// POST a JSON body. Continue and move can take minutes (a move copies the conversation first).
    public func post<T: Decodable>(_ path: String, json: [String: any Sendable], as: T.Type = T.self,
                                   timeout: TimeInterval = 300) async throws -> T {
        let body = try JSONSerialization.data(withJSONObject: json)
        return try decode(try await send(path, method: "POST", body: body, contentType: "application/json", timeout: timeout))
    }

    /// POST a form (`/terminals/new`, `/terminals/{id}/rename`).
    public func post<T: Decodable>(_ path: String, form: [(String, String)], as: T.Type = T.self,
                                   timeout: TimeInterval = 60) async throws -> T {
        let body = Data(formEncode(form).utf8)
        return try decode(try await send(path, method: "POST", body: body,
                                         contentType: "application/x-www-form-urlencoded", timeout: timeout))
    }

    /// Upload one file (`POST /hosts/{host}/upload`, multipart field "file"). Returns the path on that machine.
    public func upload(_ path: String, name: String, data: Data, timeout: TimeInterval = 300) async throws -> String {
        struct Put: Decodable { var path: String }
        let boundary = "brainhub-" + UUID().uuidString
        let safe = name.replacingOccurrences(of: "\"", with: "_").replacingOccurrences(of: "\r", with: "_")
            .replacingOccurrences(of: "\n", with: "_")
        var body = Data("--\(boundary)\r\nContent-Disposition: form-data; name=\"file\"; filename=\"\(safe)\"\r\n".utf8)
        body.append(Data("Content-Type: application/octet-stream\r\n\r\n".utf8))
        body.append(data)
        body.append(Data("\r\n--\(boundary)--\r\n".utf8))
        let reply: Put = try decode(try await send(path, method: "POST", body: body,
                                                   contentType: "multipart/form-data; boundary=\(boundary)", timeout: timeout))
        return reply.path
    }

    func send(_ path: String, method: String, body: Data?, contentType: String?, timeout: TimeInterval) async throws -> Data {
        var req = URLRequest(url: try HubURL.join(base, path), timeoutInterval: timeout)
        req.httpMethod = method
        req.httpBody = body
        req.setValue("application/json", forHTTPHeaderField: "Accept")
        if let contentType { req.setValue(contentType, forHTTPHeaderField: "Content-Type") }
        let data: Data, resp: URLResponse
        do {
            (data, resp) = try await Self.session.data(for: req)
        } catch let e as URLError {
            throw HubError(e.code == .timedOut ? "The hub did not answer in time." : "Hub unreachable: \(e.localizedDescription)")
        }
        let status = (resp as? HTTPURLResponse)?.statusCode ?? 0
        guard (200..<300).contains(status) else { throw HubError(Self.failure(status: status, body: data)) }
        return data
    }

    /// "The hub answered 400: <its error>".
    public static func failure(status: Int, body: Data) -> String {
        struct E: Decodable { var error: String }
        var text = "The hub answered \(status)"
        if let e = try? JSONDecoder().decode(E.self, from: body), !e.error.isEmpty {
            text += ": \(e.error)"
        } else if let s = String(data: body.prefix(200), encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines),
                  !s.isEmpty, !s.hasPrefix("<") {
            text += ": \(s)"
        }
        return text
    }

    func decode<T: Decodable>(_ data: Data) throws -> T {
        do { return try JSONDecoder().decode(T.self, from: data) } catch {
            throw HubError("Unexpected reply from the hub: \(error)")
        }
    }
}

final class NoRedirects: NSObject, URLSessionTaskDelegate, Sendable {
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest) async -> URLRequest? { nil }
}
