import Foundation
import Testing
import BrainHubCore

@Suite struct AllowList {
    @Test func allowsLoopbackAndTailnet() throws {
        let cases = [
            ("http://127.0.0.1:8787", "http://127.0.0.1:8787"),
            ("http://127.0.0.1:8787/some/path?x=1#f", "http://127.0.0.1:8787"),
            ("https://hub.tail1234.ts.net", "https://hub.tail1234.ts.net"),
            ("https://hub.tail1234.ts.net:443/", "https://hub.tail1234.ts.net"),
            ("http://Mac.Tail1234.TS.NET:8787/", "http://mac.tail1234.ts.net:8787"),
            ("http://100.64.0.1:8787", "http://100.64.0.1:8787"),
            ("http://100.127.255.254:8787", "http://100.127.255.254:8787"),
            ("http://[::1]:8787", "http://[::1]:8787"),
            ("http://[fd7a:115c:a1e0::1]:8787", "http://[fd7a:115c:a1e0::1]:8787"),
        ]
        for (input, want) in cases { #expect(try HubURL.validate(input) == want, "\(input)") }
    }

    @Test func refusesEverythingElse() {
        for bad in ["http://example.com", "http://192.168.1.10:8787", "http://10.0.0.5", "http://100.63.255.255",
                    "http://100.128.0.1", "http://127.0.0.1.evil.com", "http://evil.com/.ts.net", "http://evilts.net",
                    "http://ts.net", "http://.ts.net", "http://foo.ts.net.evil.com", "http://localhost:8787",
                    "http://[2001:db8::1]", "http://user:pw@127.0.0.1:8787", "http://127.0.0.1@evil.com",
                    "ftp://127.0.0.1", "file:///etc/passwd", "127.0.0.1:8787", ""] {
            #expect(throws: HubError.self, "\(bad)") { try HubURL.validate(bad) }
        }
    }

    @Test func joinAcceptsOnlyHubPaths() throws {
        #expect(try HubURL.join("http://127.0.0.1:8787", "/api/terminals").absoluteString == "http://127.0.0.1:8787/api/terminals")
        for bad in ["//evil.com/x", "https://evil.com/", "sessions/1", "/a\\b", "/a\nb", ""] {
            #expect(throws: HubError.self, "\(bad)") { try HubURL.join("http://127.0.0.1:8787", bad) }
        }
        #expect(throws: HubError.self) { try HubURL.join("https://example.com", "/x") }
    }
}

@Suite struct Requests {
    @Test func searchPathIsEncoded() {
        #expect(Paths.search("hello") == "/api/search?q=hello&scope=cluster&limit=20")
        #expect(Paths.search("a b&c=d/é") == "/api/search?q=a%20b%26c%3Dd%2F%C3%A9&scope=cluster&limit=20")
    }

    @Test func formBody() {
        #expect(formEncode([("host", "mac-1"), ("cli", "shell"), ("path", "~/my projects"), ("format", "json")])
            == "host=mac-1&cli=shell&path=~%2Fmy%20projects&format=json")
    }

    @Test func errorText() {
        #expect(HubClient.failure(status: 400, body: Data(#"{"error":"no such host"}"#.utf8)) == "The hub answered 400: no such host")
        #expect(HubClient.failure(status: 403, body: Data("forbidden\n".utf8)) == "The hub answered 403: forbidden")
        #expect(HubClient.failure(status: 502, body: Data("<html>".utf8)) == "The hub answered 502")
    }
}

@Suite struct Decoding {
    func decode<T: Decodable>(_ s: String) throws -> T { try JSONDecoder().decode(T.self, from: Data(s.utf8)) }

