# llm-brain

Anshuman's agent fleet: it takes issues Naj and Greg assign, ships them to merged PRs, and reviews the PRs that request Anshuman.
Skills, memory and settings live in [claude-backup](https://github.com/anchoo2kewl/claude-backup) (two-way synced across machines). This repo is only the fleet.

## Components

| | What | Where |
|---|---|---|
| 1 | **LLM management.** Which LLM implements and which reviews; multiple accounts per provider, personal first | `config/fleet.yaml` `providers` / `roles`, `bin/llm-run` (gateway TBD) |
| 2 | **Issue → PR → merge.** Opt-in label `agent-ready` on issues assigned to me by Naj, Greg or me | `fleet dispatch` → `prompts/implement.md`; `fleet babysit` → `prompts/babysit.md` |
| 3 | **PR reviewer.** Every PR in the elastio blue-stack family that requests or mentions me; re-reviews on every new head commit until it approves | `fleet review-prs` → `prompts/pr-review.md` + `bin/review-rules` |
| 4 | **Hub.** One UI for every agent running now, every fleet run, and every Claude/Codex conversation on every machine, with full history | `hub/` → `bin/hub serve` (Studio, port 8787), `bin/hub ship` (laptops) |

`launchd/com.llm-brain.fleet.plist` runs `fleet tick` (all three loops) every 5 minutes on each machine.

## LLM roles
`roles.implementer` does the work. `roles.reviewer` does the internal cross-check, and is always a **different** provider when one is available; only with a single provider does it review itself, in a fresh session. `bin/llm-run` is the only vendor-specific code (claude, codex, gemini).

## Two machines, no collisions
- An issue is claimed with an `llm-brain:claim host=<host>` comment; the earliest claim wins.
- Fleet PRs carry `<!-- llm-brain:issue=N -->` in the body; the babysitter finds them by that marker.
- Reviews are tracked per head commit in `~/.local/state/llm-brain/prs/*.reviewed`.

## Review rules: plug-and-play modules, union semantics
`pr_review.rule_modules` lists the modules. Every matching rule from **every** module applies; this differs from open-code-review's first-match behaviour.
- `ocr`: [alibaba/open-code-review](https://github.com/alibaba/open-code-review) system rules per language, plus the reviewed repo's own `.opencodereview/rule.json`. Used in delegation mode: OCR picks the files and rules, our LLM does the review.
- `review-rules/<name>/rule.json`: local modules in OCR rule format (`elastio` house rules, `greg` Greg's recurring checks).
- `{name, git, path}`: any git repo with an OCR-format `rule.json`.

`bin/review-rules <files…>` prints the union for each file group.

Reviews are short (CodeRabbit/Greptile style). The agent approves when there are zero red (critical/high) items; otherwise it posts COMMENT, and it re-reviews after new commits.

## Hub (`hub/`)
A Go service with a server-rendered UI, storing everything in Turso (the Rust SQLite) at `~/.local/state/llm-brain/hub.db`. It replaced the Python `bin/dashboard`, on the same port. There is no login: it binds only to 127.0.0.1 and the machine's Tailscale IP.
- **Overview** shows everything the old dashboard showed, read from `status.json`. It also shows the agents running now on every host: fleet workers, plus interactive Claude/Codex sessions whose transcript changed in the last 2 minutes.
- **Runs**: every `logs/<key>.<ts>.log` with its prompt, live-tailing log, status, PR/issue link, and the worker's own conversation (Claude sessions named `fleet-<key>`).
- **Sessions**: every `~/.claude/projects/*/*.jsonl` (subagents too) and `~/.codex/sessions/**/*.jsonl` as a readable transcript. Thinking and tool calls are collapsible, and the page updates live.
- **Search**: LIKE over every message, tool input and tool output.

Ingestion is incremental and idempotent. Each file has a byte offset that advances in the same transaction as the rows it produced, and the hub re-scans every 10 s. Other machines run `hub ship --to http://100.74.108.114:8787` (`./bootstrap.sh --shipper`). It POSTs new bytes of the same files, and the hub stores them under that machine's `host`. Ship requests are accepted only from loopback and 100.64.0.0/10.
```
make -C hub build     # -> bin/hub (darwin/arm64, no CGO)
make -C hub check     # go vet + go test
bin/hub serve [--port 8787] [--db PATH] [--listen 127.0.0.1,...]
bin/hub ship --to http://100.74.108.114:8787 [--once]
```

## Commands
```
fleet tick | dispatch | babysit | review-prs | review-pr <url> | review <url> (internal) | status | labels | pause | resume | providers
```
Kill switches: `paused: true` in config (all machines), `fleet pause` (this machine), label `agent:stop` (one issue).

## New machine
```
git clone git@github.com:anchoo2kewl/llm-brain.git ~/projects/llm-brain && ~/projects/llm-brain/bootstrap.sh
```
A machine that should only feed the hub (the laptop): `~/projects/llm-brain/bootstrap.sh --shipper`. It loads `com.llm-brain.hub-ship` and nothing else.

## Hub inspection

The overview shows fleet issue capacity, resolved implement/check/review roles, and
expandable activity for every recorded provider/model across hosts. Model session
links filter to that model. Expanded panels survive the 15-second refresh.

Codex `token_count` quota observations are ingested from new rollout lines and
shipped through the existing transcript pipeline. The UI shows remaining percent,
window length, reset time, reporting host, and observation age. Readings older than
15 minutes are marked stale; a passed reset is shown as awaiting an update rather
than an assumed replenishment. Host readings may refer to the same account and
are never added together. Previously ingested lines are not replayed by this change.
Claude subscription balances and gateway account/request routing are not present
in the current inputs and are explicitly marked unavailable.

Run `make -C hub check` before committing hub changes. Push the change for deployment
to the Studio; deployment is performed separately.
