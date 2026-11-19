import Foundation
import Testing
import BrainHubCore

@Suite struct MigrationTests {
    @Test func dragTextRoundTrips() {
        let r = TerminalRef(hub: "http://100.64.0.1:8787", id: 42)
        #expect(TerminalRef(dragText: r.dragText) == r)
        #expect(TerminalRef(dragText: "no id") == nil)
        #expect(TerminalRef(dragText: "http://x#0") == nil)
    }

    @Test func decodesPhases() throws {
        let json = #"{"id":"j","terminal":7,"from":"MacBook","to":"Studio","dest_terminal":9,"done":false,"phases":[{"name":"check","state":"done","detail":"claude on MacBook → Studio"},{"name":"export","state":"running"},{"name":"copy","state":"pending"}]}"#
        let m = try JSONDecoder().decode(Migration.self, from: Data(json.utf8))
        #expect(m.dest == 9 && !m.done && m.error == nil)
        #expect(m.current?.name == "export")
        #expect(Migration.label("copy", to: "Studio") == "Copying it to Studio")
        #expect(Paths.migrate(7) == "/api/terminals/7/migrate")
    }
}
