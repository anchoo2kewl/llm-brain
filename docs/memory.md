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
 "author": {"agent": "codex", "provider": "codex", "host": "studio"}}
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

## TaskAI copy

Every memory is also one page in a TaskAI project, so people can read and search it there.

- Project: `--memory-project` (default "Agent memory"), found or created on first use. A number is taken as a project id.
- Server: `--taskai` (default `https://taskai.cc`). `''` keeps memories on the hub.
- Key: `TASKAI_API_KEY` (or `TASKAI_X_API_KEY`) in `~/.config/llm-brain/taskai.env`, or in `secrets.env` next to it. With no key, memories stay on the hub.

The hub is the outbox. A write lands on the hub first and is forwarded with retries, waiting 30 s and doubling up to an hour, so a write never fails because TaskAI is slow or down. A superseded memory's page is retitled `[superseded]`.

## Importing Claude's memories

```sh
hub memory import [--to http://<hub>:8787] [--dir ~/.claude/projects] [--host NAME] [-v]
```

Run it on the Mac that has the files. It reads `~/.claude/projects/*/memory/*.md` (one fact per file, with name, description and type front matter) and posts each one to the hub. It prints how many files were new, updated, unchanged, refused and failed; `-v` lists the refused ones.

It is safe to run again: an unchanged file is a no-op, an edited file supersedes its earlier import, and a file an agent has since improved is left alone.
