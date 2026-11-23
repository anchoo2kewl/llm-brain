import Foundation
import Testing
import BrainHubCore

@Suite struct MemoryKeyTests {
    @Test func cleansTheTypedKey() {
        #expect(MemoryKey.clean("  taskai-key-123\n") == "taskai-key-123")
        #expect(MemoryKey.clean("") == nil)
        #expect(MemoryKey.clean("short") == nil)
        #expect(MemoryKey.clean("two words here") == nil)
        #expect(MemoryKey.clean(String(repeating: "k", count: 513)) == nil)
    }

    @Test func decodesTheHubsStatus() throws {
        let on = try JSONDecoder().decode(MemoryStatus.self, from: Data(#"{"enabled":true,"source":"settings","message":"Shared memory is on (TaskAI key from settings)","protocol":"wiki","settable":true}"#.utf8))
        #expect(on.enabled && on.source == "settings" && on.settable == true)
        let off = try JSONDecoder().decode(MemoryStatus.self, from: Data(#"{"enabled":false,"message":"Add a TaskAI API key to enable shared memory","settable":true}"#.utf8))
        #expect(!off.enabled && off.message == "Add a TaskAI API key to enable shared memory" && off.error == nil)
    }

    @Test func explainsAnOldHub() {
        #expect(MemoryKey.explain(HubError(HubClient.failure(status: 404, body: Data("404 page not found".utf8)))).contains("hub 0.1.10"))
        let refused = HubClient.failure(status: 422, body: Data(#"{"error":"TaskAI rejected the API key from settings (HTTP 401); it was not saved"}"#.utf8))
        #expect(MemoryKey.explain(HubError(refused)) == "The hub answered 422: TaskAI rejected the API key from settings (HTTP 401); it was not saved")
    }

    @Test func appliesToEveryHubAndReportsEach() async {
        let hubs = ["http://127.0.0.1:8787", "http://100.64.0.2:8787", "http://100.64.0.3:8787"]
        let out = await MemoryKey.apply(to: hubs) { hub in
            if hub.contains("0.2") { throw HubError("Hub unreachable: timed out") }
            if hub.contains("0.3") { throw HubError(HubClient.failure(status: 404, body: Data())) }
            return MemoryStatus(enabled: true, source: "settings", message: "Shared memory is on (TaskAI key from settings)")
        }
        #expect(out.map(\.hub) == hubs)
        #expect(out[0].status?.enabled == true && out[0].error == nil)
        #expect(out[1].status == nil && out[1].error == "Hub unreachable: timed out")
        #expect(out[2].error?.contains("0.1.10") == true)
    }
}
