# Fleet

`bin/fleet` is the issue to PR to merge loop, plus PR review. launchd runs `fleet tick` every 5 minutes.

## A tick

`fleet tick` does, in order:

1. `bin/chat-watch`: turn new team-chat messages into notes ([Team chat](chat.md)).
2. `dispatch`: claim new issues and start implement workers.
3. `babysit`: start a worker for every fleet PR with new activity.
4. `review-prs`: review PRs that request you.
5. `snapshot`: write `status.json` for the hub.

Only one tick runs at a time on a machine. A tick takes a lock in `~/.local/state/llm-brain/tick.lock`. A lock whose process is gone, or that is over an hour old, is taken over.

## Intake

An issue is a candidate when all of these hold:

- It is open in one of `intake.repos` and assigned to `intake.assignee`.
- Its author is in `intake.authors`.
- It was created on or after `intake.created_since`.
- It has none of `intake.skip_labels`, nor the claimed, PR-open or blocked labels.

Candidates are taken oldest first. No opt-in label is needed.

The fleet owns at most `intake.max_in_flight` issues at once (default 2), counted across all machines by the `agent:working` and `agent:pr-open` labels. The next issue starts when one is merged or closed.

An issue is skipped when:

- one of your open PRs in the first `pr_owners` org mentions it (you are working it by hand; `skip_if_my_open_pr`);
- any earlier fleet claim exists on it. Each issue is picked up once, ever;
- this machine has no free worker slot (`limits.max_workers`).

## Claims

To claim an issue, a machine posts a comment with `<!-- llm-brain:claim host=<host> -->`, waits 5 seconds and reads the comments back. The earliest claim comment wins, so two machines racing on one issue resolve the same way. The winner adds `agent:working`.

## Workers

Each worker is `bin/llm-run` under `gtimeout`, started in the background. Its pid is in `runs/<key>.pid` and its output in `logs/<key>.<YYYYmmdd-HHMMSS>.log`.

| Key | Started by | Provider | Prompt | Timeout |
| --- | --- | --- | --- | --- |
| `issue-<n>` | dispatch | implementer | `prompts/implement.md` | `limits.implement_timeout` |
| `pr-<repo>-<n>` | babysit | implementer | `prompts/babysit.md` | `limits.babysit_timeout` |
| `review-<repo>-<n>` | review-prs | `pr_review.provider` | `prompts/pr-review.md` | `limits.review_timeout` |

Implement and babysit workers share `limits.max_workers`. Reviews have their own pool, `limits.max_review_workers`.

Prompts are templates. `{{KEY}}` placeholders are filled from environment variables: owner, people, repo, issue, PR URL, marker, reviewers, chat notes (`{{NOTES}}`) and house notes (`{{HOUSE}}`).

### What an implement worker does

From `prompts/implement.md`:

- Reads the issue and all its comments. The author's words are the spec.
- Works in a fresh worktree per repo under `work_root`, one PR per repo, `#<n>` in every branch, commit and PR title.
- Runs the repo's own checks before every push.
- Ends every PR body with `<!-- llm-brain:issue=<n> -->`, requests `reviewers`, labels the issue `agent:pr-open`.
- Runs `fleet review <pr-url>` (the other LLM) and fixes real findings.
- If there is nothing to build, comments once and labels `agent:done`.
- If it is blocked, asks the exact question and labels `agent:blocked`.

## Babysitting

`fleet babysit` finds your open PRs in the `pr_owners` orgs that carry the marker, or that name an issue the fleet owns (`repo#N` or the issue URL). For each:

- It skips the PR when its issue has `agent:stop`.
- It computes a fingerprint: head SHA, review count, comment count, review decision, merge state, CI states, and a hash of the chat notes. If nothing changed since the last pass, it does nothing.
- Otherwise it starts a babysit worker, which reads every review, thread and CI log, fixes, replies on each thread, re-runs the internal review and requests `reviewers`.

A new chat note from a trusted person changes the fingerprint, so it wakes the babysitter.

## Merge policy

The babysitter merges only when all of these hold (from `prompts/babysit.md`):

- At least one approval and no outstanding "changes requested". One approval is enough; it does not ask for re-approval after fixes.
- No unresolved threads or unanswered comments.
- Every required check is green, including slow and merge-queue checks.
- The latest internal review found no real defects.
- The target is the default branch.

It never opens or merges staging or production promotion PRs. With `merge.auto: false` it stops at "ready to merge" and notifies you. After a merge it verifies the change where it deploys, comments a summary, removes the labels and closes the issue when all its PRs are merged.

## PR review

`fleet review-prs` reviews PRs that request you. See [PR review](pr-review.md).

## Internal review

`fleet review <pr-url>` is the cross-LLM check workers run on their own PRs. It clones the PR head into a temp folder, runs the reviewer provider read-only with `prompts/review.md`, prints findings (`## Verdict: CLEAN | ISSUES`) and saves them as `findings.md` in that folder. It posts nothing.

## Kill switches

| Scope | How |
| --- | --- |
| Every machine | `paused: true` in the config |
| This machine | `fleet pause` (creates `~/.config/llm-brain/PAUSE`); `fleet resume` removes it |
| One issue | Label `agent:stop`. A trusted person can also say so in chat ([Team chat](chat.md#stopping-an-issue)). |

A paused machine skips dispatch, babysit and review-prs. Running workers are not killed.

## Commands

| Command | What it does |
| --- | --- |
| `fleet tick` | chat-watch, dispatch, babysit, review-prs, snapshot. What launchd runs. |
| `fleet dispatch` | Claim candidate issues and start implement workers. |
| `fleet babysit` | Start a babysit worker for each fleet PR with new activity. |
| `fleet review-prs` | Review every PR that requests or mentions you, once per head commit. |
| `fleet review-pr <url>` | The same for one PR. |
| `fleet review <pr-url>` | Cross-LLM internal review of a PR; prints findings. |
| `fleet status` | Paused or not, roles, running workers, claimed issues, fleet PRs. |
| `fleet prs` | The fleet PRs as JSON (what babysit works from). |
| `fleet snapshot` | Write `status.json` for the hub. |
| `fleet labels` | Create the fleet labels in every intake repo. Run once. |
| `fleet pause` / `fleet resume` | Pause or resume this machine. |
| `fleet providers` | Which providers are available, and who implements and reviews. |

## Labels

| Label | Meaning |
| --- | --- |
| `agent:working` | A worker is implementing it. |
| `agent:pr-open` | A fleet PR is open for it. |
| `agent:blocked` | Waiting on a human decision. Shows in the hub inbox. |
| `agent:stop` | The fleet must not touch it. |
| `agent:done` | Nothing to build; a person needs to check. |

Names come from `labels.*` in the config.

## Status snapshot

`fleet snapshot` writes `~/.local/state/llm-brain/status.json`: host, paused, roles, issues owned, the intake queue (with a "by hand" flag), fleet PRs with review decision and CI state, the last 40 review verdicts, running workers, the tail of `fleet.log` and the chat digest. The hub reads this file and never calls GitHub itself. It is served at `/status.json`.

## Limits of multi-repo intake

Several intake repos work for dispatch. Some lookups use only the first entry: babysit looks up a PR's issue in `intake.repos[0]`, and the "open PR by hand" check searches only `pr_owners[0]`. Keep the main repo first.
