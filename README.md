# llm-brain

A Hub for one person's coding agents. It runs Claude Code, Codex and Antigravity on every machine you own, shows every session in one place, gives all of them one shared memory and one set of skills and rules, and keeps a small fleet that turns assigned issues into reviewed PRs.

## Why

- **Agents forget, and they do not talk to each other.** A Codex session rediscovers what a Claude session found yesterday. The Hub keeps one memory that every agent reads before it works and writes to as it learns.
- **Each CLI has its own skills, rules and MCP servers.** You set them up once in the Hub and it installs them for every CLI on every machine.
- **One model reviewing its own work misses things.** Every change is reviewed by the other CLIs and by a separate API reviewer, read-only.
- **Your work is spread over machines.** A laptop goes offline; a desk machine stays on. Each machine runs its own Hub, the Hubs copy to each other, and nothing stops when one is away.
- **Agents write too much code.** The shared rules push the smallest complete change, a code graph before broad search, and a design review that removes moving parts.

## How it flows

```
you / an issue   -> terminal or fleet worker (Claude Code | Codex | Antigravity)
                      starts with: shared memory + who else works here + hive rules and skills
                      maps the repo with Graphify, makes the smallest change (Ponytail)
                 -> cross-review (the other CLIs + OpenAI API + Jev) -> fix real findings
                 -> PR (agent labels) -> PR review + rule modules + Entropy Score
                 -> a person approves -> fleet merges when CI is green (default branch only)
every lesson     -> brain-remember -> this Hub -> peer Hubs (every minute) -> TaskAI (optional)
every LLM call   -> account gateway (personal first, then work) -> usage and quota in the Hub
```

## Building blocks

