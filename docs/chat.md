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

A message from a trusted person that names an issue or PR becomes a note on it, in `~/.local/state/llm-brain/notes/<owner>__<repo>__<n>.md`. Implement and babysit workers get those notes in their prompt and treat them like that person's review comments. The latest note wins when two disagree. A new note changes the PR's fingerprint, so it wakes the babysitter.

Messages from anyone else are only used for the digest.

### How references are read

| In the message | Means |
| --- | --- |
| `https://github.com/acme/web/pull/12` | `acme/web#12` |
| `web#12`, `api#7` | The known repo whose name is `web`, ends in `-web`, or contains `web`; the shortest match wins. |
| `#123` | The repo an earlier explicit ref with the same number used; else the channel's repo from `chat.channel_repos`. |

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
