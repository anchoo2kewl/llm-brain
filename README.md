# llm-brain

Anshuman's agent fleet: it takes issues Naj and Greg assign, ships them to merged PRs, and reviews the PRs that request Anshuman.
Skills, memory and settings live in [claude-backup](https://github.com/anchoo2kewl/claude-backup) (two-way synced across machines). This repo is only the fleet.

## Components

| | What | Where |
|---|---|---|
| 1 | **LLM management.** Which LLM implements and which reviews; multiple accounts per provider, personal first | `config/fleet.yaml` `providers` / `roles`, `bin/llm-run` (gateway TBD) |
| 2 | **Issue → PR → merge.** Opt-in label `agent-ready` on issues assigned to me by Naj, Greg or me | `fleet dispatch` → `prompts/implement.md`; `fleet babysit` → `prompts/babysit.md` |
| 3 | **PR reviewer.** Every PR in the elastio blue-stack family that requests or mentions me; re-reviews on every new head commit until it approves | `fleet review-prs` → `prompts/pr-review.md` + `bin/review-rules` |

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

## Commands
```
fleet tick | dispatch | babysit | review-prs | review-pr <url> | review <url> (internal) | status | labels | pause | resume | providers
```
Kill switches: `paused: true` in config (all machines), `fleet pause` (this machine), label `agent:stop` (one issue).

## New machine
```
git clone git@github.com:anchoo2kewl/llm-brain.git ~/projects/llm-brain && ~/projects/llm-brain/bootstrap.sh
```
