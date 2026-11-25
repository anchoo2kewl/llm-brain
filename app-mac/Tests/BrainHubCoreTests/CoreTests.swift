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

    @Test func uploadPathAndPastedText() {
        #expect(Paths.upload(host: "mac-1", terminal: 7) == "/hosts/mac-1/upload?into=t7")
        #expect(Paths.upload(host: "a b/c", terminal: 7) == "/hosts/a%20b%2Fc/upload?into=t7")
        #expect(pastedPaths(["/u/t7/a.png"]) == "/u/t7/a.png ")
        #expect(pastedPaths(["/u/t7/a b.png", "/u/x.png"]) == "'/u/t7/a b.png' /u/x.png ")
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

    @Test func accounts() throws {
        let ps: [String: [Profile]] = try decode(#"""
        {"m":[{"name":"","claude":"me@home.test"},{"name":"work","claude":"me@work.test","codex":""}]}
        """#)
        let m = try #require(ps["m"])
        #expect(m[0].label(for: .claude) == "Default · me@home.test" && m[1].label(for: .claude) == "work · me@work.test")
        #expect(m[1].label(for: .codex) == "work · not logged in" && m[1].label(for: .shell) == "work")
        let t: Terminal = try decode(#"""
        {"id":1,"title":"a","host":"m","cli":"Codex","provider":"codex","cwd":"~","status":"live","updated":"","profile":"work","account":"me@work.test"}
        """#)
        #expect(t.profile == "work" && t.account == "me@work.test")
    }

    @Test func agentStateAndDetail() throws {
        let ts: [Terminal] = try decode(#"""
        [{"id":1,"title":"a","host":"m","cli":"Codex (OpenAI)","provider":"codex","cwd":"~","status":"live","updated":"",
          "state":"waiting","ask":"Allow Bash?","ask_kind":"question","model":"gpt","branch":"main",
          "context_used":150000,"context_window":200000,"subagents":2,"started":"2026-11-13T18:58:13.096Z"},
         {"id":2,"title":"b","host":"m","cli":"Claude Code","provider":"claude","cwd":"~","status":"live","updated":"",
          "state":"waiting","ask":"All done.","ask_kind":"done"},
         {"id":3,"title":"c","host":"m","cli":"Zsh","provider":"other","cwd":"~","status":"live","updated":""}]
        """#)
        #expect(ts[0].badge == "Codex" && ts[1].badge == "Claude Code" && ts[2].badge == "Zsh") // never "(OpenAI)"
        #expect(ts[0].state == .waiting && ts[0].needsAnswer && ts[0].contextFraction == 0.75 && ts[0].subagents == 2)
        #expect(ts[1].needsAnswer == false) // a finished turn is marked only until you look
        #expect(ts[2].state == nil && ts[2].contextFraction == nil) // an older hub
        let d: TerminalDetail = try decode(#"""
        {"id":1,"last_prompt":"fix it","last_reply":"done","tool":"Bash npm test",
         "agents":[{"title":"explore","updated":"x","messages":4,"live":true}],
         "stats":{"dollars":1.5,"tool_calls":7,"tool_errors":1,"prompts":3,"tokens":{}}}
        """#)
        #expect(d.tool == "Bash npm test" && d.agents?.first?.live == true && d.stats?.toolCalls == 7)
        #expect(shortCount(63499) == "63k" && shortCount(1_200_000) == "1.2M" && shortCount(12) == "12")
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
                          nodes: ["studio", "laptop"])
        #expect(ms.map(\.name) == ["studio", "laptop", "other"])
        #expect(ms[0].terminals.map(\.terminal.id) == [3], "open ones in the list")
        let ended = endedSessions(terminals: [(hub, t(2, "studio", "ended", "2")), (hub, t(5, "studio", "ended", "5")), (hub, t(6, "laptop", "failed", "6"))],
                                  keep: [TerminalRef(hub: hub, id: 5), TerminalRef(hub: hub, id: 6)])
        #expect(ended.map(\.terminal.id) == [6, 5], "ended ones only when kept, one group for every machine")
        #expect(ms[1].elsewhere.map(\.id) == [9])
        let more = try JSONDecoder().decode([LiveSession].self, from: Data(#"""
            [{"id":3,"title":"a","host":"m","cli":"x","cwd":"~","updated":"2027-01-15T08:00:20.900Z","messages":1},
             {"id":4,"title":"b","host":"m","cli":"x","cwd":"~","updated":"2027-01-15T08:00:20.100Z","messages":1},
             {"id":2,"title":"c","host":"m","cli":"x","cwd":"~","updated":"2027-01-15T08:00:30Z","messages":1}]
            """#.utf8))
        #expect(machines(terminals: [], live: more, nodes: ["m"])[0].elsewhere.map(\.id) == [2, 4, 3], "by activity, whole seconds, ties by id")
    }
}

@Suite struct SidebarOrder {
    /// A row from hub 0.1.12: `act` = last_activity_at, `input` = last_input_at (seconds after a fixed start).
    func term(_ id: Int64, act: Int? = nil, input: Int? = nil, started: Int = 0, updated: Int? = 0, state: AgentState? = nil) -> Terminal {
        func at(_ s: Int) -> String { ISO8601DateFormatter().string(from: Date(timeIntervalSince1970: 1_800_000_000 + Double(s))) }
        var t = Terminal(id: id, title: "", host: "m", cli: "x", provider: "x", cwd: "~", status: "live", updated: updated.map(at) ?? "", state: state)
        (t.started, t.lastActivity, t.lastInput) = (at(started), act.map(at), input.map(at))
        return t
    }
    func ids(_ ts: [Terminal], inputs: [Int64: Int] = [:]) -> [Int64] {
        let local = Dictionary(uniqueKeysWithValues: inputs.map { (TerminalRef(hub: "h", id: $0.key), Date(timeIntervalSince1970: 1_800_000_000 + Double($0.value))) })
        return machines(terminals: ts.map { ("h", $0) }, live: [], nodes: ["m"], inputs: local)[0].terminals.map(\.ref.id)
    }

    @Test func outputAndInputMoveARowUp() {
        let base = [term(1, act: 10), term(2, act: 20), term(3, act: 30)]
        #expect(ids(base) == [3, 2, 1])
        #expect(ids([term(1, act: 40), term(2, act: 20), term(3, act: 30)]) == [1, 3, 2], "agent output moves it up")
        #expect(ids([term(1, act: 10, input: 50), term(2, act: 20), term(3, act: 30)]) == [1, 3, 2], "owner input moves it up")
        #expect(ids(base, inputs: [2: 60]) == [2, 3, 1], "input this app sent counts on an older hub too")
    }

    @Test func activityMovesRowsButNotTheCycle() {
        func cycle(_ ts: [Terminal]) -> [Int64] {
            byStart(machines(terminals: ts.map { ("h", $0) }, live: [], nodes: ["m"])[0].terminals).map(\.ref.id)
        }
        let before = [term(1, act: 10, started: 1), term(2, act: 20, started: 2), term(3, act: 30, started: 3)]
        let after = [term(1, act: 40, started: 1), term(2, act: 20, started: 2), term(3, act: 30, started: 3)]
        #expect(ids(before) != ids(after), "output reorders the rows")
        #expect(cycle(before) == [1, 2, 3] && cycle(after) == [1, 2, 3], "next/previous keeps its order")
        #expect(cycle([term(5, started: 1), term(4, started: 1)]) == [4, 5], "ties: the lower id first")
    }

    @Test func nothingElseMovesARow() {
        let base = [term(1, act: 10), term(2, act: 20)]
        // A spinner or a state flip that the hub does not count as activity changes no timestamp.
        #expect(ids([term(1, act: 10, updated: 99, state: .waiting), term(2, act: 20, state: .working)]) == ids(base))
        #expect(ids([term(1, act: 20), term(2, act: 20)]) == [2, 1], "ties: the higher id first")
        // Less than a second apart is a tie: jitter never reorders.
        var a = term(1, act: 20), b = term(2, act: 20)
        a.lastActivity = "2027-01-15T08:00:20.900Z"; b.lastActivity = "2027-01-15T08:00:20.100Z"
        #expect(ids([a, b]) == [2, 1])
    }

    @Test func olderHubUsesUpdatedThenStart() {
        #expect(ids([term(1, updated: 50), term(2, updated: 40)]) == [1, 2])
        #expect(ids([term(1, started: 6, updated: nil), term(2, started: 5, updated: nil)]) == [1, 2], "start time only when there is nothing else")
    }

    @Test func aParentRisesWithItsChildrenAndChildrenFollowTheirOwnActivity() {
        func node(_ id: Int64, _ hub: String, parent: (String, Int64)? = nil, act: Int) -> Terminal {
            var t = term(id, act: act)
            (t.hubNode, t.parentHub, t.parentId) = (hub, parent?.0, parent?.1)
            return t
        }
        let ts = [node(1, "studio", act: 1),
                  node(2, "studio", parent: ("studio", 1), act: 50),
                  node(3, "studio", parent: ("studio", 1), act: 40),
                  node(1, "laptop", parent: ("studio", 2), act: 2), // same id, another hub
                  node(8, "studio", act: 30),
                  node(9, "studio", parent: ("studio", 77), act: 5)] // parent not listed: top level
        let top = machines(terminals: ts.map { ("h", $0) }, live: [], nodes: ["m"])[0].terminals
        #expect(top.map(\.terminal.id) == [1, 8, 9], "the conductor rises: its child worked at 50")
        #expect(top[0].children.map(\.terminal.id) == [2, 3], "children by their own activity")
        #expect(top[0].children[0].children.map { $0.terminal.hubNode } == ["laptop"])
        #expect(top[0].all.count == 4)
        let ms = machines(terminals: ts.map { ($0.hubNode == "laptop" ? "l" : "h", $0) }, live: [], nodes: ["m"])
        #expect(descendants(of: TerminalRef(hub: "h", id: 1), in: ms) == [TerminalRef(hub: "h", id: 2), TerminalRef(hub: "l", id: 1),
                                                                          TerminalRef(hub: "h", id: 3)], "every depth, the other hub's child too")
        #expect(descendants(of: TerminalRef(hub: "l", id: 1), in: ms).isEmpty, "the same id on another hub is another session")
        #expect(descendants(of: TerminalRef(hub: "h", id: 8), in: ms).isEmpty)
        #expect(descendants(of: TerminalRef(hub: "h", id: 99), in: ms).isEmpty, "not listed")
        let loop = [node(5, "s", parent: ("s", 6), act: 1), node(6, "s", parent: ("s", 5), act: 2), node(7, "s", parent: ("s", 7), act: 3)]
        let shown = machines(terminals: loop.map { ("h", $0) }, live: [], nodes: ["m"])[0].terminals
        #expect(shown.flatMap(\.all).count == 3, "a loop in bad data never hides a session")
    }
}

@Suite struct QuickSwitcher {
    @Test func prefixFirstThenContainsInSidebarOrder() {
        let names = ["Memory sync", "DB migration", "Docs site", "scratch", "Old db"]
        #expect(switcherMatches(names, query: "db") == [1, 4])
        #expect(switcherMatches(names, query: "s") == [3, 0, 2], "starts with s first, then contains s")
        #expect(switcherMatches(names, query: "  ") == [0, 1, 2, 3, 4])
        #expect(switcherMatches(names, query: "", limit: 2) == [0, 1])
        #expect(switcherMatches(names, query: "zzz").isEmpty)
    }
}

@Suite struct SendNowKeys {
    @Test func eachCLIGetsItsSendNowKeys() {
        #expect(SendNow.keys(provider: "claude") == [Data([0x0d]), Data([0x18]), Data([0x13])])
        #expect(SendNow.keys(provider: "codex") == [Data([0x0d])])
        #expect(SendNow.keys(provider: "antigravity") == [Data([0x1b, 0x6d])])
    }

    @Test func shellAndUnknownCLIsAreLeftAlone() {
        for p in ["shell", ""] { #expect(SendNow.keys(provider: p) == nil, "\(p)") }
    }
}
