# Shared memory

One memory every agent reads before it works and writes to as it learns, so a Codex worker doesn't rediscover what a Claude session found yesterday. It also tells an agent who else is working on the same thing right now.

## What a memory is

A lesson, not a transcript. Each has a kind, a short title, a body and a scope.

| Kind | For | Expires |
| --- | --- | --- |
| `fact` | Something true about the code or system | 180 days |
| `decision` | A choice and why | Never |
| `gotcha` | A trap and how to avoid it | Never |
| `runbook` | How to run or test something | Never |
| `handoff` | Where you stopped and what's next | 14 days |

A write can set `expires_days` (`-1` means never). Expired and superseded memories drop out of context packs.

The scope is a repo (`owner/name`) and optionally an issue, a PR and files, or a folder. At least one of repo or folder is needed; issue and PR need a repo.

## Write

```sh
bin/brain-remember gotcha "RDS refuses IAM tokens on replication" \
  "Use the password user for the replica slot." --issue 12
```

```
brain-remember <kind> <title> <body> [--issue N] [--pr N] [--files a,b] [--confidence low|medium|high] [--repo owner/name]
```

The repo defaults to the cwd's GitHub origin (or `$BRAIN_REPO`), the issue and PR to `$BRAIN_ISSUE` and `$BRAIN_PR`, and the folder to the git top level. The author is `$BRAIN_AGENT` (or `claude-code` inside Claude Code), `$BRAIN_PROVIDER`, the host and `$BRAIN_SESSION`. It prints `memory <id>: <outcome>` and what it replaced.

Or `POST /api/memory` with JSON:

```json
{"kind": "gotcha", "title": "…", "body": "…", "confidence": "high",
 "scope": {"repo": "acme/api", "issue": 12, "files": ["db/replica.go"]},
 "author": {"agent": "codex", "provider": "codex", "host": "desk-1"}}
```

The hub checks every write:

- The kind and scope are valid. The title is at most 140 bytes, the body 2,000, and at most 20 files.
- Nothing looks like a credential: key prefixes, `password=…`, tokens in URLs, long random strings. Such a write is refused, and the refusal never repeats the value. "The key is in 1Password item X" is fine.
- An exact repeat in the same scope is a no-op.
- A near-duplicate (70% of the same words) in the same scope supersedes the older memory instead of adding a second one.

## Read

```sh
bin/brain-context                         # this repo, issue (from the branch) and folder
bin/brain-context --issue 12 --files api/router.go
bin/brain-context >> AGENTS.md
```

```
brain-context [--repo owner/name] [--issue N] [--pr N] [--cwd DIR] [--files a,b] [--q words] [--k N] [--json]
```

It prints the context pack as markdown (`--json` for JSON). The issue comes from `$BRAIN_ISSUE` or `$BRAIN_PR`, else the branch name (`17446-fix`, `issue-12`, `gh-12`). It fails open: no hub, no memory or a slow answer (3 s) prints nothing and exits 0. It never blocks a run.

The same over HTTP:

```
GET /api/memory/context?repo=&issue=&pr=&folder=&files=&q=&k=          JSON
GET /api/memory/context?…&format=md                                   markdown
```

### Ranking

Memories outside the request's scope are left out. The rest are ranked by scope (this issue, then this PR, the same files, this folder, this repo), then kind, confidence, words in common with `q`, and age (half-life 60 days). A memory that names a file, flag or function is marked "verify before use".

Packs are scored in memory on the hub, so they take a few ms. TaskAI's search is merged in when it answers within 150 ms; otherwise its answer is cached for the next pack.

## Who else is on it

The pack also lists who is working on the same issue, PR, folder or repo right now: live Claude/Codex sessions on any Mac, running fleet workers, and open hub terminals. Repos are matched by name, ignoring `-wt-<name>` worktree suffixes. Your own session (`self=`, from `$BRAIN_SESSION`) is left out.

```
GET /api/presence?repo=&issue=&cwd=
```

`/memory` in the hub shows who is working now and the recent memories.

## In fleet workers

With `memory.enabled: true`, `bin/llm-run` adds to every worker prompt:

- the context pack (`memory.k` memories, default 8) for the worker's repo, issue or PR and folder;
- an instruction to save lessons with `brain-remember`, never a secret, and a handoff if it stops early.

It fails open: with no hub, the prompt is unchanged.

## Between hubs

Each Hub keeps its own memory database. There is no distributed database: a write always lands on the Hub you talk to, so a laptop that is offline keeps writing.