| Block | What it does, and why |
| --- | --- |
| [Shared memory](docs/memory.md) | Short lessons (fact, decision, gotcha, runbook, handoff) scoped to a repo, issue or folder. Every session starts with the top ones, so agents stop relearning. Secrets are refused at write. |
| [Memory replication](docs/memory.md#between-hubs) | Each Hub pulls the memories written on its peers every minute over the cluster mesh. There is no distributed database: an offline laptop keeps writing to its own Hub and catches up later. |
| [TaskAI Memory Protocol](docs/memory.md#taskai) | An open, per-user memory API for any app or LLM, with hybrid search, a 1 GB quota and compaction; the Hub is one client ([taskai#50](https://github.com/biswas-dev/taskai/pull/50), not live yet). Until then the Hub copies each memory to a TaskAI page. Off unless a TaskAI API key is set. |
| [Hive](docs/hive.md) | Skills, rules, agents and MCP servers stored once in the Hub, copied across the cluster and linked into Claude Code, Codex and Antigravity. |
| [Graphify](https://github.com/Graphify-Labs/graphify) | A local code graph. The hive rule says: query the graph first in every repo, build it if missing. The daily tool update keeps Graphify and its skill current. |
| [Ponytail](https://github.com/DietrichGebert/ponytail) | Hive rule and skills: the lazy senior dev, the smallest complete change that reuses what exists. |
| [pstack](https://github.com/backnotprop/pstack) | Hive skills and agents for rigorous AI-assisted engineering. |
| [Cursor team kit](https://github.com/cursor/plugins/tree/main/cursor-team-kit) | Hive skills: thermo-nuclear code quality review, deslop, fix-ci, loop-on-ci, verify-this and more. |
| [Design review](docs/hive.md#rules) | Hive rule: a good change removes moving parts; flags missed simplifications, special cases and wrong layers. |
| [Quality gate](docs/hive.md#rules) | Hive rule: read the instructions and memory, map with Graphify, make a small tested change, cross-review, report honestly. |
| [Cross-review](docs/hive.md#cross-review) | Each CLI's change is reviewed by the other two CLIs plus the OpenAI API and Jev, all read-only and in parallel. |
| [Issue fleet](docs/fleet.md) | Claims issues assigned to you, implements them, opens PRs and follows them through review and CI. |
| [PR review](docs/pr-review.md) | Reviews the PRs that ask for you, once per head commit. Agents comment only; they never approve. |
| [Review rule modules](docs/pr-review.md#rule-modules) | Security (OWASP), performance, open-code-review, multi-tenancy, migrations, contracts, plus your own. |
| [Entropy Score](docs/slop-score.md) | A 0-100 score of how hard a PR is to understand, trust and maintain, posted on every review. Called AI Slop Score in this repo's `bin/slop-score`. |
| [Agent PR policy](docs/agent-pr-policy.md) | `agent:assisted`, `agent:author:<provider>` and `agent:pr-open` labels; advisory comments only. |
| [LLM roles and gateway](docs/llm-roles-and-gateway.md) | A different LLM reviews the implementer's work. One gateway shares your subscriptions, personal accounts first. |
| [Terminals and Workspace](docs/hub-terminals.md) | Claude Code, Codex, Antigravity (`agy`) or a shell in tmux, live in the browser and the apps, many at once. In the Mac app, Ctrl+Enter sends now in every agent CLI. |
| [Hub](docs/hub.md) | Inbox, Now, History, [search](docs/hub-search.md), [insights](docs/hub-insights.md), [tasks and chats](docs/hub-tasks-and-chats.md), [files and editor](docs/hub-files-and-editor.md). |
| [Desktop app](docs/desktop-app.md) | macOS ([native](docs/mac-app.md)), Linux and Windows clients: live terminals, hub switcher, quick search, notifications, restart to update. One look on every system: [design spec](docs/design.md). |
| [MCP control](docs/mcp.md) | Manage hubs, sessions, moves, clones and memory from any MCP client. |
| [Tool updates](docs/hub.md#tool-updates) | A daily job updates Homebrew, Claude Code, Codex, Antigravity, gh, open-code-review and Graphify on every machine. The Hub [updates itself](docs/hub-auto-updates.md) from `hub-v*` releases. |
| [Team chat](docs/chat.md) | Reads Slack; a trusted teammate's note about an issue reaches its worker. |
| [Jev](docs/jev.md) | Typed yes/no decisions from any agent. |

The Hub service ships as signed `hub-v*` [release binaries](https://github.com/anchoo2kewl/llm-brain/releases). Memory replication, the hive, MCP install and the Graphify update are in those binaries (0.1.6 to 0.1.8); the `hub/` folder here is an older snapshot of its source.

## Quick start

```sh
git clone https://github.com/anchoo2kewl/llm-brain ~/projects/llm-brain
mkdir -p ~/projects/llm-brain-overlay
cp -R ~/projects/llm-brain/examples/overlay/. ~/projects/llm-brain-overlay/
$EDITOR ~/projects/llm-brain-overlay/config.yaml   # who you are, who hands you work, which repos
~/projects/llm-brain/bootstrap.sh                  # tools, hub (from hub/), fleet loop
```

`bootstrap.sh` builds the older `hub/` snapshot. For the hive, memory replication and the rest, install the release binary and let it update itself:

```sh
cd ~/projects/llm-brain
gh release download hub-v0.1.8 -p hub-darwin-arm64 -p brain-hub-update -D /tmp/hub-release
deploy/install-hub-macos.sh /tmp/hub-release/hub-darwin-arm64
install -m 755 /tmp/hub-release/brain-hub-update bin/
python3 deploy/install-hub-auto-update.py --brain ~/projects/llm-brain
```

Open `http://<this-machine>:8787`. Details: [Getting started](docs/getting-started.md). Every doc: [docs/](docs/README.md).

## Caveats

- The Hub has no login. Its ship, agent, memory and session-control endpoints accept only loopback and Tailscale addresses. Anyone on your tailnet can read transcripts and start sessions, so keep it on a tailnet you control.
- Routing subscription logins through a proxy may not be covered by your provider's terms. Keep work data on work accounts if your employer requires it.

## License

MIT. See [LICENSE](LICENSE).
