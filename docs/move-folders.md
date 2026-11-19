# Optional folders for session moves

Moving or cloning a session transfers its conversation context. You can optionally choose an existing code checkout on the destination machine. Its path can differ from the source. Code files, uncommitted changes, and running processes are not copied.

In the Mac app, Move/Clone asks for an optional destination path and offers to pin it for future moves from that source folder to that machine. Leave it blank for the pinned mapping or the existing automatic behavior. Choose “Use home folder instead” to bypass the mapping. The web/Linux workspace offers the same path and pin options.

A selected or pinned path must exist and pass the destination agent's folder checks. An invalid path fails before the destination starts and leaves the source running. Moves without a selected or pinned path retain the existing home-folder fallback. The source ends only after the resumed destination passes verification.

## MCP

`session_move` accepts:

| Argument | Behavior |
| --- | --- |
| `id`, `to`, optional `hub` | Source terminal and destination machine; ids belong to the named hub. |
| `mode` | `move` (default) or `clone`. |
| `destination_path` | Optional absolute or `~/...` existing destination folder. Overrides a pin for this move. |
| `pin_path` | Save the supplied path for future moves from this source folder to this destination machine. |
| `without_folder` | Resume in the destination home folder, bypassing pins. Cannot be combined with a selected path or pin. |

`move_folders` manages saved mappings on the source hub:

```json
{"action":"list","hub":"Studio"}
```

```json
{"action":"set","from_host":"Laptop","source_path":"~/code/app","to":"Studio","destination_path":"~/projects/app"}
```

```json
{"action":"remove","from_host":"Laptop","source_path":"~/code/app","to":"Studio"}
```

Mappings are exact source-folder/machine pairs, normalized across home-folder prefixes. They persist in the hub database. Setting a mapping does not create directories; its destination is verified when used.

```json
{"id":12,"to":"Studio","destination_path":"~/projects/app","pin_path":true}
```

Pass the returned job to `move_status`. Its `destination_path` reports the resolved directory; phases show copy, resume, verification and source close. Reconnect MCP clients after updating the bridge to refresh their tool schemas.

REST endpoints: `GET /api/move-folders`, `POST /api/move-folders` (`action: set|remove`), and the existing terminal migration endpoint with the same move options. Older hubs that cannot confirm folder support are rejected when a folder option is requested.
