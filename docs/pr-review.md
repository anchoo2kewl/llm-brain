# PR review

Every PR that requests you (or @-mentions you) gets a short review that leads with security and performance, once per head commit, until it approves.

## Which PRs

`fleet review-prs` runs on every tick. For each org in `pr_review.owners` it searches open PRs where you are a requested reviewer or are mentioned, updated since `pr_review.updated_since`. It then drops:

- drafts;
- PRs authored by `intake.assignee` (your own);
- repos whose name doesn't match `pr_review.repo_regex`.

`fleet review-pr <url>` does the same for one PR, and still refuses your own.

Nothing runs when `pr_review.enabled` is false or the machine is paused.

## Once per head commit

The reviewed SHA is saved in `prs/review-<repo>-<n>.reviewed`. A PR is reviewed again only when its head moves. A re-review is told the previous SHA and focuses on `git diff <last>..HEAD` and on whether its earlier points were fixed.

If the last review's log has no `REVIEWED ` line (the worker died before posting), it is retried, up to 3 times per head commit.

## The flow

For each PR the fleet:

1. Clones the head branch (`--filter=blob:none`) into `reviews/<key>/src` and fetches the base.
2. Writes `existing.md`: every review, inline comment and PR comment already on the PR, from people and bots.
3. Renders `prompts/pr-review.md` and starts `pr_review.provider` in that folder.

The reviewer then:

1. Reads `existing.md`, the PR, CI and linked issues.
2. Lists reviewable files with `ocr delegate preview --format json --from <base> --to HEAD`.
3. Gets the rules with `bin/review-rules --repo <owner/name> --base <base> <files…>`.
4. Reviews each file's diff, reading surrounding code and paired PRs where there is a cross-repo contract.
5. Keeps only findings with a concrete failing scenario and an exact `path:line`.
6. Drops anything already said in `existing.md`, even in other words.

Focus order: security (OWASP Top 10), then performance, then correctness.

With `pr_review.post: false` it is a dry run: the review JSON goes to `reviews/<key>/review.json` and the rendered review to `review.md`, and nothing is posted.

## Red and Optional

| Class | Blocks approval | What |
| --- | --- | --- |
| Red | Yes | Critical or high: bugs, security, tenant or RBAC leaks, data loss, migrations unsafe at production size, a stated outcome not delivered, UI copy that contradicts the API, a real CI failure. |
| Optional | No | Medium or low. At most 3; the rest are dropped. |

The event is `APPROVE` when there are no Red items (the reviewer's own, or others' that are still open and real) and CI has no real failure. Otherwise `COMMENT`. It never uses `REQUEST_CHANGES`.

The [AI Slop Score](slop-score.md) adds a taste and PR-hygiene score to every review and blocks approval above a threshold.

## Output format

Short and plain, like a note to a teammate. Each item says what breaks, gives an example and the fix. Security items carry their OWASP id, e.g. `(A01)`.

When there is nothing new, one line:

```
Approving. Nothing to add to <who>'s review.  Files reviewed: X/Y.
Approving. Nothing found.  Files reviewed: X/Y.
Not approving yet: <who>'s point on `path:line` is still open.
```

Otherwise a body of about 100 words:

```
<optional one sentence: what was verified, or which earlier items are fixed>

**Blocking**
- `path:line` — one line.

**Optional**
- `path:line` — one line.

Approving. | Not approving yet: N blocking.  Files reviewed: X/Y.
```

Red items are always inline comments; Optional items are inline when on a diff line. An inline comment starts with `**🔴 Blocking** · <bug|security|data|contract|test>` or `**Optional** · <category>` and may carry a `suggestion` block. Lines the API rejects move into the body.

The review is posted with `gh api repos/<nwo>/pulls/<n>/reviews` against the reviewed SHA. The worker's last line is:

```
REVIEWED <url> <APPROVE|COMMENT> red=<n> optional=<n> files=<x>/<y>
```