Every minute, each Hub asks the other Hubs of its cluster (the peers in `cluster.peers`) for the memories written on them since the newest one it holds from each (`GET /api/cluster/memories?after=<id>`). A Hub that was offline catches up when it is back. Replacements follow, the same lesson written on two Hubs is kept once, and every row is checked again for credentials before it is stored. This ships in hub 0.1.8. From 0.1.10 a Hub without a TaskAI API key neither pulls nor serves memories ([see below](#shared-memory-needs-a-taskai-api-key)).

## At session start

Every Hub terminal (Claude Code and Codex; Antigravity when it starts with a first prompt) gets the context pack for its folder, who else is working there, and how to search and save, in the CLI's instructions rather than as a message. From 0.1.10 there is no such note while the Hub's shared memory is off. The Hub also imports this machine's Claude Code memory files every hour (see below).

## TaskAI

[TaskAI](https://taskai.cc) holds a copy of the memories that people can read and search.

### Shared memory needs a TaskAI API key

From hub 0.1.10 ([release notes](https://github.com/anchoo2kewl/llm-brain/releases/tag/hub-v0.1.10)), shared memory is on only while the Hub has a TaskAI API key. Without one it is off: no context pack, no `memory_write`, no copying between Hubs, no import of Claude's memory files, and no shared-memory note in new sessions. The Hub's Settings page, its Memory and MCP pages, and `GET /api/memory/status` say "Add a TaskAI API key to enable shared memory". Earlier Hubs ran shared memory without a key and only skipped the TaskAI copy.

Ways to set the key, highest precedence first. The first source that holds a key decides; if TaskAI refuses that key, memory is off with an error instead of falling back to a lower source.

| Source | How | Without a restart? |
|---|---|---|
| The Hub's key store, `~/.config/llm-brain/taskai.key` (mode 0600) | The Hub's **Settings → Shared memory** page; the desktop app's **Settings → Memory** (0.1.11), on one Hub or every Hub in the cluster; or `hub memory key set < key.txt` (reads stdin; also `status`, `remove`, and `--to URL` for another Hub) | Yes, at once |
| `hub serve --taskai-key-file PATH` | A file (mode 0600) holding the key, or `TASKAI_API_KEY=…` | Yes, within 15 s of the file changing |
| The `TASKAI_API_KEY` environment variable | For example the launchd plist's `EnvironmentVariables` | No: restart the Hub |
| `~/.config/llm-brain/taskai.env`, then `secrets.env` next to it | `TASKAI_API_KEY` (or `TASKAI_X_API_KEY`), as before 0.1.10 | Yes, within 15 s |

A new key is checked with TaskAI before it is saved. A refused key is not saved. If TaskAI does not answer, the key is saved, memory is on, and memories wait on the Hub until TaskAI answers. Removing the stored key hands over to a key file or environment key when one exists, and the status names the source in use. The key is never logged, never passed in command arguments, and never shown after saving. Memories written while a Hub had no key are sent once it has one.

### Today: one page per memory

- Protocol: `--taskai-protocol wiki`, the default. Each memory is one page in a TaskAI project.
- Project: `--memory-project` (default "Agent memory"), found or created on first use. A number is taken as a project id.
- Server: `--taskai` (default `https://taskai.cc`). `''` turns shared memory off.

The Hub is the outbox. A write lands on the Hub first and is forwarded with retries, waiting 30 s and doubling up to an hour, so a write never fails because TaskAI is slow or down. A superseded memory's page is retitled `[superseded]`. TaskAI's search is merged into context packs when it answers within 150 ms.

### Next: the TaskAI Memory Protocol

[biswas-dev/taskai#50](https://github.com/biswas-dev/taskai/pull/50) makes memories an open, versioned protocol (v1) that any app or LLM can use over REST or MCP. The Hub is one client among many. It is not live yet. Hub 0.1.10 has a client for it, but it stays off: the Hub keeps the page copy (`wiki`) until #50 ships, and then switches with `--taskai-protocol 1`. With protocol 1, each memory is written to `/api/memories` with the Hub's node name as `origin_host` and its Hub id as `origin_id`, so retries and replicated Hubs update one record. Existing pages move with `POST /api/memories/import-wiki`, using the same `origin_host`.

- **Per user.** Memories belong to a user and their API keys, private by default. An owner can share their whole store read-only with chosen collaborators.
- **Hybrid search.** Full text plus vector search, merged with reciprocal rank fusion; full text alone when the embedding is slow.
- **Quota.** 1 GiB per user. Writes warn at 80% and are refused past the quota, after one compaction.
- **Compaction.** Deterministic: drops superseded and expired memories, folds exact copies, and merges old low-confidence notes. Near-duplicates are left alone, because a similar-looking note can say the opposite; replacing a lesson is an explicit supersede.
- **Idempotent from many hubs.** Each write carries its origin hub and id, so replicated Hubs that send the same memory update one row.

## Importing Claude's memories

```sh
hub memory import [--to http://<hub>:8787] [--dir ~/.claude/projects] [--host NAME] [-v]
```

Run it on the Mac that has the files. It reads `~/.claude/projects/*/memory/*.md` (one fact per file, with name, description and type front matter) and posts each one to the hub. It prints how many files were new, updated, unchanged, refused and failed; `-v` lists the refused ones.

`hub serve` runs the same import every hour. It is safe to run again: an unchanged file is a no-op, an edited file supersedes its earlier import, and a file an agent has since improved is left alone.