    @Test func terminals() throws {
        let ts: [Terminal] = try decode(#"""
        [{"id":47,"title":"fix build","host":"mac-1","cli":"Codex (OpenAI)","provider":"codex","cwd":"~/x","status":"live",
          "updated":"2026-11-13T18:58:13.096Z","session_id":9},
         {"id":46,"title":"","host":"mac-2","cli":"Shell","provider":"shell","cwd":"~","status":"ended","error":"exit 1",
          "updated":"2026-11-13T18:40:46.521Z","blocked":{"kind":"x"},"fix":"y"}]
        """#)
        #expect(ts.map(\.id) == [47, 46])
        #expect(ts[0].isOpen && !ts[1].isOpen)
        #expect(ts[1].error == "exit 1")
    }

    @Test func statusAndNodes() throws {
        let s: TerminalStatus = try decode(#"{"status":"live","error":"","cwd":"/x","host_live":false,"blocked":null}"#)
        #expect(s == TerminalStatus(status: "live", error: "", cwd: "/x", hostLive: false))
        let n: ClusterNodes = try decode(#"""
        {"self":{"node":"studio","role":"permanent","cluster":"main","version":"v1"},
         "peers":[{"url":"http://100.100.1.2:8787","info":{"node":"laptop","role":"ephemeral","cluster":"main","version":"v1"},
                   "ok":true,"error":"","seen_at":"2026-11-13T13:57:36-05:00"}]}
        """#)
        #expect(n.names == ["studio", "laptop"])
    }

    @Test func searchWithNulls() throws {
        let r: SearchReply = try decode(#"{"took_ms":3,"total":0,"partial":null,"sessions":null}"#)
        #expect(r.sessions.isEmpty && r.partial.isEmpty)
        let full: SearchReply = try decode(#"""
        {"took_ms":12,"total":1,"partial":["laptop"],"sessions":[{"id":123,"node":"studio","host":"laptop","provider":"claude",
         "uuid":"u-1","title":"Fix the build","cwd":"/Users/x/app","branch":"main","last_at":"2026-11-13T09:00:00Z",
         "matches":3,"score":1.5,"snippet":"the build failed","link":"/sessions/123","continue_url":"/api/sessions/123/continue"}]}
        """#)
        #expect(full.sessions[0].continueURL == "/api/sessions/123/continue")
        #expect(full.partial == ["laptop"])
    }

    @Test func inboxWaitingTerminals() throws {
        let i: Inbox = try decode(#"""
        {"Items":[{"Key":"q:19","State":"waiting","Kind":"terminal","Title":"a","TermID":19,"LLMs":null},
                  {"Key":"s:3","State":"waiting","Kind":"session","Title":"b","TermID":0},
                  {"Key":"t:8","State":"blocked","Kind":"terminal","Title":"c","TermID":8}],
         "Counts":{"waiting":2},"Total":3,"Hash":"x"}
        """#)
        #expect(i.waitingTerminals == [19])
        let empty: Inbox = try decode(#"{"Items":null}"#)
        #expect(empty.items.isEmpty)
    }

    @Test func openedTerminal() throws {
        let nodes: ClusterNodes = try decode(#"""
        {"self":{"node":"studio"},"peers":[{"url":"http://100.100.1.2:8787","info":{"node":"laptop"},"ok":true}]}
        """#)
        let hub = "https://hub.tail1.ts.net"
        let local: OpenedTerminal = try decode(#"{"terminal_id":9,"mode":"fork","link":"/terminals/9"}"#)
        #expect(openedTerminalRef(local, hub: hub, nodes: nodes) == TerminalRef(hub: hub, id: 9))
        let remote: OpenedTerminal = try decode(#"{"terminal_id":4,"link":"/cluster/open?node=laptop&path=%2Fterminals%2F4"}"#)
        #expect(openedTerminalRef(remote, hub: hub, nodes: nodes) == TerminalRef(hub: "http://100.100.1.2:8787", id: 4))
        let none: OpenedTerminal = try decode(#"{}"#)
        #expect(openedTerminalRef(none, hub: hub, nodes: nodes) == nil)
    }
}

@Suite struct Frames {
    @Test func shellWords() {
        #expect(shellQuote("/Applications/Brain Hub.app/Contents/MacOS/brain-attach")
            == "'/Applications/Brain Hub.app/Contents/MacOS/brain-attach'")
        #expect(shellQuote("a'b; rm -rf ~") == #"'a'\''b; rm -rf ~'"#)
        #expect(shellQuote("") == "''")
    }

    @Test func afterClose() {
        let live = TerminalStatus(status: "live", hostLive: true)
        #expect(Wire.afterClose(code: 1006, reason: "", status: nil, host: "m", tries: 3)
            == .retry(message: "The hub is unreachable. Retrying…", after: 3))
        #expect(Wire.afterClose(code: 4000, reason: "the terminal is ended", status: TerminalStatus(status: "ended"), host: "m", tries: 1)
            == .ended(message: "the terminal is ended"))
        #expect(Wire.afterClose(code: 1000, reason: "", status: TerminalStatus(status: "failed", error: "no tmux"), host: "m", tries: 1)
            == .ended(message: "The terminal has failed. no tmux"))
        #expect(Wire.afterClose(code: 4000, reason: "the agent on m is offline", status: TerminalStatus(status: "live", hostLive: false), host: "m", tries: 20)
            == .retry(message: "m is offline. Retrying…", after: 10))
        #expect(Wire.afterClose(code: 4000, reason: "detached", status: live, host: "m", tries: 0) == .retry(message: "Reconnecting…", after: 0.2))
        #expect(Wire.afterClose(code: 1000, reason: "", status: TerminalStatus(status: "starting"), host: "m", tries: 2)
            == .retry(message: "Starting…", after: 1.5))
    }
}

@Suite struct Saved {
    func tmp(_ name: String) -> URL {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("brainhub-test-\(ProcessInfo.processInfo.processIdentifier)-\(name)")
        try? FileManager.default.removeItem(at: dir)
        return dir.appendingPathComponent("hubs.json")
    }

    @Test func roundTripAndTauriFormat() throws {
        let path = tmp("rt")
        var c = HubConfig()
        try c.add(name: "Studio", url: "https://hub.tail1.ts.net/")
        try c.add(name: "", url: "http://127.0.0.1:8787/ignored")
        #expect(c.selected == "https://hub.tail1.ts.net", "the first hub is selected")
        #expect(c.hubs[1].name == "127.0.0.1:8787")
        #expect(throws: HubError.self) { try c.add(name: "dup", url: "http://127.0.0.1:8787") }
        c.select("http://127.0.0.1:8787")
        try c.save(path)
        #expect(HubConfig.load(path) == c)
        c.remove("http://127.0.0.1:8787")
        #expect(c.selected == "https://hub.tail1.ts.net", "selection moves to a remaining hub")
    }

    @Test func readsTauriFileAndDropsBadHosts() throws {
        let path = tmp("tauri")
        try FileManager.default.createDirectory(at: path.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data(#"{"hubs":[{"name":"x","url":"https://example.com"},{"name":"ok","url":"http://127.0.0.1:8787"}],"selected":"https://example.com","hotkey":"Alt+Space"}"#.utf8).write(to: path)
        let c = HubConfig.load(path)
        #expect(c.hubs.map(\.url) == ["http://127.0.0.1:8787"])
        #expect(c.selected == "http://127.0.0.1:8787")
        #expect(c.hotkey == "Alt+Space")
    }

    @Test func brokenFileIsKept() throws {
        let path = tmp("broken")
        try FileManager.default.createDirectory(at: path.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data("{not json".utf8).write(to: path)
        #expect(HubConfig.load(path) == HubConfig())
        #expect(FileManager.default.fileExists(atPath: path.appendingPathExtension("bad").path))
    }
}

@Suite struct Grouping {
    func t(_ id: Int64, _ host: String, _ status: String, _ updated: String) -> Terminal {
        Terminal(id: id, title: "t\(id)", host: host, cli: "Shell", provider: "shell", cwd: "~", status: status, updated: updated)
    }

    @Test func groupsByMachine() throws {
        let live = try JSONDecoder().decode([LiveSession].self, from: Data(#"""
            [{"id":9,"title":"x","host":"laptop","cli":"Claude Code","cwd":"~","updated":"1","messages":3}]
            """#.utf8))
        let hub = "http://127.0.0.1:8787"
        let ms = machines(terminals: [(hub, t(1, "laptop", "live", "1")), (hub, t(2, "studio", "ended", "2")),
                                      (hub, t(3, "studio", "live", "3")), (hub, t(4, "other", "starting", "4")),
                                      (hub, t(5, "studio", "ended", "5"))],
                          live: live,
                          nodes: ["studio", "laptop"], keep: [TerminalRef(hub: hub, id: 5)])
        #expect(ms.map(\.name) == ["studio", "laptop", "other"])
        #expect(ms[0].terminals.map(\.terminal.id) == [5, 3], "ended ones only when kept; newest first")
        #expect(ms[1].elsewhere.map(\.id) == [9])
    }
}
