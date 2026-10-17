# Configuration

All settings live in YAML files merged in layers, plus a few env files and environment variables.

## Layers

Later files win. Maps merge key by key; lists and scalars are replaced.

| Order | File | Holds | In git |
| --- | --- | --- | --- |
| 1 | `config/fleet.yaml` | Public defaults | Yes, this repo |
| 2 | `<overlay>/config.yaml` | You: people, repos, orgs, house rules | Your private overlay |
| 3 | `~/.config/llm-brain/local.yaml` | This machine only | No |

The overlay folder is found at `$BRAIN_OVERLAY`, else the `overlay:` key in `local.yaml`, else `~/projects/llm-brain-overlay`. See [Overlay](overlay.md).

The shell scripts merge with `yq` (`bin/lib.sh`, function `cfg`). The hub's session agent reads the same three files with a small parser that only sees top-level keys and one level below (`gateway.url`), which is enough for the keys it needs.

To see the merged value of any key:

```sh
source ~/projects/llm-brain/bin/lib.sh; cfg '.intake'
```

## Keys

### Top level

| Key | Default | Meaning |
| --- | --- | --- |
| `paused` | `false` | Kill switch for every machine. One machine can pause itself with `fleet pause`. |
| `work_root` | `~/work` | Where your repos live. Workers make worktrees here. `$FLEET_WORK_ROOT` overrides it. |
| `hub_url` | `http://127.0.0.1:8787` | Where shippers send data. Fallback hub for memory and chat. |
| `people` | `{}` | GitHub handle to display name, e.g. `{octocat: Mona}`. Used in prompts and the hub. |
| `pr_marker` | `llm-brain:issue=` | The marker the fleet puts in PR bodies (`<!-- llm-brain:issue=N -->`). |
| `pr_owners` | `[]` | Orgs to search for fleet PRs. The first one is also used for the "open PR by hand" check and for hub links. |
| `reviewers` | `[]` | GitHub handles requested on every fleet PR. |
| `overlay` | unset | Only read from `local.yaml`: path of the overlay folder. |

### `providers`

One entry per LLM CLI. A provider is available when it is `enabled` and its `cli` is on `PATH`.

| Key | Default | Meaning |
| --- | --- | --- |
| `providers.<p>.enabled` | `true` for claude and codex, `false` for gemini | Whether this machine may use it. |
| `providers.<p>.cli` | `claude`, `codex`, `gemini` | The command to run. |
| `providers.<p>.model` | `""` | Model to pass. Empty means the CLI's own default. `$LLM_MODEL` overrides it for one run. |

### `gateway`

The account gateway. See [LLM roles and gateway](llm-roles-and-gateway.md).

| Key | Default | Meaning |
| --- | --- | --- |
| `gateway.enabled` | `true` | Route workers and tasks through the gateway when it answers. |
| `gateway.url` | `http://127.0.0.1:8317` | CLIProxyAPI base URL. Other machines use the gateway host's address. |
| `gateway.env_file` | `~/.config/llm-brain/cpa.env` | Holds `CPA_API_KEY` (client key) and `CPA_MGMT_KEY` (management key). Not in git. |

### `terminals`

| Key | Default | Meaning |
| --- | --- | --- |
| `terminals.gateway` | `false` (not in the default file) | Hub terminals for Claude and Codex go through the gateway too. Off means a terminal uses the CLI's own login. |

### `roles`

| Key | Default | Meaning |
| --- | --- | --- |
| `roles.implementer` | `claude` | Builds issues and babysits PRs. Falls back to the first available provider. |
| `roles.reviewer` | `codex` | Internal cross-review. Always a different provider from the implementer when one is available. |

### `owner`

| Key | Default | Meaning |
| --- | --- | --- |
| `owner.name` | `""` | Your name, used in prompts. |
| `owner.github` | `""` | Your GitHub login, used in prompts. |
| `owner.notify` | `""` | Reserved. No script reads it today. |

### `intake`

