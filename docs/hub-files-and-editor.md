# Hub files and editor

Move files between your Macs and the hub, keep copies of what agents write, and edit a folder in the browser.

All of this goes through each Mac's session agent, so it needs `--agent` on (the default).

## Browse a Mac

`/browse` opens any folder in your home folder on a Mac whose agent is live. From there you can:

- **Download** a file to your browser. The hub keeps a copy.
- **Keep** a file: copy it to the hub without downloading it.
- **Send files to this Mac…**: upload files.

Folders that hold keys are refused: `.ssh`, `.aws`, `.gnupg`, `.kube`, `.docker`, `.azure`, `.config/llm-brain`, `.config/gh`, `.config/gcloud`, `.config/op`, `.config/elastio`, `.codex`, `.claude`, `.password-store`, `Library/Keychains` and 1Password's Application Support folder.

## Drop files on a terminal or task

Drop or paste files onto a terminal, a new terminal's first message, or a task. Each file is uploaded to that Mac under `<state>/uploads/<dir>` (outside every repo, so it never lands in a commit), the hub keeps a copy, and the path is handed to the CLI. The limit is 200 MB per file.

Model chats take attachments too, but those are stored on the hub and sent with the message ([Tasks and chats](hub-tasks-and-chats.md#model-chats)).

## Kept files

`/files` lists the hub's own copy of every file you downloaded, kept or dropped in. They stay after the Mac cleans up. Files are stored by content hash in `<state>/assets/`. Search by name, download, or view the ones a browser can show.

### Documents sessions write

Once a minute the hub looks at what sessions wrote and queues a copy of each document: `md`, `pdf`, `csv`, `tsv`, `txt`, `html`, images, Office files, `json`, `zip` and `log`. Source code is skipped since it lives in git. An offline Mac holds its queue until it is back; a failed copy is retried with a growing wait, up to 6 hours.

## Editor

`/edit?host=<mac>&root=<folder>` opens Monaco on a folder of a Mac. A terminal page has an **Editor** button that opens it on the terminal's folder, inline or in its own tab, and so does the Workspace.

Files go over a live websocket to that Mac's agent (`/fs/ws`), fast enough to type against. Every path stays inside the root folder and outside the key folders above.

- Edits autosave.
- A file changed on disk reloads. If you have unsaved edits, it asks first. A write against a stale copy is reported as a conflict.
- Binary files are not opened as text.

It is laid out and keyed like Sublime Text:

| Keys | Action |
| --- | --- |
| ⌘P / ⌘T | Goto Anything: `file:line`, `file@symbol`, `:line`, `@symbol`, `#text`, `>command` |
| ⌘⇧P | Command palette |
| ⌘⇧F | Find in files |
| ⌘R / ⌘⇧R | Goto symbol / in project |
| ⌃G | Goto line |
| ⌘K ⌘B | Toggle the side bar (open files and folders, resizable) |
| ⌘⌥2 | Two columns |
| ⌘1…9 | Switch to open file N |
| ⌘S / ⌘⌥S | Save / save all |
| ⌃N / ⌃W / ⌃⇧T | New file / close file / reopen closed file |
| ⌃⌘F / ⌃⌘⇧F | Full screen / distraction free |
| ⌘K ⌘S | Show every shortcut |

Selection, find, line, bookmark and folding keys follow Sublime; the full list is in the editor's shortcut sheet. On other platforms ⌘ is Ctrl.

## Endpoints

| Endpoint | Does |
| --- | --- |
| `GET /hosts/{host}/ls` | List a folder. |
| `GET /hosts/{host}/download` | Download a file (the hub keeps a copy). |
| `POST /hosts/{host}/upload` | Upload a file to that Mac. |
| `POST /hosts/{host}/keep` | Copy a file to the hub without downloading. |
| `POST /llms/upload` | Attach a file to a model chat. |
| `GET /files`, `GET /files/{id}` | Kept files, and one of them. |
| `GET /browse` | The browser page. |
| `GET /edit` | The editor. |
| `GET /fs/ws` | The editor's file channel (websocket). |

The `/hosts/…` GETs are refused from another site too, so another site's link can't make a Mac send a file.
