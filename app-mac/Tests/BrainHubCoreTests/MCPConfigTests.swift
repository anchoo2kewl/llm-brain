import Foundation
import Testing
import BrainHubCore

@Suite struct MCPConfigTests {
    @Test func configsNameTheBundledBridgeAndHub() throws {
        let c = MCPConfig(command: "/Applications/Brain Hub.app/Contents/Resources/brain-mcp", hub: "https://hub.example.ts.net")
        let obj = try JSONSerialization.jsonObject(with: Data(c.json.utf8)) as? [String: Any]
        let brain = (obj?["mcpServers"] as? [String: Any])?["brain"] as? [String: Any]
        #expect(brain?["command"] as? String == c.command)
        #expect((brain?["env"] as? [String: String])?["BRAIN_HUB_URL"] == c.hub)
        #expect(c.claudeCode.hasSuffix("-- '/Applications/Brain Hub.app/Contents/Resources/brain-mcp'"))
        #expect(c.codex.contains(#"command = "/Applications/Brain Hub.app/Contents/Resources/brain-mcp""#))
    }
}
