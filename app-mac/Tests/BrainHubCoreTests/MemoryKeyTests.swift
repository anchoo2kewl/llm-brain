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
        #expect(MemoryKey.clean(String(repeating: "k", count: 512)) != nil && MemoryKey.clean("12345678") != nil && MemoryKey.clean("1234567") == nil)
        #expect(MemoryKey.clean("abcd\tefgh") == nil && MemoryKey.clean("          ") == nil)
    }

    @Test func decodesTheHubsStatus() throws {
        let on = try JSONDecoder().decode(MemoryStatus.self, from: Data(#"{"enabled":true,"source":"settings","message":"Shared memory is on (TaskAI key from settings)","protocol":"wiki","settable":true}"#.utf8))
        #expect(on.enabled && on.source == "settings")
        let off = try JSONDecoder().decode(MemoryStatus.self, from: Data(#"{"enabled":false,"message":"Add a TaskAI API key to enable shared memory"}"#.utf8))
        #expect(!off.enabled && off.message == "Add a TaskAI API key to enable shared memory" && off.error == nil)
    }

    @Test func explainsAnOldHub() {
        #expect(MemoryKey.explain(HubError(HubClient.failure(status: 404, body: Data("404 page not found".utf8)))).contains("hub 0.1.10"))
        let refused = HubClient.failure(status: 422, body: Data(#"{"error":"TaskAI rejected the API key from settings (HTTP 401); it was not saved"}"#.utf8))
        #expect(MemoryKey.explain(HubError(refused)) == "The hub answered 422: TaskAI rejected the API key from settings (HTTP 401); it was not saved")
    }

    @Test func everyHubIncludesTheClustersUnsavedPeers() throws {
        // laptop twice (one address kept), studio saved already, down not answering, odd not at a hub address.
        let json = #"""
        {"self":{"node":"studio"},"peers":[
         {"url":"http://100.64.0.2:8787","info":{"node":"laptop"},"ok":true},
         {"url":"http://100.64.0.3:8787","info":{"node":"studio"},"ok":true},
         {"url":"http://100.64.0.4:8787","info":{"node":"down"},"ok":false},
         {"url":"https://evil.example","info":{"node":"odd"},"ok":true},
         {"url":"http://100.64.0.5:8787","info":{"node":"laptop"},"ok":true}]}
        """#
        let cluster = try JSONDecoder().decode(ClusterNodes.self, from: Data(json.utf8))
        let out = MemoryKey.clusterPeers(cluster.peers, savedNodes: ["studio"])
        #expect(out.map(\.node) == ["laptop"] && out.map(\.url) == ["http://100.64.0.2:8787"])
    }

    @Test func appliesToEveryHubAndReportsEach() async {
        let hubs = ["http://127.0.0.1:8787", "http://100.64.0.2:8787", "http://100.64.0.3:8787"]
        let out = await MemoryKey.apply(to: hubs) { hub in
            if hub.contains("0.2") { throw HubError("Hub unreachable: timed out") }
            if hub.contains("0.3") { throw HubError(HubClient.failure(status: 404, body: Data())) }
            return MemoryStatus(enabled: true, source: "settings", message: "Shared memory is on (TaskAI key from settings)")
        }
        #expect(out.map(\.hub) == hubs)
        #expect((try? out[0].result.get())?.enabled == true)
        #expect(out[1].result == .failure(HubError("Hub unreachable: timed out")))
        if case .failure(let e) = out[2].result { #expect(e.message.contains("0.1.10")) } else { Issue.record("old hub") }
    }
}
