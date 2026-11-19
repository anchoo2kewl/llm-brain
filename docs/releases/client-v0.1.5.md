# Brain Hub desktop 0.1.5

- The Mac sidebar and move menu use the names you saved in Hubs, including hubs reached through a loopback address.
- Drag a terminal row or its pane grip onto a destination machine. Move follows the conversation across machines and shows the handoff phases. Clone keeps the original session open. The destination needs a compatible provider CLI and login. Local files and in-flight shell processes stay on their original machine.
- MCP clients can discover hubs and sessions, read a session, send input, rename or end it, move or clone a conversation, follow migration progress, and inspect or control supported workspace layouts. Open the MCP settings in the Mac app for connection instructions.
- The local macOS MCP tool `desktop_update` supports `status`, `check`, and `install`. Install verifies the stable release with Brain Hub’s bundled updater. Restart the desktop app to load the new version.
