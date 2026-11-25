import Foundation
import Testing
import BrainHubCore

@Suite struct VersionsTests {
    @Test func comparesVersions() {
        #expect(Versions.isNewer("0.1.12", than: "0.1.11") && Versions.isNewer("0.2.0", than: "0.1.99") && Versions.isNewer("1.0.0", than: "0.9.9"))
        #expect(!Versions.isNewer("0.1.11", than: "0.1.11") && !Versions.isNewer("0.1.9", than: "0.1.10"))
        #expect(!Versions.isNewer("dev", than: "0.1.0") && !Versions.isNewer("0.1", than: "0.0.9") && !Versions.isNewer("0.1.12", than: "x"))
    }

    @Test func restartOnlyIntoANewerInstall() {
        #expect(Versions.restart(running: "0.1.11", installed: "0.1.12") == "0.1.12")
        #expect(Versions.restart(running: "0.1.12", installed: "0.1.12") == nil)
        #expect(Versions.restart(running: "0.1.12", installed: "0.1.11") == nil)
        #expect(Versions.restart(running: "0.1.11", installed: nil) == nil)
    }

    @Test func availableFromTheCheckLine() {
        #expect(Versions.available(inCheck: "Version 0.1.13 is available: https://github.com/anchoo2kewl/llm-brain/releases/tag/client-v0.1.13") == "0.1.13")
        #expect(Versions.available(inCheck: "You’re up to date.") == nil)
        #expect(Versions.available(inCheck: "Installed 0.1.13. Restart Brain Hub to use it.") == nil)
        #expect(Versions.available(inCheck: "Version 0.1 is available: x") == nil)
        #expect(Versions.available(inCheck: "Version is available") == nil)
    }

    @Test func scheduleWithAFakeClock() {
        let t0 = Date(timeIntervalSince1970: 1_000_000)
        #expect(UpdateSchedule.isDue(now: t0, lastCheck: nil))
        #expect(!UpdateSchedule.isDue(now: t0.addingTimeInterval(60), lastCheck: t0))
        #expect(!UpdateSchedule.isDue(now: t0.addingTimeInterval(3599), lastCheck: t0))
        #expect(UpdateSchedule.isDue(now: t0.addingTimeInterval(3600), lastCheck: t0))
        #expect(UpdateSchedule.isDue(now: t0, lastCheck: t0.addingTimeInterval(60)))
        #expect(!UpdateSchedule.isDue(now: t0.addingTimeInterval(299), lastCheck: t0, failed: true))
        #expect(UpdateSchedule.isDue(now: t0.addingTimeInterval(300), lastCheck: t0, failed: true))
    }

    @Test func hubNotes() {
        func reply(_ s: String) -> HubVersions.Reply { try! JSONDecoder().decode(HubVersions.Reply.self, from: Data(s.utf8)) }
        let health = reply(#"{"version":"0.1.11","ready":true}"#)
        func v(_ status: String?) -> HubVersions { HubVersions(health: health, status: status.map(reply)) }
        #expect(v(#"{"state":"up_to_date","version":"0.1.11"}"#).note == "Hub 0.1.11")
        #expect(v(#"{"version":"0.1.12","latest":"0.1.12"}"#).note == "Hub 0.1.11 · 0.1.12 installed, restart pending")
        #expect(v(#"{"state":"available","version":"0.1.11","latest":"0.1.13"}"#).note == "Hub 0.1.11 · 0.1.13 available")
        #expect(v(nil).note == "Hub 0.1.11" && !v(nil).needsAttention)
        #expect(HubVersions(health: nil, status: nil).note == "Hub version unknown")
        #expect(HubVersions(health: reply(#"{"version":"dev"}"#), status: nil).note == "Hub version unknown")
    }
}

@Suite struct RecencyTests {
    let now = Date(timeIntervalSince1970: 1_000_000)

    @Test func shortTimes() {
        #expect(Recency.short(now.addingTimeInterval(-5), now: now) == "now")
        #expect(Recency.short(now.addingTimeInterval(-125), now: now) == "2m")
        #expect(Recency.short(now.addingTimeInterval(-3700), now: now) == "1h")
        #expect(Recency.short(now.addingTimeInterval(-3 * 86400 - 10), now: now) == "3d")
        #expect(Recency.short(now.addingTimeInterval(30), now: now) == "now" && Recency.short(nil, now: now) == "")
        #expect(Recency.dotOpacity(now, now: now) == 1 && Recency.dotOpacity(now.addingTimeInterval(-90000), now: now) == 0.4)
    }

    @Test func laterOfActivityAndInput() {
        var t = Terminal(id: 1, title: "", host: "m", cli: "x", provider: "x", cwd: "~", status: "live", updated: "2026-01-01T00:00:00Z")
        #expect(t.lastActive == ISO8601DateFormatter().date(from: "2026-01-01T00:00:00Z"))
        (t.lastActivity, t.lastInput) = ("2026-01-02T00:00:00Z", "2026-01-03T00:00:00.5Z")
        #expect(Recency.short(t.lastActive, now: ISO8601DateFormatter().date(from: "2026-01-03T00:02:30Z")!) == "2m")
    }

    @Test func onlyNeedsYouPulses() {
        func t(_ s: AgentState?, _ kind: String?) -> Terminal {
            Terminal(id: 1, title: "", host: "m", cli: "x", provider: "x", cwd: "~", status: "live", updated: "1", state: s, askKind: kind)
        }
        #expect(Recency.pulses(t(.waiting, "question"), opened: false))
        #expect(Recency.pulses(t(.blocked, nil), opened: false))
        #expect(!Recency.pulses(t(.waiting, "error"), opened: false) && !Recency.pulses(t(.waiting, nil), opened: false))
        var hub = t(.waiting, "done")
        hub.needsOwner = true
        #expect(Recency.pulses(hub, opened: false), "the hub's needs_owner decides")
        hub = t(.waiting, "question")
        hub.needsOwner = false
        #expect(!Recency.pulses(hub, opened: false))
        #expect(!Recency.pulses(t(.waiting, "done"), opened: false), "a finished turn is calm")
        #expect(!Recency.pulses(t(.working, nil), opened: false) && !Recency.pulses(t(.idle, nil), opened: false) && !Recency.pulses(t(nil, nil), opened: false))
        #expect(!Recency.pulses(t(.waiting, "question"), opened: true), "stops once opened")
    }
}
