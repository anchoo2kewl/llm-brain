# llm-brain

A small fleet of coding agents that works for one person:

- **Picks up issues** your teammates assign you, builds them, opens PRs, answers review comments, and merges when CI and a reviewer say yes.
- **Reviews PRs** that request you, every 5 minutes, with short notes that lead with security (OWASP Top 10) and performance. It approves when nothing is blocking and re-reviews after every new commit.
- **Shares your LLM subscriptions** through one gateway: personal accounts first, work accounts only when personal runs out.
- **Shows everything in one place**: a web hub with every agent running now, every run, and every Claude/Codex conversation on every machine, with fast search.

It runs on a Mac you leave on (launchd), with other machines shipping their sessions to it. There is no login; it's meant to be reached over Tailscale only.

```
issue assigned ──► fleet dispatch ──► worker (claude/codex) ──► PR ──► internal review (other LLM)
                                                                 │
review comments / CI ──► fleet babysit ──► fix, reply ──► merge ◄─┘
PR requests you ──► fleet review-prs ──► rule modules (OWASP, perf, OCR, yours) ──► review
every LLM call ──► account gateway (personal → work) ──► hub (quota, model, account per call)
```

## Quick start

```
git clone https://github.com/anchoo2kewl/llm-brain ~/projects/llm-brain
mkdir -p ~/projects/llm-brain-overlay && cp -R ~/projects/llm-brain/examples/overlay/. ~/projects/llm-brain-overlay/
$EDITOR ~/projects/llm-brain-overlay/config.yaml      # who you are, who hands you work, which repos
~/projects/llm-brain/bootstrap.sh                     # tools, hub, fleet loop (launchd)
```
Then open `http://<this-mac>:8787`. A laptop that should only send its sessions to the hub: `bootstrap.sh --shipper`.

## Overlay: your private settings

The repo holds only general defaults. Everything about *you* goes in an overlay directory, which you keep private:

```
llm-brain-overlay/
  config.yaml              # owner, people, repos, orgs to watch, extra rule modules (merged over config/fleet.yaml)
  repos.txt                # repos to clone into work_root:  <name> <git-url>
  house/<prompt>.md        # extra instructions added to prompts/implement.md, babysit.md, pr-review.md
  rules/<name>/rule.json   # private review rule modules
```
It's found at `$BRAIN_OVERLAY`, or `overlay:` in `~/.config/llm-brain/local.yaml`, or `~/projects/llm-brain-overlay`.
Config layers, later wins: `config/fleet.yaml` → overlay `config.yaml` → `~/.config/llm-brain/local.yaml` (one machine). See `examples/overlay/`.

## Review rules: modules, union, fuzzy

