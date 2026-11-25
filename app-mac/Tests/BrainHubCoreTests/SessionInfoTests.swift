import Foundation
import Testing
import BrainHubCore

// The same cases as tests/wslist.test.mjs: the Mac and the web rows must say the same thing.
@Suite struct SessionInfoTests {
    @Test func modelShortNames() {
        let cases = ["claude-opus-5-5": "Opus 5.5", "claude-sonnet-4-5-20250929": "Sonnet 4.5", "claude-3-5-sonnet-20241022": "Sonnet 3.5",
                     "claude-opus-5-5[1m]": "Opus 5.5", "claude-fable-5-2": "Fable 5.2", "gpt-6.1-sol": "GPT-6.1-Sol", "gpt-5-codex": "GPT-5-Codex",
                     "gemini-4-carbon": "Gemini 4 Carbon", "gemini-2.5-pro": "Gemini 2.5 Pro", "claude-2.1": "Claude 2.1", "o3": "o3", "Custom Model": "Custom Model", "": ""]
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
    }

    @Test func detailRowsKeepTheirShape() throws {
        let now = try #require(ISO8601DateFormatter().date(from: "2026-11-25T12:00:00Z"))
        let before = term().detailRows(nil, now: now)
        #expect(before.map(\.label) == ["Context", "State", "Where", "Account", "Totals", "You", "Agent", "Started"])
        #expect(before[0].value == "92k of 200k · 46%")
        #expect(before[1].value == "Working · 3m · 2 subagents")
        #expect(before[2].value == "studio · app · feat/x" && before[2].help == "studio · /work/app · feat/x")
        #expect(before[3].value == "default login")
        #expect(before[5].value == "…")
        #expect(before[7].value == "2h ago")
        let json = #"{"tool":"Bash { \"command\": \"ls\" }","last_prompt":"Fix the\nbuild","last_reply":"","stats":{"dollars":0.589,"#
            + #""tokens":{"input":32,"output":3770,"cache_read":1110178,"cache_write":58295},"prompts":1,"tool_calls":11,"tool_errors":1}}"#
        let d = try JSONDecoder().decode(TerminalDetail.self, from: Data(json.utf8))
        let after = term().detailRows(d, now: now)
        #expect(after.count == before.count)
        #expect(after[1].value == "Working · 3m · 2 subagents · Bash" && after[1].help == #"Bash { "command": "ls" }"#)
        #expect(after[4].value == "$0.59 · 1.2M tokens · 1 prompt · 11 tool calls (1 failed)")
        #expect(after[5].value == "Fix the build" && after[6].value == "–")
        let waits = term { ($0.state, $0.needsOwner, $0.branch, $0.contextUsed) = (.waiting, true, "main", nil) }.detailRows(d, now: now)
        #expect(waits[1].value.hasPrefix("Waiting for you · "))
        #expect(waits[0].value == "not reported")
        #expect(waits[2].value == "studio · app")
        let done = term { ($0.state, $0.askKind, $0.needsOwner) = (.waiting, "done", false) }.detailRows(d, now: now)
        #expect(done[1].value.hasPrefix("Finished its turn"))
        for status in ["ended", "failed"] {
            #expect(term { ($0.status, $0.needsOwner) = (status, true) }.detailRows(d, now: now)[1].value.hasPrefix("Ended"))
        }
    }

    @Test func olderDetailWithoutTokensStillDecodes() throws {
        let d = try JSONDecoder().decode(TerminalDetail.self, from: Data(#"{"stats":{"dollars":0,"tool_calls":1,"tool_errors":0,"prompts":2}}"#.utf8))
        #expect(d.stats?.tokens == nil && d.stats?.prompts == 2)
    }
}
