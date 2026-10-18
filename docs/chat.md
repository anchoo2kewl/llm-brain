# Team chat

The hub reads Slack. A trusted teammate's message about an issue or PR becomes an instruction to the worker on it, and an LLM pass writes you a short digest.

It is read-only on Slack: the hub only calls Slack's read methods and never posts there.

## Connect Slack

1. Create a Slack app with these user scopes:

   ```
   channels:history groups:history im:history mpim:history
   channels:read groups:read im:read mpim:read users:read search:read
   ```

2. Install it to your workspace and copy the user token (`xoxp-…`).
3. Paste it at `/settings/slack`. Or put `SLACK_TOKEN=…` in `~/.config/llm-brain/slack.env`; the hub reads that file once, only while it has no token.

The token is kept in the hub's database. It is never shown again or returned by any API. The settings page shows the connected account and any missing scopes.

## What the hub watches

Pick on `/settings/slack`:

- the channels you tick;
- optionally all your DMs and group DMs;
- optionally mentions of you anywhere (through Slack search).

The hub reads them every 2 minutes (`--chat-interval`, `0` turns chat off) and backfills 24 hours for a newly watched conversation (`--chat-backfill`). Settings changes apply on the next read.

Where messages show up:

- `/chat` groups them by source and thread.
- [Search](hub-search.md) takes `type:chat`, `channel:`, `from:` and `mentions:me`.
- DMs and mentions you haven't answered land in the [inbox](hub.md#inbox).

## bin/chat-watch

Runs first on every fleet tick when `chat.enabled` is true. It reads new messages from the hub (`GET /api/chat/me`, `GET /api/chat/messages?after=<id>`), skips your own, and keeps its cursor in `~/.local/state/llm-brain/chat/state.json`.

```sh
bin/chat-watch            # what the tick runs
bin/chat-watch --dry-run  # print the new messages as JSON; write and act on nothing
```

### Trusted people

`chat.trusted` maps a Slack user id to a GitHub handle (a key of `people`):

```yaml
chat:
  enabled: true
  trusted:
    U0000000002: teammate-a
```

A message from a trusted person that names an issue or PR becomes a note on it, in `~/.local/state/llm-brain/notes/<owner>__<repo>__<n>.md`. Implement, babysit and external PR review workers receive those notes with source links and sender IDs. The owner's explicit decisions take precedence over conflicting teammate proposals; unresolved conflicts must be reported. A new note changes an owned PR's fingerprint, so it wakes the babysitter.

The owner's own instructions are retained too. Set `owner.slack_user_id` to their verified Slack ID (otherwise the connected account is used), `owner.name` to their full name, and `owner.aliases` to names/handles people use to address them. Aliases identify the addressee in already-ingested messages; they never grant a sender trust. Slack's search outside watched channels still uses the account's native @-mention. Continue watching the relevant handoff channels and DMs.

GitHub's object type determines the route: a confirmed PR is review context, including PRs linked through `/issues/N`. A confirmed issue is task context, still subject to intake, assignment, ownership and pause checks; mentioning it does not start a task. Unknown objects remain unresolved. Review requests do not grant permission to merge, tag, deploy or bypass checks.

Concrete new trusted requests (for example, “please review/check/re-run/merge #12”) create a deduplicated review-request token. Slack requests can enter the external review queue without a GitHub reviewer request, within the configured owners/repository scope; closed PRs are archived and drafts wait. A new token permits one bounded same-head feedback pass after a successful prior review. It reads the new context and directly relevant code rather than repeating the whole review. Repeated identical requests and status-only updates do not restart the reviewer. Review capacity, own-PR exclusion, pause checks and resource-stop handoffs still apply. Owned PRs continue through their existing babysitter.

`chat/state.json` records routing receipts with sources and target kinds. `routed` means delivered to notes, **not completed**; `needs_reference` and `needs_resolution` indicate missing scope or unresolved object types. Review request tokens are in `chat/review-requests/`; a dispatched token is recorded in `prs/<review-key>.chat-reviewed`. The worker's final review/log is the evidence of completion.

Messages from anyone else are only used for the digest.

### How references are read

| In the message | Means |
| --- | --- |
| `https://github.com/acme/web/pull/12` | `acme/web#12` |
| `web#12`, `api#7` | A unique known repo by exact name, suffix, then substring. Ambiguous matches are not guessed. |
| `SDK #99` | A unique exact-name/suffix repo alias, including a space before the number. |
| `#123`, `#123/#124` | A prior explicit mapping in the same conversation thread; else the channel's repo from `chat.channel_repos`. Mappings do not leak between channels/threads. |
| `#123-#126` | A small inclusive range in that same context (at most 21 targets). |
| Reply with no reference | Inherits the already-seen trusted parent/thread's targets, if available; otherwise needs a reference. |

Known repos are `chat.repos`, `intake.repos` and the GitHub URLs in the overlay's `repos.txt`.

```yaml
chat:
  channel_repos:
    handoff: acme/web     # a bare #123 in #handoff means acme/web#123
```

### Digest

With `chat.insights: true`, each batch (the last 200 new messages) gets one LLM pass with `prompts/chat-insights.md`, run read-only by `chat.provider` (and `chat.model`) with a 15-minute limit. It writes `~/.local/state/llm-brain/chat/digest.json`: at most 8 short bullets, newest state only, asks of you first. The fleet overview and the inbox show it.

### Stopping an issue

The digest can return one kind of action: `stop`. It is meant for when a trusted person says a fleet issue should stop ("stand down, Sam has it", reassigned, on hold), not for "later" or "after X".

`chat-watch` re-checks that the issue is a fleet issue (it has `agent:working` or `agent:pr-open`). Then it:

1. labels it `agent:stop`;
2. comments on the issue with the reason;
3. sends TERM to its `issue-<n>` worker, if one is running;
4. sends a notification.

The babysitter skips PRs whose issue has `agent:stop`.

## Logs

`chat-watch` writes to `fleet.log`. Each digest run has its own log, `logs/chat-insights.<time>.log`. A digest run that does not end with a valid `INSIGHTS {…}` line is logged and ignored.

After deploying routing changes, re-route already-stored channel feedback without advancing the normal cursor or paying for another digest:

```sh
bin/chat-watch --replay-channel C0123456789 --dry-run  # inspect proposed routes
bin/chat-watch --replay-channel C0123456789            # update notes and request tokens
```

Replay reads only the hub's stored messages and GitHub object metadata. Existing notes are upgraded with source markers instead of duplicated. It does not itself launch workers or send Slack messages; the next normal fleet tick applies the configured workflow gates.
