# Troubleshooting

Known gotchas, where to look, and how to get unstuck.

## Where the logs are

All in `~/.local/state/llm-brain/logs/`.

| Log | What |
| --- | --- |
| `fleet.log` | Every tick, chat-watch, dispatch, babysit and review decisions |
| `hub.log` | `hub serve` |
| `hub-ship.log` | `hub ship` on a laptop |
| `<key>.<time>.log` | One fleet worker (`issue-12`, `pr-api-88`, `review-web-12`) |
| `chat-insights.<time>.log` | One chat digest run |
| `agent-<id>.<time>.log` | One hub task turn |

The hub shows all of them on Runs, a task's page and the fleet overview.

## A terminal froze

A CLI in a hub terminal that touches Desktop, Documents, Downloads, Pictures, Music or parts of Library makes macOS ask on that Mac's own screen. Nobody is there, so the CLI waits forever. After 3 minutes the hub marks it blocked and offers fixes ([Frozen terminals](hub-terminals.md#frozen-terminals)).

To avoid it:

- Give the hub agent and tmux Full Disk Access (System Settings, Privacy & Security, Full Disk Access).
- Without that, don't have an agent search the whole home folder (`find ~`, `rg ~`). Search the folders you work in, such as `~/projects`. One such scan froze a terminal for 15 minutes on a prompt nobody could see.

## The fleet does nothing

- `bin/fleet status` says whether this machine is paused. Check `paused:` in the config and `~/.config/llm-brain/PAUSE`.
- `bin/fleet providers` must list at least one available provider.
- `intake.max_in_flight` may be full: `fleet.log` says "in flight: N/N — waiting for a merge".
- An issue is picked up once, ever. If it already has a claim comment, the fleet won't take it again. Remove the claim comment by hand to retry.
- An issue that one of your open PRs mentions is skipped as "worked by hand".
- "tick: previous tick still running — skipping" means a tick holds `tick.lock`. A dead or hour-old lock is taken over on the next tick.

## Babysit doesn't wake up

The babysitter runs only when the PR's fingerprint changes: head SHA, review or comment count, review decision, merge state, CI states or chat notes. A PR without the `<!-- llm-brain:issue=N -->` marker is found only if its title or body names an issue the fleet owns. Add the marker with:

```sh
gh api -X PATCH repos/<owner>/<repo>/pulls/<n> -F body=@body.md
```

`gh pr edit` fails on some repos with a "Projects (classic)" error.

## A PR review never posts

- The review is retried up to 3 times per head commit when its log has no `REVIEWED` line. After that it waits for a new commit.
- `pr_review.post: false` is a dry run; look in `reviews/<key>/review.md`.
- `bin/review-rules` needs Python 3.13 or newer (`PurePosixPath.full_match`). macOS's own `python3` is older.
- If `ocr` is missing, the `ocr` module is skipped; install it with `npm install -g @alibaba-group/open-code-review`.

## fleet labels creates a label called "null"

`fleet labels` still tries to create an opt-in label from `intake.label`, a key the default config no longer has, so `yq` returns `null`. Delete that label, or set `intake.label` in your overlay if you want an opt-in label. Intake does not read it either way.

## Gateway problems

- "gateway <url> unreachable — using this machine's own CLI login" in a worker log: `CPA_API_KEY` is missing from `gateway.env_file`, or `<gateway.url>/v1/models` didn't answer in 3 s.
- No quota on the overview: the hub has no `CPA_MGMT_KEY` in `--gateway-env` (`hub.log` says "account quota/routing disabled").
- Plain chats say the gateway is down: same, for `CPA_API_KEY`.
- Stop the gateway before editing a credential file. A live edit can invalidate its refresh token.

## Terminals don't open

- `tmux is not installed on this Mac`: `brew install tmux`. Bootstrap doesn't install it.
- The Mac must have a live agent (it polled in the last 30 s). Check `/agents` or `/hosts`.
- The CLI must be installed on that Mac.
- If the agent is offline, Close can't end a terminal; the page gives you the `tmux -L llm-brain kill-session -t llmb-<id>` command to run there.

## Tasks refuse a folder

Task folders must be under that Mac's `--roots` (default `work_root` if it exists, else `~/projects`), after resolving symlinks. Terminals accept any folder in the home folder.

## Push notifications don't arrive

Browsers need HTTPS. Run `tailscale serve --bg 8787` on the hub Mac and open the `https://<mac>.<tailnet>.ts.net` address. On iOS, add the app to the home screen first; iOS only notifies installed apps. Settings, Notifications, Send a test, shows each device's last delivery or error.

## Chat-watch is quiet

- `chat.enabled` must be true.
- `fleet.log` says "hub unreachable or chat not connected" when `/settings/slack` has no token or the hub is down.
- Only people in `chat.trusted` create notes. Check the Slack user ids.
- `bin/chat-watch --dry-run` prints what it would see, without acting.

## Memory writes are refused

The hub refuses anything that looks like a credential, a title over 140 bytes, a body over 2,000 bytes, or a bad kind or scope. Rephrase: say where the secret lives, not what it is.

## The database keeps growing

`hub.db` grows with your transcripts and there is no retention yet. The search index (`hub.bleve`) can be deleted while the hub is stopped; it is rebuilt from the database.

## Several intake repos

Dispatch reads every intake repo, but babysit looks up a PR's issue in `intake.repos[0]` and the "open PR by hand" check searches only `pr_owners[0]`. Keep the main repo and org first.
