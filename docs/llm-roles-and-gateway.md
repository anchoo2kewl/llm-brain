# LLM roles and the account gateway

Which LLM does which job, how every vendor CLI is driven through one adapter, and how subscriptions are shared through CLIProxyAPI.

## Providers

A provider is one LLM CLI: `claude`, `codex` or `gemini`. It is available on a machine when `providers.<p>.enabled` is true and `providers.<p>.cli` is on `PATH`. Check with:

```sh
bin/fleet providers
```

## Roles

| Role | Config | Does |
| --- | --- | --- |
| Implementer | `roles.implementer` | Implements issues and babysits PRs. |
| Internal reviewer | `roles.reviewer` | Reviews the implementer's PRs (`fleet review`), read-only. |
| PR reviewer | `pr_review.provider` | Writes reviews of other people's PRs. |

The implementer falls back to the first available provider when the configured one is missing.

The internal reviewer is always a different provider from the implementer when a second one is available. With only one provider, it reviews itself in a fresh session with no shared context.

## bin/llm-run

```sh
bin/llm-run <provider> <mode> <cwd> <prompt-file> [session-name]
```

The only vendor-specific code in the fleet. Modes:

| Mode | claude | codex | gemini |
| --- | --- | --- | --- |
| `write` | `-p … --dangerously-skip-permissions` | `exec --dangerously-bypass-approvals-and-sandbox` | `-p … --yolo` |
| `review` | `--permission-mode plan`, tools limited to Read, Grep, Glob, `git diff/log/show`, `gh pr view/diff` | `exec --sandbox read-only` | `-p …` |

The model is `$LLM_MODEL`, else `providers.<p>.model`, else the CLI's default. The fleet explicitly defaults Claude to `sonnet`, so an interactive Opus/1M preference does not silently apply to automation. `pr_review.model` optionally overrides the model for external PR review workers.

Before running, it:

- adds the shared-memory context pack and the `brain-remember` instructions when `memory.enabled` is true ([Shared memory](memory.md));
- routes through the gateway when `gateway.enabled` is true, `CPA_API_KEY` is set in `gateway.env_file`, and `<gateway.url>/v1/models` answers within 3 s. Otherwise it logs "gateway unreachable" and uses the CLI's own login.

Through the gateway, Claude gets `ANTHROPIC_BASE_URL` and `ANTHROPIC_AUTH_TOKEN`, and Codex gets a `cpa` model provider (`<gateway>/v1`, `wire_api="responses"`). Gemini always uses its own login.

## The account gateway

[CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) holds every subscription login (personal and work). Workers, hub tasks and plain chats talk to it instead of each CLI's own login.

Setup is in CLIProxyAPI's own docs. llm-brain needs:

| Item | Where |
| --- | --- |
| Gateway URL | `gateway.url` (default `http://127.0.0.1:8317`) and the hub's `--gateway` flag |
| Client key `CPA_API_KEY` | `~/.config/llm-brain/cpa.env`. Used by `llm-run`, hub tasks and plain chats. |
| Management key `CPA_MGMT_KEY` | Same file. Used by the hub to read quota and usage. |

### Personal first, work second

Give personal accounts a higher `priority` than work ones (for example 10 and 0) and use `fill-first` routing. Work accounts are then used only while personal ones are cooling down after a 429 or quota hit, and personal takes over again after its reset.

Keep `routing.session-affinity` off when a recovered personal credential should regain priority on the next
request, including in an existing session. Read reset/cooldown times from the gateway's account data rather
than scheduling a weekly priority flip. A cooldown ending does not require changing or rewriting credentials.

Normal terminal Claude sessions using their own login are outside this gateway policy. Routing them through
`ANTHROPIC_BASE_URL` plus gateway credentials enables the same failover, but disables Claude Remote Control
and built-in dictation while those gateway credentials are active; see
[Anthropic's gateway surface notes](https://code.claude.com/docs/en/llm-gateway-connect#slack-cloud-sessions-and-remote-control).

Stop the gateway before you edit a credential file. A live rewrite can invalidate or resurrect a spent refresh token.

### What the hub shows

With `CPA_MGMT_KEY` set, `hub serve` polls `/v0/management/auth-files` and `/v0/management/usage-queue` every 20 s. It shows:

- each account's quota windows (for example 5-hour and weekly) and how much is left;
- which account is serving now;
- the account, model, tokens and latency of every call.

These appear on the fleet overview and on [Insights](hub-insights.md). Without the key, the hub logs "account quota/routing disabled" and skips it.

### Hub terminals and the gateway

Hub terminals use the CLI's own login by default. Set `terminals.gateway: true` to send interactive Claude and Codex terminals through the gateway too. Claude and Codex hub tasks always use the gateway when it answers, the way `llm-run` does. Gemini uses its own login.

For interactive and unattended session budgets, subagent caps, and context limits, see [Claude resource limits](claude-limits.md).