The hub and `status.json` read verdicts from that line.

## Rule modules

`pr_review.rule_modules` plus `pr_review.extra_rule_modules` list the modules. Every matching rule of every applicable module is used: a union, unlike open-code-review's first match.

| Module | Applies | Checks |
| --- | --- | --- |
| `security` | Always | OWASP Top 10 (A01 to A10), path traversal, TOCTOU, mass assignment; plus performance: N+1, missing indexes, unbounded results, missing timeouts, frontend polling. |
| `ocr` | Always | [open-code-review](https://github.com/alibaba/open-code-review) language rules and the reviewed repo's own `.opencodereview/rule.json`, via `ocr delegate rule`. |
| `multitenant` | Diff mentions tenant, org_id, organization, account_id, workspace, rbac, role, scope or permission | Tenant and role binding, cache keys, runtime feature flags, background jobs carrying the tenant. |
| `migrations` | Files under `migrations/`, `*.sql`, `schema*`, or diff mentions ALTER TABLE, CREATE INDEX, ADD COLUMN, backfill | No backfills in deploy-time migrations, concurrent indexes, deploy order, indexes for hot columns. |
| `contracts` | Always | API, UI and mock agree; description matches code; issue outcomes delivered; LIMIT keeps the right rows; lifecycle edges; tests pin the risky guard. |

A module is found by name in `review-rules/<name>/`, then in `<overlay>/rules/<name>/`. A module can also be a git repo: `{name: x, git: <url>, path: <dir inside>}`. It is cloned to `~/.local/state/llm-brain/rule-modules/<name>` and pulled on every run.

## Writing a module

A module is a folder with an OCR-format `rule.json`:

```json
{
  "name": "migrations",
  "description": "Schema changes that must survive production-sized tables.",
  "when": { "repos": ["*api*"], "files": ["**/migrations/**"], "keywords": ["ALTER TABLE", "backfill"], "min": 1 },
  "exclude": ["**/testdata/**"],
  "rules": [
    { "path": "**/*.sql", "rule": "- No data backfill inside a deploy-time migration." }
  ]
}
```

| Field | Meaning |
| --- | --- |
| `name` | Module name. |
| `description` | For people; not used by the tool. |
| `when` | When the module applies. No `when` means always. |
| `exclude` | File globs this module never applies to. |
| `rules[].path` | File glob a rule applies to. |
| `rules[].rule` | The text the reviewer gets. |
| `rules[].when` | Optional. Same shape as the module's, checked per file. |

### Fuzzy `when`

Each matching repo glob, file glob or keyword counts as one hit. The module applies with at least `min` hits (default 1).

- `repos`: globs against the repo name and against `owner/name`.
- `files`: globs against the changed files. `**/x` also matches `x` at the root. Braces work: `**/*.{ts,tsx}`.
- `keywords`: looked up in the added lines of the diff. Case, `_`, `-` and spaces are ignored, and they match inside words, so `tenant` hits `tenant_id`, `TenantName` and `multi-tenant`.

Globs are case-insensitive. Keywords need `--base`, since they are matched against the diff.

## bin/review-rules

```sh
bin/review-rules [--repo owner/name] [--base REF] <file>...
```

Run it inside the repo under review. It prints the modules that applied, then the rules grouped by the files that share them:

```
Modules applied: ocr, security, migrations

## Rule group 1
Files: db/migrations/0042_add_col.sql

### [security]
...
```

| Flag | Meaning |
| --- | --- |
| `--repo` | `owner/name`, for `repos` globs. Default `$REVIEW_REPO`. |
| `--base` | Base ref. The diff `<base>...HEAD` feeds keyword matching. |

Try it on a branch:

```sh
cd ~/work/api && ~/projects/llm-brain/bin/review-rules --repo acme/api --base origin/main $(git diff --name-only origin/main...HEAD)
```

If `ocr` is not installed, the `ocr` module is skipped with a note on stderr. A module with no `rule.json` is skipped the same way.