`pr_review.rule_modules` + `extra_rule_modules` list the modules. **Every** matching rule of **every** applicable module is used (unlike open-code-review's first match). Built in:

| Module | What |
|---|---|
| `security` | OWASP Top 10 (A01–A10) + performance. Reviews weigh these first. |
| `ocr` | [alibaba/open-code-review](https://github.com/alibaba/open-code-review) language rules + the reviewed repo's own `.opencodereview/rule.json` |
| `multitenant` | tenant/role scoping, cache keys, feature flags |
| `migrations` | schema changes safe at production size |
| `contracts` | API ↔ UI ↔ mock agreement, description vs code, limits, lifecycle edges |

A module is a folder with an OCR-format `rule.json` (in `review-rules/`, your overlay's `rules/`, or `{name, git, path}` for any git repo). It can say **when** it applies, loosely:

```json
{ "name": "migrations",
  "when": { "repos": ["*api*"], "files": ["**/migrations/**"], "keywords": ["ALTER TABLE", "backfill"], "min": 1 },
  "rules": [ { "path": "**/*", "rule": "No data backfill inside a deploy-time migration…" } ] }
```
Each matching repo glob, file glob or keyword is one hit; the module applies with at least `min` hits. Keywords are fuzzy: case, `_` and `-` are ignored and they match inside words, so `tenant` hits `tenant_id`, `TenantName` and `multi-tenant`. A single rule entry can carry its own `when`.

Try it: `bin/review-rules --repo acme/api --base origin/main <files…>` prints which modules applied and their rules.

## LLM roles and accounts

`roles.implementer` builds; `roles.reviewer` cross-checks, and is always a **different** provider when one is available (one provider → it reviews itself in a fresh session). `bin/llm-run` is the only vendor-specific code (claude, codex, gemini).

With `gateway.enabled`, workers go through [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI), which holds every subscription login. Give personal accounts a higher `priority` than work ones and use `fill-first` routing: work is used only while personal is cooling down, and personal takes over again after its reset. The hub polls the gateway and shows each account's 5h/weekly quota left, which one is serving now, and the account, model, tokens and latency of every call. Stop the gateway before editing a credential file; a live edit can invalidate its refresh token.

## Team chat

The hub holds the Slack connection. At `/settings/slack` you paste a token and pick what to watch: channels, all your DMs and group DMs, and mentions of you anywhere. It stores every message and makes it searchable at `/chat`. It is read-only and never posts.

`bin/chat-watch` runs first on every fleet tick and reads the new messages from the hub:
- A message from someone in `chat.trusted` that names an issue or PR (`#212`, `web#12`, a GitHub link) becomes a note on it. Workers treat those notes like that person's review comments, and the latest one wins. A new note wakes the babysitter.
- Each batch gets one LLM pass that writes a short digest for the overview. If a trusted person says a fleet issue should stop ("stand down, Sam has it"), it labels the issue `agent:stop` and kills its worker.

Setup: create a Slack app with the user scopes `channels:history groups:history im:history mpim:history channels:read groups:read im:read mpim:read users:read search:read`, install it, and paste the `xoxp-…` token at `/settings/slack`. You can also put `SLACK_TOKEN=…` in `~/.config/llm-brain/slack.env`. Then fill in `chat:` in your overlay; see `examples/overlay/config.yaml`.

## The fleet

- **Intake:** open issues assigned to `intake.assignee`, written by someone in `intake.authors`, newer than `created_since`. At most `max_in_flight` at once (default 2); the next starts when one is merged or closed. An issue you're already working by hand (an open PR of yours mentions it) is skipped. Each issue is picked up once; if there's nothing to build it gets `agent:done`.
- **Claims:** an `llm-brain:claim host=<host>` comment; the earliest wins, so two machines never collide.
- **PRs:** carry `<!-- llm-brain:issue=N -->`; `fleet babysit` finds them by it, fixes review comments and CI, and merges to the default branch when approved + green + internal review clean. It never touches staging/production promotion.
- **Reviews:** once per head commit; a review that dies without posting is retried up to 3 times. It never reviews your own PRs.
- **Kill switches:** `paused: true` (all machines), `fleet pause` (this machine), label `agent:stop` (one issue).

```
fleet tick | dispatch | babysit | review-prs | review-pr <url> | review <url> | status | labels | pause | resume | providers
```

## Shared memory

One memory every agent reads before it works and writes to as it learns, so a Codex worker doesn't rediscover what a Claude session found yesterday. Each memory is a lesson, not a transcript: a `fact`, `decision`, `gotcha`, `runbook` or `handoff`, scoped to a repo (and an issue, a PR, files) or a folder.

- **Write:** `bin/brain-remember gotcha "RDS refuses IAM tokens on replication" "Use the password user for the replica slot." --issue 12`, or `POST /api/memory` with JSON. The hub checks it:
  - kind and scope are valid, and the title and body are small (140 and 2,000 bytes);
  - nothing looks like a credential (key prefixes, `password=…`, tokens in URLs, long random strings). Such a write is refused, and the refusal never repeats the value;
  - an exact repeat in the same scope is a no-op, and a near-duplicate (70 % of the same words) supersedes the older memory instead of adding a second one;
  - handoffs expire after 14 days and facts after 180; decisions, gotchas and runbooks don't expire.
- **Read:** `bin/brain-context` prints the context pack for the current repo, issue (from `$BRAIN_ISSUE` or the branch name) and folder as markdown. `GET /api/memory/context?repo=&issue=&pr=&folder=&files=&q=&k=` gives JSON (`&format=md` gives markdown). Memories are ranked by scope (this issue, then the same files, this folder, this repo), kind, confidence and age. Anything that names a file, flag or function is marked "verify before use". The pack is scored in memory on the hub (a few ms); TaskAI's hybrid search is merged in when it answers within 150 ms, and is otherwise cached for the next pack.
- **Awareness:** the pack also says who else is on it right now: live sessions, running fleet workers and open hub terminals in the same issue, PR, folder or repo. `GET /api/presence?repo=&issue=&cwd=` gives the same as a feed; `/memory` shows it with the recent memories.
- **Fleet:** with `memory.enabled: true`, `bin/llm-run` adds the pack and the `brain-remember` instructions to every worker's prompt. It fails open: no hub, no change.
- **TaskAI:** every memory is also one page in TaskAI's "Agent memory" project (`--memory-project`, found or created on first use), so people can read and search it there. The hub is the outbox: a write lands on the hub first and is forwarded with retries (30 s doubling to an hour). A superseded memory's page is retitled `[superseded]`. The key is `TASKAI_API_KEY` in `~/.config/llm-brain/taskai.env` (or `TASKAI_X_API_KEY` in `secrets.env`); with no key, memories stay on the hub.
- **Claude's own memories:** `hub memory import [--to http://<hub>:8787]` imports `~/.claude/projects/*/memory/*.md` on that Mac. It is safe to run again: an unchanged file is a no-op, an edited file supersedes its earlier import, and a file an agent has since improved is left alone.

## Hub (`hub/`)

Go + [Turso](https://github.com/tursodatabase/turso) (the Rust rewrite of SQLite), server-rendered, no build step. Binds only to 127.0.0.1 and the machine's Tailscale IP; ship requests are accepted only from loopback and 100.64.0.0/10.

- **Overview:** 24-hour KPI tiles with sparklines and change against the previous day, the top findings, the issue pipeline, a quota gauge per account, activity per host, agents running now, recent runs. Long tables and logs sit behind expanders.
- **Insights** (`/insights?range=24h|7d|30d|all`): server-rendered SVG charts, no JavaScript. Token use by model and kind, gateway requests by account, latency p50/p95, errors and 429s, personal vs work share and when work was used; each quota window's headroom, burn rate and projected run-out; the API-equivalent value of your tokens (an estimate from the `Prices` table in `hub/internal/insights/price.go`); sessions and prompts by host, a weekday × hour heatmap, top projects, tool calls and error rates, the longest and most expensive sessions; fleet runs by kind and outcome, review verdicts and blockers, busiest repos and cycle times; chat volume, mentions and busiest threads. Plain-English findings at the top come from fixed rules, not an LLM. JSON for every section at `/api/insights/{kpis,callouts,llm,quota,cost,sessions,fleet,chat}` and per conversation at `/api/insights/sessions/{id}`. A background rollup condenses messages into small tables once (about 15 s for 1 GB), then keeps up incrementally; reports are cached for a minute.
- **Runs:** every worker with its prompt, live log, status, PR/issue link and its own conversation, with a strip of its tokens, cost estimate, tool calls and token use over time.
- **Sessions:** every `~/.claude/projects/**.jsonl` and `~/.codex/sessions/**.jsonl` as a readable, live transcript, with the same strip; runs of three or more consecutive tool calls fold into one expander.
- **Search:** a bleve full-text index over messages, thinking, tool calls and output, sessions, run logs and events — a few ms per query. Syntax: `words`, `"phrase"`, `deploy*`, `-word`, code tokens as typed (`app#835`, `a/b.go`, uuids, `snake_case`), filters `host:` `type:` `provider:` `kind:` `tool:` `role:` `status:` `session:` `after:` `before:` (also `7d`, `today`).
- **Chat:** Slack, read-only — the hub only calls read methods and never posts. Connect on `/settings/slack` by pasting a user token (or put `SLACK_TOKEN=` in `~/.config/llm-brain/slack.env`; it seeds the hub's database once). The token is never shown again or returned by any API; the page shows the account and any missing scopes.
  - What it watches: the channels you tick, optionally all your DMs and group DMs, and optionally mentions of you anywhere. It reads them every 2 minutes (`--chat-interval`) and backfills 24 h for a new conversation (`--chat-backfill`).
  - Where it shows up: `/chat` groups messages by source and thread, and search takes `type:chat channel: from: mentions:me`.
  - Fleet scripts read `GET /api/chat/messages?after=<id>` and `GET /api/chat/me`.

- **Chats** (`/chats`, **+ New chat** in the nav): start a conversation on any Mac from the hub. One picker says who you talk to:
  - **Agent chat:** Claude Code, Codex or Gemini CLI (whichever that Mac has), with tools, in a folder. With no folder picked it runs read-only in a scratch folder under the state dir. Options pick the host, a repo or worktree under the allowed roots (or a new `<repo>-wt-<name>` worktree from a base branch), the model, and the mode: read-only (`plan` / `read-only`), edits (`acceptEdits` / `workspace-write`, the default in a folder), or bypass, which needs its own tick box per session.
  - **Plain chat:** any model the account gateway lists (`/v1/models`, grouped Claude / OpenAI / Gemini / other), no tools, streamed token by token, markdown rendered safely, and the model can change mid-conversation. It needs `CPA_API_KEY` in `--gateway-env`; when the gateway is down the page says so.
  - Each turn runs headless (`claude -p --output-format stream-json`, `codex exec --json`, `gemini -o stream-json`), through the gateway the way `bin/llm-run` does, else on the CLI's own login. The session id is captured, so every later message resumes it. While a turn runs the page shows a typing indicator and its live output (the turn log is `logs/agent-<id>.<time>.log`, shipped and shown like a worker log); the transcript arrives through normal ingestion.
  - Every session page, hub-started or not, gets a composer to continue it (when its Mac has a live agent), **Stop**, **Review with <the other LLM>** (a read-only review of `git diff <base>...HEAD` plus uncommitted work, using `bin/review-rules`, as its own linked chat), **Send review back** (a resume turn with the findings), and the exact `claude --resume <id>` / `codex resume <id>` to continue by hand.
  - `/agents` lists every hub-run session, the hosts with a live agent, and the command log. `/prompts` is a prompt library with `{{VARS}}` (CWD, REPO, HOST and BASE are filled in).
  - How it runs: the hub keeps a command queue. `hub serve` runs the session agent for its own Mac; `hub ship` runs it on the laptop and polls `POST /api/agent/poll` (JSON) every 3 s, so nothing listens there. Folders are checked on the Mac that runs them, after resolving symlinks, against `--roots`. Every control endpoint is tailnet-only and refuses cross-site POSTs; every command is logged.

Ingestion is incremental and idempotent: a byte offset per file advances in the same transaction as its rows; the hub re-scans every 10 s.

```
make -C hub build     # -> bin/hub (darwin/arm64, no CGO)
make -C hub check     # go vet + go test
bin/hub serve [--port 8787] [--db PATH] [--index PATH|off] [--people login=Name,...] [--gateway URL] [--gateway-env PATH] [--slack-env PATH] [--chat-interval 2m|0]
bin/hub ship --to http://<hub-host>:8787 [--once] [--poll 3s]
# both: [--agent=true|false] [--roots DIR,DIR] [--brain DIR]
```

`--agent` (on by default) runs the session agent on that Mac; `--agent=false` makes it read-only again. `--roots` are the folders sessions may run in (default: `work_root` from the fleet config if it exists, else `~/projects`). `--brain` is the llm-brain checkout for `config/fleet.yaml` and `bin/review-rules` (default: the one the binary lives in).

## Caveats

- Routing subscription OAuth logins through a proxy may not be covered by your provider's terms. Read them, and keep work data on work accounts if your employer requires it.
- No auth on the hub: anyone on your tailnet can read every transcript, and can start agent sessions on any Mac running `hub serve` or `hub ship` with `--agent` on. Keep it on a tailnet you control, or run with `--agent=false`.
- `hub.db` grows with your transcripts; there is no retention yet.

## License

MIT — see [LICENSE](LICENSE).
