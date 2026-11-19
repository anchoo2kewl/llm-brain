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

    @Test func decodesCloneAndWorkspaceRequests() throws {
        let m = try JSONDecoder().decode(Migration.self, from: Data(#"{"id":"j","to":"Studio","mode":"clone","done":true,"phases":[]}"#.utf8))
        #expect(m.isClone)
        let w = try JSONDecoder().decode(WorkspaceUpdate.self, from: Data(#"{"seq":9,"intents":[{"seq":7,"action":"focus","terminal":12},{"seq":8,"action":"zoom","client":"web:main"},{"seq":9,"action":"open","terminal":3,"client":"mac:1"}]}"#.utf8))
        #expect(w.pending(after: -1, kind: "mac").isEmpty) // first look: no replay
        #expect(w.pending(after: 6, kind: "mac").map(\.seq) == [7, 9])
        #expect(w.pending(after: 7, kind: "mac").map(\.terminal) == [3])
        #expect(Paths.workspace(since: 4) == "/api/workspace?since=4")
    }
}
