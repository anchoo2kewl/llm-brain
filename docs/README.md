# llm-brain docs

Every feature of llm-brain, one page per area. Start with [Getting started](getting-started.md).

## Set up

| Doc | What's in it |
| --- | --- |
| [Getting started](getting-started.md) | Install, `bootstrap.sh` and its flags, launchd jobs, logins, shipper laptops |
| [Configuration](configuration.md) | Config layers, every key of `config/fleet.yaml`, environment variables, files on disk |
| [Overlay](overlay.md) | The private folder with your people, repos, house notes and rule modules |

## Agents

| Doc | What's in it |
| --- | --- |
| [Fleet](fleet.md) | The issue to PR to merge loop: intake, claims, workers, babysitter, merge policy, kill switches, every `fleet` subcommand |
| [PR review](pr-review.md) | Reviews of PRs that request you: flow, rule modules, fuzzy `when`, writing a module, `bin/review-rules`, output format |
| [AI Slop Score](slop-score.md) | AI Slop Score: a taste and PR-hygiene score posted on every review; blocks approval above a threshold |
| [LLM roles and gateway](llm-roles-and-gateway.md) | Implementer and reviewer roles, `bin/llm-run`, the CLIProxyAPI account gateway, priorities, quota |
| [Team chat](chat.md) | Slack in the hub, `bin/chat-watch`, trusted people, notes, digest, stopping an issue |
| [Shared memory](memory.md) | Lessons agents share, context packs, presence, replication between hubs, TaskAI, importing Claude's memories |
| [Hive](hive.md) | Skills, rules, agents and MCP servers shared by every CLI on every machine; cross-review |
| [Agent PR policy](agent-pr-policy.md) | Agent labels and advisory-only reviews |
| [Agent rules (draft)](agent-rules.md) | What agents may do: human approval, stop, limits, identity |
| [MCP control](mcp.md) | Manage hubs, sessions, moves, clones and workspaces from any MCP client with `bin/brain-mcp` |
| [Jev](jev.md) | `bin/jev`: typed yes/no decisions from any agent |

## Hub

| Doc | What's in it |
| --- | --- |
| [Hub](hub.md) | What the hub is, its pages, the cluster, `hub serve` / `hub ship` / `hub memory` / `hub version`, ingestion, installable app, push, shortcuts, tool updates |
| [Terminals and Workspace](hub-terminals.md) | Live CLIs in tmux, the Workspace grid, Terminal and Chat views, folder-trust answers, frozen terminals, close, rename |
| [Tasks and model chats](hub-tasks-and-chats.md) | Headless tasks on any Mac, plain chats through the gateway, cross-review, prompt library, host agents |
| [Files and editor](hub-files-and-editor.md) | Browse a Mac, download, upload, kept documents, the Monaco editor |
| [Search](hub-search.md) | The full-text index and the query syntax |
| [Insights](hub-insights.md) | Fleet overview, `/insights` charts and findings, the JSON API, cost estimates |
| [API](hub-api.md) | Every HTTP endpoint the hub serves |
| [Desktop app](desktop-app.md) | macOS, Linux and Windows clients |
| [Mac app](mac-app.md) | The native macOS client |
| [Design spec](design.md) | The shared look: font, session list, restart sign, pasted images |
| [Move folders](move-folders.md) | Pick the destination folder when you move or clone a session |
| [Hub automatic updates](hub-auto-updates.md) | How the hub updates itself from `hub-v*` releases |

## When things go wrong

| Doc | What's in it |
| --- | --- |
| [Troubleshooting](troubleshooting.md) | Known gotchas, where the logs are, how to unstick things |

[Claude resource limits](claude-limits.md): shared session budgets, subagent caps and bounded context for interactive and fleet work.
