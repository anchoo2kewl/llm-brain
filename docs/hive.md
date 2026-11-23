# Hive: shared skills, rules, agents and MCP servers

The hive is one set of skills, rules, agents and MCP servers for the whole cluster. You add an item once. Each Hub copies it to its peers and installs it for every agent CLI on its own machine, so Claude Code, Codex and Antigravity work from the same instructions.

It ships in the `hub-v*` release binaries (Antigravity support from 0.1.6). The `/skills` page in the Hub lists every item and lets you turn one off.

## Where each kind goes

| Kind | Claude Code | Codex | Antigravity |
| --- | --- | --- | --- |
| Skill | `~/.claude/skills/<source>-<name>` | `~/.codex/skills/<source>-<name>` | `~/.gemini/config/skills/<source>-<name>` |
| Agent | `~/.claude/agents/<source>-<name>` | — | — |
| Rule | `~/.claude/rules/` | a marked block in `~/.codex/AGENTS.md` | `~/.gemini/config/rules/` |
| MCP server | `claude mcp add-json -s user` | a marked block in `~/.codex/config.toml` | `~/.gemini/config/mcp_config.json` |

The files live under the Hub's state folder (`<state>/hive`) and are linked into place. The Hub changes only what it installed: a skill, rule or MCP server of the same name that you added yourself is left alone. A machine that runs only the agent (`hub ship`) gets the set from its Hub.

MCP definitions never hold keys. A value is written as `${secret:NAME}`; each machine keeps its own secret values, encrypted, and never sends them to another Hub. A server whose secret is missing on a machine is not installed there, and the Hub says which secret to set.

## Add items

From a machine that runs a Hub:

```sh
hub skills import --dir <folder of skills> --source <name>
hub skills import --dir <folder of rules>  --kind rule  --source <name>
hub skills import --dir <folder of agents> --kind agent --source <name>
```

`--source` names where the items come from; links are named `<source>-<name>`.

## What the hive holds

| Source | What it adds | Upstream |
| --- | --- | --- |
| Ponytail | The lazy senior dev rule: the smallest complete change, reuse before writing, a test for every new branch, and skills for audit and review. | [DietrichGebert/ponytail](https://github.com/DietrichGebert/ponytail) |
| pstack | Skills and agents for rigorous AI-assisted engineering. Skills that fetch or run code from the network are left out. | [backnotprop/pstack](https://github.com/backnotprop/pstack) |
| Cursor team kit | Thermo-nuclear code quality review, deslop, fix-ci, loop-on-ci, fix-merge-conflicts, verify-this, new-branch-and-pr and more. In Cursor: `/add-plugin cursor-team-kit`. | [cursor/plugins](https://github.com/cursor/plugins/tree/main/cursor-team-kit) |
| Graphify | The `/graphify` skill for each CLI. The daily tool update upgrades Graphify and copies its new skill for Claude Code, Codex and Antigravity. | [Graphify-Labs/graphify](https://github.com/Graphify-Labs/graphify) |
| llm-brain | The rules below and the cross-review and design-review skills. | this repo |

## Rules

Rules load in every session of every CLI.

| Rule | What it says | Why |
| --- | --- | --- |
| Quality gate | Read the repo's instructions and the shared memory, check who else works there, map the code with Graphify, make a small tested change, run the checks, cross-review, leave a handoff, report what passed and what did not. | One bar for every CLI, so the result does not depend on which agent did the work. |
| Graphify | In every repo, query the code graph first (`graphify query "<question>"`). Build it with `graphify update .` if it is missing, and update it after a change. Keep `graphify-out/` local. | A graph query costs far less context than broad grep and many file reads. |
| Ponytail | The smallest complete change (see above). | Less new code means less to review and less to break. |
| Design review | A good change removes moving parts. Look for structural regressions, missed simplification, special-case branching, the wrong layer, unclear types, files pushed past 1,000 lines, non-atomic writes, and moves that only rename. | Matches the Entropy Score's design rules, so reviews and scores agree. |

## Cross-review

Before a change is done, pushed or put in a PR, the author runs:

```sh
~/.local/state/llm-brain/hive/skill/cross-review/cross-review --self claude   # or codex, antigravity
```

It reviews everything since the merge base with the default branch with every other CLI (Claude Code in plan mode, Codex in a read-only sandbox, Antigravity in plan mode) plus the OpenAI API and Jev review, in parallel and read-only. The author checks each finding against the code, fixes the real ones and records why it dismissed the rest. A reviewer that did not answer is a gap, not a pass.

The OpenAI API and Jev part is `cheap-review`: small focused requests to the official API with a fixed budget, used instead of a CLI review before a merge.
