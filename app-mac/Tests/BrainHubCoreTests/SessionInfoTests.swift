import Foundation
import Testing
import BrainHubCore

// The same cases as tests/wslist.test.mjs: the Mac and the web rows must say the same thing.
@Suite struct SessionInfoTests {
    @Test func modelShortNames() {
        let cases = ["claude-opus-5-5": "Opus 5.5", "claude-sonnet-4-5-20250929": "Sonnet 4.5", "claude-3-5-sonnet-20241022": "Sonnet 3.5",
                     "claude-opus-5-5[1m]": "Opus 5.5", "claude-fable-5-2": "Fable 5.2", "gpt-6.1-sol": "GPT-6.1-Sol", "gpt-5-codex": "GPT-5-Codex",
                     "gemini-4-carbon": "Gemini 4 Carbon", "gemini-2.5-pro": "Gemini 2.5 Pro", "claude-2.1": "Claude 2.1", "claude-opus-5-5\n": "Opus 5.5", "claude-4.-opus": "4. Opus", "o3": "o3", "Custom Model": "Custom Model", "": ""]
        for (id, want) in cases { #expect(modelShort(id) == want, "\(id)") }
        #expect(modelShort(nil) == "")
    }

    @Test func durations() {
        #expect(duration(0) == "0s")
        #expect(duration(45.9) == "45s")
        #expect(duration(180) == "3m")
        #expect(duration(3600) == "1h")
        #expect(duration(2 * 3600 + 5 * 60) == "2h 5m")
        #expect(duration(3 * 86400 + 4 * 3600 + 59) == "3d 4h")
        #expect(duration(-5) == "0s")
        #expect(duration(-1e20) == "0s" && duration(.infinity) == "0s" && duration(.nan) == "0s")
        #expect(duration(.greatestFiniteMagnitude).contains("d"), "no trap")
    }

    private func term(_ f: (inout Terminal) -> Void = { _ in }) -> Terminal {
        var t = Terminal(id: 7, title: "t", host: "studio", cli: "Claude Code", provider: "claude", cwd: "/work/app", status: "live",
                         updated: "2026-11-25T11:59:00Z", state: .working)
        (t.branch, t.model, t.stateSince, t.started) = ("feat/x", "claude-opus-5-5", "2026-11-25T11:57:00Z", "2026-11-25T10:00:00Z")
        (t.contextUsed, t.contextWindow, t.subagents) = (92608, 200_000, 2)
        f(&t)
        return t
    }

    @Test func rowMeta() {
        #expect(RowMeta(term()) == RowMeta(term()))
        let m = RowMeta(term())
        #expect([m.mark, m.tool, m.model] == ["CC", "Claude Code", "Opus 5.5"] && m.percent == 46)
        let c = RowMeta(term { ($0.provider, $0.cli, $0.model, $0.contextUsed) = ("codex", "Codex", nil, 0) })
        #expect([c.mark, c.tool, c.model] == ["CX", "Codex", "Codex"] && c.percent == nil)
        #expect(RowMeta(term { ($0.contextUsed, $0.contextWindow) = (300, 200) }).percent == 100)
        #expect(RowMeta(term { ($0.provider, $0.cli) = ("", "Claude Code") }).mark == "CC", "only cli, as a session running elsewhere")
        #expect(RowMeta(term { $0.provider = "Codex" }).mark == "CX")
        let other = RowMeta(term { ($0.provider, $0.cli, $0.model) = ("Aider", "", nil) })
        #expect(other.tool == "Aider" && other.model == "Aider")
    }

    @Test func detailRowsKeepTheirShape() throws {
        let now = try #require(ISO8601DateFormatter().date(from: "2026-11-25T12:00:00Z"))
        let before = term().detailRows(nil, now: now)
        #expect(before.map(\.label) == ["Now", "Context", "Where", "Account", "Totals", "You", "Agent", "Started"])
        #expect(before[1].value == "92k of 200k · 46%")
        #expect(before[0].value == "Working 3m · 2 subagents")
        #expect(before[2].value == "studio · app · feat/x" && before[2].help == "studio · /work/app · feat/x")
        #expect(before[3].value == "default login")
        #expect(before[5].value == "…")
        #expect(before[7].value == "2h ago")
        let json = #"{"tool":"Bash { \"command\": \"ls\" }","last_prompt":"Fix the\nbuild","last_reply":"","stats":{"dollars":0.589,"#
            + #""tokens":{"input":32,"output":3770,"cache_read":1110178,"cache_write":58295},"prompts":1,"tool_calls":11,"tool_errors":1}}"#
        let d = try JSONDecoder().decode(TerminalDetail.self, from: Data(json.utf8))
        let after = term().detailRows(d, now: now)
        #expect(after.count == before.count)
        #expect(after[0].value == "Working 3m · 2 subagents · Bash" && after[0].help == #"Bash { "command": "ls" }"#)
        #expect(after[4].value == "$0.59 · 1.2M tokens · 1 prompt · 11 tool calls (1 failed)")
        #expect(after[5].value == "Fix the build" && after[6].value == "–")
        let waits = term { ($0.state, $0.needsOwner, $0.branch, $0.contextUsed) = (.waiting, true, "main", nil) }.detailRows(d, now: now)
        #expect(waits[0].value.hasPrefix("Waiting for you 3m"))
        #expect(waits[1].value == "not reported")
        #expect(waits[2].value == "studio · app")
        let done = term { ($0.state, $0.askKind, $0.needsOwner) = (.waiting, "done", false) }.detailRows(d, now: now)
        #expect(done[0].value.hasPrefix("Finished its turn"))
        func label(_ f: (inout Terminal) -> Void) -> Substring {
            term { f(&$0); ($0.stateSince, $0.subagents) = (nil, 0) }.detailRows(d, now: now)[0].value.split(separator: " · ")[0]
        }
        #expect(label { ($0.state, $0.askKind) = (.waiting, "error") } == "Stopped on an error")
        #expect(label { $0.state = .blocked } == "Waiting for you")
        #expect(label { $0.state = .idle } == "Idle")
        #expect(label { $0.state = nil } == "Unknown")
        #expect(term { ($0.profile, $0.account) = ("work", "dev@example.com") }.detailRows(d, now: now)[3].value == "work · dev@example.com")
        #expect(term().detailRows(TerminalDetail(), now: now)[4...6].map(\.value) == ["–", "–", "–"])
        #expect(term { $0.started = "" }.detailRows(d, now: now)[7].help == "–")
        for status in ["ended", "failed"] {
            #expect(term { ($0.status, $0.needsOwner) = (status, true) }.detailRows(d, now: now)[0].value.hasPrefix("Ended"))
        }
    }

    @Test func contextLevelsAndHandoff() {
        #expect([74, 75, 84, 85, 100].map { ContextLevel(percent: $0) } == [.normal, .warn, .warn, .high, .high])
        func at(_ used: Int, _ f: (inout Terminal) -> Void = { _ in }) -> Terminal {
            term { ($0.contextUsed, $0.contextWindow, $0.needsOwnerSince, $0.git) = (used, 100, nil, GitSummary()); f(&$0) }
        }
        #expect([79, 80].map { at($0).handoffOffered } == [false, true])
        #expect(!at(90) { $0.git = nil }.handoffOffered, "an older hub: no action")
        #expect(at(90) { ($0.git, $0.handoff) = (nil, true) }.handoffOffered, "the hub's own flag wins")
        #expect(!at(90) { $0.handoff = false }.handoffOffered)
        #expect(!at(90) { $0.status = "ended" }.handoffOffered)
    }

    private func entries(_ ts: [Terminal]) -> [Machine.Entry] {
        machines(terminals: ts.map { ("h", $0) }, live: [], nodes: ["m"]).flatMap(\.terminals).flatMap(\.all)
    }

    @Test func needsMeQueue() {
        func w(_ id: Int64, _ f: (inout Terminal) -> Void) -> Terminal { term { $0.id = id; $0.hubNode = "h"; ($0.parentHub, $0.parentId) = (nil, nil); f(&$0) } }
        let a = w(1) { ($0.state, $0.needsOwner, $0.needsOwnerSince) = (.waiting, true, "2026-11-25T12:10:00Z") }
        let b = w(2) { ($0.state, $0.needsOwner, $0.needsOwnerSince) = (.waiting, true, "2026-11-25T12:05:00Z") }
        let c = w(3) { ($0.state, $0.needsOwner, $0.stateSince) = (.blocked, nil, "2026-11-25T12:01:00Z") } // an older hub
        let busy = w(4) { ($0.state, $0.needsOwner) = (.working, false) }
        let q = needsQueue(entries([a, b, c, busy]))
        let r = { (id: Int64) in TerminalRef(hub: "h", id: id) }
        #expect(q == [r(3), r(2), r(1)])
        #expect(nextNeeds(q, current: nil) == r(3) && nextNeeds(q, current: r(2)) == r(1) && nextNeeds(q, current: r(1)) == r(3))
        #expect(nextNeeds(q, current: r(4)) == r(3), "after an answer the current one left the queue")
        #expect(nextNeeds([], current: r(1)) == nil)
    }

    @Test func rollupAndStatusLine() throws {
        func k(_ id: Int64, _ parent: Int64?, _ f: (inout Terminal) -> Void) -> Terminal {
            term { $0.id = id; $0.hubNode = "h"; ($0.parentHub, $0.parentId) = (parent.map { _ in "h" }, parent); f(&$0) }
        }
        let ts = [k(1, nil) { ($0.state, $0.needsOwner) = (.waiting, false) }, k(2, 1) { $0.state = .working },
                  k(3, 1) { ($0.state, $0.needsOwner, $0.needsOwnerSince) = (.waiting, true, "2026-11-25T12:09:00Z") },
                  k(4, 3) { ($0.state, $0.needsOwner, $0.needsOwnerSince) = (.waiting, true, "2026-11-25T12:01:00Z") },
                  k(5, 2) { $0.state = .working }]
        let top = machines(terminals: ts.map { ("h", $0) }, live: [], nodes: ["m"]).flatMap(\.terminals)
        let roll = Rollup(top[0])
        #expect([roll.needs, roll.working, roll.waiting, roll.total] == [2, 2, 0, 4] && roll.label == "2 need you · 2 working")
        #expect(roll.first == TerminalRef(hub: "h", id: 4), "the oldest wait opens first")
        let now = try #require(ISO8601DateFormatter().date(from: "2026-11-25T12:00:00Z"))
        let ago = { (m: Double) in ISO8601DateFormatter().string(from: now.addingTimeInterval(-m * 60)) }
        func line(_ f: (inout Terminal) -> Void, kids: Rollup? = nil) -> String {
            term { ($0.needsOwner, $0.subagents) = (false, 0); f(&$0) }.statusLine(kids, now: now)
        }
        #expect(line { ($0.state, $0.stateSince, $0.git) = (.working, ago(9), GitSummary(filesChanged: 5, insertions: 112, deletions: 19)) }
                == "Working 9m · 5 files (+112/−19)")
        #expect(line { ($0.state, $0.stateSince, $0.git) = (.idle, ago(14), GitSummary(filesChanged: 1, ahead: 2)) } == "Idle 14m · 2 commits ahead")
        #expect(line { ($0.state, $0.stateSince, $0.git) = (.idle, ago(14), GitSummary(filesChanged: 1, insertions: 3, ahead: 0)) } == "Idle 14m · 1 file (+3/−0)")
        #expect(line({ ($0.state, $0.askKind) = (.waiting, "done") }, kids: roll) == "Waiting on 2 children")
        #expect(line({ ($0.state, $0.needsOwner, $0.stateSince) = (.waiting, true, ago(2)) }, kids: roll) == "Waiting for you 2m")
        #expect(line { ($0.state, $0.stateSince) = (.working, ago(1)) } == "Working 1m", "an older hub: no git part")
        #expect(line({ ($0.state, $0.status) = (.waiting, "ended") }, kids: roll).hasPrefix("Ended"), "an ended parent never waits on children")
        #expect(line({ ($0.state, $0.askKind) = (.waiting, "error") }, kids: roll).hasPrefix("Stopped on an error"))
    }

    @Test func olderDetailWithoutTokensStillDecodes() throws {
        let d = try JSONDecoder().decode(TerminalDetail.self, from: Data(#"{"stats":{"dollars":0,"tool_calls":1,"tool_errors":0,"prompts":2}}"#.utf8))
        #expect(d.stats?.tokens == nil && d.stats?.prompts == 2)
    }
}