Which issues the fleet takes. See [Fleet](fleet.md#intake).

| Key | Default | Meaning |
| --- | --- | --- |
| `intake.repos` | `[]` | Repos to take issues from, e.g. `[acme/platform]`. The first one is where babysat PRs look up their issue. |
| `intake.assignee` | `""` | The assignee to look for, usually your login. Also used as "me" when skipping your own PRs in review. |
| `intake.authors` | `[]` | Who may hand you work. Issues by anyone else are ignored. |
| `intake.created_since` | `1970-01-01` | Ignore issues created before this date. |
| `intake.skip_labels` | `[agent:stop, agent:done]` | Issues with any of these are skipped (as are claimed, PR-open and blocked ones). |
| `intake.max_in_flight` | `2` | Issues the fleet owns at once, across all machines. |
| `intake.skip_if_my_open_pr` | `true` | Skip an issue that one of your open PRs already mentions. |

### `labels`

The labels the fleet writes. `fleet labels` creates them.

| Key | Default | Meaning |
| --- | --- | --- |
| `labels.claimed` | `agent:working` | A worker is implementing it. |
| `labels.pr_open` | `agent:pr-open` | A fleet PR is open for it. |
| `labels.blocked` | `agent:blocked` | Needs a human decision. |
| `labels.stop` | `agent:stop` | The fleet must not touch it. |
| `labels.done` | `agent:done` | Nothing left to build, a person needs to check. |

### `pr_review`

Reviews of PRs that request you. See [PR review](pr-review.md).

| Key | Default | Meaning |
| --- | --- | --- |
| `pr_review.enabled` | `true` | Turn PR review on or off. |
| `pr_review.provider` | `claude` | The CLI that writes PR reviews. This is separate from `roles.reviewer`. |
| `pr_review.owners` | `[]` | Orgs to watch for review requests and mentions. |
| `pr_review.repo_regex` | `.*` | Only review repos whose name matches. |
| `pr_review.updated_since` | `1970-01-01` | Skip PRs not updated since this date. |
| `pr_review.post` | `true` | `false` is a dry run: the review goes to a file, not GitHub. |
| `pr_review.rule_modules` | `[security, ocr, multitenant, migrations, contracts]` | Built-in or git rule modules. |
| `pr_review.extra_rule_modules` | `[]` | Your overlay's private modules. |

### `limits`

| Key | Default | Meaning |
| --- | --- | --- |
| `limits.max_workers` | `3` | Implement and babysit workers at once on this machine. |
| `limits.max_review_workers` | `3` | PR review workers at once, in their own pool. |
| `limits.implement_timeout` | `4h` | Hard limit for an implement worker (`gtimeout`). |
| `limits.babysit_timeout` | `2h` | Hard limit for a babysit worker. |
| `limits.review_timeout` | `30m` | Hard limit for a PR review and for `fleet review`. |

### `merge`

| Key | Default | Meaning |
| --- | --- | --- |
| `merge.auto` | `true` | Read by `fleet babysit`. `false` makes the babysitter stop at "ready to merge" and notify you. |
| `merge.require_approval` | `true` | Documents the rule. The babysit prompt always requires it; no script reads this key. |
| `merge.require_full_ci` | `true` | Same: always required by the prompt. |
| `merge.require_internal_review_clean` | `true` | Same: always required by the prompt. |

### `notify`

| Key | Default | Meaning |
| --- | --- | --- |
| `notify.macos` | `true` | macOS notifications from the `notify` helper in `bin/lib.sh`. Today `bin/chat-watch` uses it when it stops an issue. |
| `notify.slack_self_dm` | `false` | Reserved. No script reads it today. |

### `chat`

Team chat. See [Team chat](chat.md).

| Key | Default | Meaning |
| --- | --- | --- |
| `chat.enabled` | `false` | Run `bin/chat-watch` on every tick. |
| `chat.hub_url` | `http://127.0.0.1:8787` | The hub that holds the Slack connection. |
| `chat.trusted` | `{}` | Slack user id to GitHub handle. Their messages count like their PR comments. |
| `chat.channel_repos` | `{}` | Channel name to repo. A bare `#123` in that channel means that repo. |
| `chat.repos` | `[]` | Extra repos for short refs like `web#12`. Intake repos and `repos.txt` are always known. |
| `chat.insights` | `true` | Run the LLM digest pass. |
| `chat.provider` | `claude` | CLI for the digest. |
| `chat.model` | `""` | Model for the digest, e.g. `sonnet`. |

### `memory`

Shared agent memory. See [Shared memory](memory.md).

| Key | Default | Meaning |
| --- | --- | --- |
| `memory.enabled` | `false` | Add the context pack and save instructions to every worker prompt. |
| `memory.hub_url` | `http://127.0.0.1:8787` | The hub that keeps memories. Falls back to `hub_url`. |
| `memory.k` | `8` | Memories per prompt. |

### `jev`

Not in the default file. See [Jev](jev.md).

| Key | Default | Meaning |
| --- | --- | --- |
| `jev.env_file` | `~/.config/elastio/typesafe.env` | File holding `TYPESAFE_API_KEY`. |
| `jev.keychain` | `typesafe-jev-personal` | macOS keychain item to try last. |

### `config_sync`

Not in the default file. Used only by `bootstrap.sh`.

| Key | Meaning |
| --- | --- |
| `config_sync.repo` | Git URL of a repo that syncs your agent config (skills, memory, settings). |
| `config_sync.dir` | Where to clone it. |
| `config_sync.restore` | A script inside it to run after cloning. |

## Files on disk

| Path | What |
| --- | --- |
| `~/.config/llm-brain/local.yaml` | This machine's config layer. |
| `~/.config/llm-brain/PAUSE` | Present = this machine is paused. |
| `~/.config/llm-brain/cpa.env` | `CPA_API_KEY`, `CPA_MGMT_KEY` for the gateway. |
| `~/.config/llm-brain/slack.env` | `SLACK_TOKEN=…`, read once to seed the hub. |
| `~/.config/llm-brain/taskai.env` | `TASKAI_API_KEY` (or `TASKAI_X_API_KEY`) for the memory copy in TaskAI. `secrets.env` next to it is tried too. |
| `~/.local/state/llm-brain/` | State: `runs/` (pid and prompt per worker), `prs/` (fingerprints, review marks), `logs/`, `reviews/`, `notes/`, `chat/`, `status.json`, `tick.lock`. |
| `~/.local/state/llm-brain/hub.db` | The hub's Turso database. |
| `~/.local/state/llm-brain/hub.bleve` | The search index. |
| `~/.local/state/llm-brain/rule-modules/` | Clones of git rule modules. |

## Environment variables

| Variable | Used by | Meaning |
| --- | --- | --- |
| `BRAIN_OVERLAY` | all | Overlay folder. |
| `BRAIN_DIR` | scripts | llm-brain checkout (default: the one the script is in). |
| `BRAIN_LOCAL` | scripts | Local config folder (default `~/.config/llm-brain`). |
| `BRAIN_STATE` | scripts | State folder (default `~/.local/state/llm-brain`). |
| `FLEET_WORK_ROOT` | scripts, hub agent | Overrides `work_root`. |
| `LLM_MODEL` | `bin/llm-run` | Model for this run. |
| `BRAIN_HUB_URL` | memory scripts | Hub to talk to. |
| `BRAIN_REPO`, `BRAIN_ISSUE`, `BRAIN_PR` | memory scripts | Scope of the current work. The fleet sets them for each worker. |
| `BRAIN_SESSION`, `BRAIN_AGENT`, `BRAIN_PROVIDER` | memory scripts | Who is writing; `self` is left out of presence. |
| `BRAIN_MEMORY_K`, `BRAIN_MEMORY_TIMEOUT` | `bin/brain-context` | Memories to fetch (8) and timeout in seconds (3). |
| `REVIEW_REPO` | `bin/review-rules` | Default for `--repo`. |
| `TYPESAFE_API_KEY`, `TYPESAFE_BASE_URL`, `TYPESAFE_DEFAULT_MODEL`, `JEV_TIMEOUT` | `bin/jev` | Key, API base, model (`jev-latest`), timeout (60 s). |
