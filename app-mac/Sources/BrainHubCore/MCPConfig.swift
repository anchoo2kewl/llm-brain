import Foundation

/// How an MCP client (Claude Code, Codex, Claude Desktop, Cursor, any stdio client) starts the Brain MCP bridge for a
/// hub. The bridge ships in the app (Contents/Resources/brain-mcp, needs python3) and reaches the hub over the
/// tailnet; the hub URL is the only setting and holds no credential.
public struct MCPConfig: Sendable, Equatable {
    public var command: String
    public var hub: String
    public init(command: String, hub: String) { (self.command, self.hub) = (command, hub) }

    /// The `mcpServers` JSON most clients read (Claude Desktop, Cursor, Claude Code's .mcp.json).
    public var json: String {
        let obj: [String: Any] = ["mcpServers": ["brain": ["type": "stdio", "command": command, "args": [String](), "env": ["BRAIN_HUB_URL": hub]]]]
        let data = (try? JSONSerialization.data(withJSONObject: obj, options: [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes])) ?? Data()
        return String(decoding: data, as: UTF8.self)
    }

    /// One command for Claude Code.
    public var claudeCode: String { "claude mcp add brain --scope user --env BRAIN_HUB_URL=\(hub) -- \(Self.shellQuote(command))" }

    /// Codex's ~/.codex/config.toml.
    public var codex: String {
        "[mcp_servers.brain]\ncommand = \(Self.tomlQuote(command))\nenv = { BRAIN_HUB_URL = \(Self.tomlQuote(hub)) }"
    }

    static func shellQuote(_ s: String) -> String { "'" + s.replacingOccurrences(of: "'", with: "'\\''") + "'" }
    static func tomlQuote(_ s: String) -> String { "\"" + s.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "\"", with: "\\\"") + "\"" }
}
