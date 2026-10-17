# llm-brain

A small fleet of coding agents that works for one person. It picks up the issues your teammates assign you, builds them, opens PRs, answers review comments and merges when CI and a reviewer agree. It reviews the PRs that ask for you. It shares your LLM subscriptions through one gateway. A web hub shows every agent, run and Claude/Codex conversation on every Mac you own, with fast search, live terminals and a shared memory the agents read and write.

It runs on one Mac you leave on, under launchd. Other Macs ship their sessions to it. The hub has no login: reach it over Tailscale only.

## Features

| Feature | What it does |
| --- | --- |
| [Issue fleet](docs/fleet.md) | Claims issues assigned to you, implements them, opens PRs, babysits them to merge. |
| [PR review](docs/pr-review.md) | Reviews every PR that requests you, once per head commit, with short security-first notes. |
| [Review rule modules](docs/pr-review.md#rule-modules) | OWASP, performance, open-code-review, tenancy, migrations, contracts, plus your own. |
| [AI Slop Score](docs/slop-score.md) | A taste and PR-hygiene score posted on every review; blocks approval above a threshold. |
| [LLM roles and gateway](docs/llm-roles-and-gateway.md) | Implementer and a different reviewer LLM; personal accounts first, work only when personal runs out. |
| [Hub](docs/hub.md) | Inbox, Now, History, search and insights for every agent on every Mac. |
| [Terminals and Workspace](docs/hub-terminals.md) | Claude Code, Codex, Gemini or a shell in tmux, live in the browser, many at once. |
| [Tasks and model chats](docs/hub-tasks-and-chats.md) | Start, continue, stop and cross-review headless sessions; chat with any gateway model. |
| [Files and editor](docs/hub-files-and-editor.md) | Browse a Mac, move files both ways, edit a folder in a Sublime-style Monaco editor. |
| [Search](docs/hub-search.md) | Full-text search over every message, tool call, run log, chat and event. |
| [Insights](docs/hub-insights.md) | Token use, quota headroom, cost estimates, session and fleet stats. |
| [Team chat](docs/chat.md) | Reads Slack; a trusted teammate's message about an issue becomes an instruction to its worker. |
| [Shared memory](docs/memory.md) | Lessons agents leave for each other, plus who else is working on the same thing. |
| [Jev](docs/jev.md) | Typed yes/no questions to Jev (TypeSafe systemOne) from any agent. |

## How it flows

```
issue assigned  -> fleet dispatch   -> worker (claude/codex) -> PR -> internal review (the other LLM)
                                                                |
review / CI     -> fleet babysit    -> fix, reply, re-review -> merge (default branch only)
PR requests you -> fleet review-prs -> rule modules (OWASP, perf, OCR, yours) -> review on GitHub
Slack message   -> bin/chat-watch   -> notes for workers, digest, "stop" on a fleet issue
every LLM call  -> account gateway (personal -> work) -> hub (quota, model, account per call)
```

## Quick start

```sh
git clone https://github.com/anchoo2kewl/llm-brain ~/projects/llm-brain
mkdir -p ~/projects/llm-brain-overlay
cp -R ~/projects/llm-brain/examples/overlay/. ~/projects/llm-brain-overlay/
$EDITOR ~/projects/llm-brain-overlay/config.yaml   # who you are, who hands you work, which repos
~/projects/llm-brain/bootstrap.sh                  # tools, hub, fleet loop (launchd)
```

Open `http://<this-mac>:8787`. On a laptop that should only send its sessions to the hub, run `bootstrap.sh --shipper` instead.

The details are in [Getting started](docs/getting-started.md).

## Documentation

| Doc | Covers |
| --- | --- |
| [Getting started](docs/getting-started.md) | Install, bootstrap flags, launchd jobs, logins, the shipper |
| [Configuration](docs/configuration.md) | Config layers, every key of `config/fleet.yaml`, env vars, files on disk |
| [Overlay](docs/overlay.md) | Your private settings: people, repos, house notes, private rules |
| [Fleet](docs/fleet.md) | Intake, claims, workers, babysitting, merge policy, kill switches, every `fleet` command |
| [PR review](docs/pr-review.md) | Review flow, rule modules, fuzzy `when`, `bin/review-rules`, output format |
| [AI Slop Score](docs/slop-score.md) | The taste and PR-hygiene score on every review |
| [LLM roles and gateway](docs/llm-roles-and-gateway.md) | `bin/llm-run`, providers, CLIProxyAPI, account priority, quota |
| [Hub](docs/hub.md) | Pages, `hub serve` / `hub ship` flags, ingestion, app, push, shortcuts, updates |
| [Hub terminals](docs/hub-terminals.md) | Terminals, Workspace, Terminal/Chat view, frozen terminals, close, rename |
| [Hub tasks and chats](docs/hub-tasks-and-chats.md) | Tasks, model chats, cross-review, prompt library, host agents |
| [Hub files and editor](docs/hub-files-and-editor.md) | Browse, download, upload, kept files, the editor |
| [Hub search](docs/hub-search.md) | Index, query syntax, filters |
| [Hub insights](docs/hub-insights.md) | Fleet overview, `/insights`, JSON API, cost estimates |
| [Hub API](docs/hub-api.md) | Every HTTP endpoint |
| [Team chat](docs/chat.md) | Slack setup, trusted people, notes, digest, stop |
| [Shared memory](docs/memory.md) | `brain-context`, `brain-remember`, `/memory`, TaskAI, import |
| [Jev](docs/jev.md) | `bin/jev` usage and key lookup |
| [Troubleshooting](docs/troubleshooting.md) | Known gotchas and how to get out of them |

## Caveats

- Routing subscription OAuth logins through a proxy may not be covered by your provider's terms. Read them, and keep work data on work accounts if your employer requires it.
- The hub has no auth. Anyone on your tailnet can read every transcript and can start agent sessions and terminals on any Mac that runs `hub serve` or `hub ship` with `--agent` on. Keep it on a tailnet you control, or run with `--agent=false`.
- `hub.db` grows with your transcripts. There is no retention yet.

## License

MIT. See [LICENSE](LICENSE).
