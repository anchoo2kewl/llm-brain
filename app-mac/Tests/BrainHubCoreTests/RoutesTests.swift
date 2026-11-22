import Foundation
import Testing
import BrainHubCore

@Suite struct HubAddresses {
    @Test func savesAndLoadsOtherAddresses() throws {
        var c = HubConfig()
        try c.add(name: "Studio", url: "https://hub.tail1.ts.net")
        let got = try c.setAddresses("https://hub.tail1.ts.net", "http://100.74.0.9:8787, http://100.74.0.9:8787\nhttps://hub.tail1.ts.net")
        #expect(got == ["http://100.74.0.9:8787"]) // duplicates and the first address dropped
        #expect(c.hubs[0].all == ["https://hub.tail1.ts.net", "http://100.74.0.9:8787"])
        #expect(throws: HubError.self) { try c.setAddresses("https://hub.tail1.ts.net", "http://192.168.1.5") }
        try c.setAddresses("https://hub.tail1.ts.net", "")
        #expect(c.hubs[0].addresses == nil)

        let dir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        let path = dir.appendingPathComponent("hubs.json")
        try c.setAddresses("https://hub.tail1.ts.net", "http://100.74.0.9:8787")
        try c.save(path)
        #expect(HubConfig.load(path) == c)
        // A file written before addresses existed still loads.
        try Data(#"{"hubs":[{"name":"S","url":"http://127.0.0.1:8787"}],"selected":"http://127.0.0.1:8787"}"#.utf8).write(to: path)
        #expect(HubConfig.load(path).hubs.map(\.all) == [["http://127.0.0.1:8787"]])
    }

    @Test func picksTheFastestAnswerFromTheSameHub() {
        let a = (address: "https://hub.tail1.ts.net", node: "studio", seconds: 0.20)
        let b = (address: "http://100.74.0.9:8787", node: "studio", seconds: 0.02)
        let impostor = (address: "http://100.74.0.10:8787", node: "laptop", seconds: 0.001)
        #expect(HubRoutes.pick([a, b, impostor], first: a.address, expected: nil)?.address == b.address)
        #expect(HubRoutes.pick([a, b, impostor], first: a.address, expected: "studio")?.address == b.address)
        // The first address is down: the node seen before decides, so the impostor is still never used.
        #expect(HubRoutes.pick([b, impostor], first: a.address, expected: "studio")?.address == b.address)
        #expect(HubRoutes.pick([impostor], first: a.address, expected: "studio") == nil)
        #expect(HubRoutes.pick([], first: a.address, expected: nil) == nil)
    }

    @Test func resolvesAnyAddressOfAHub() throws {
        var c = HubConfig()
        try c.add(name: "Studio", url: "https://hub.tail1.ts.net")
        try c.setAddresses("https://hub.tail1.ts.net", "http://100.74.0.9:8787")
        let r = HubRoutes()
        r.configure(c.hubs)
        #expect(r.resolve("https://hub.tail1.ts.net") == "https://hub.tail1.ts.net") // nothing probed yet
        #expect(r.addresses("http://100.74.0.9:8787") == ["https://hub.tail1.ts.net", "http://100.74.0.9:8787"])
        #expect(r.resolve("http://127.0.0.1:9") == "http://127.0.0.1:9") // not a saved hub
        r.learn("https://hub.tail1.ts.net", node: "studio")
        r.alias("http://100.118.0.4:8787", toNode: "studio") // a peer's advertised address
        #expect(r.addresses("http://100.118.0.4:8787").first == "https://hub.tail1.ts.net")
    }
}
