# AI Slop Score

A 0-100 score for how hard a pull request is to understand, trust and maintain. 0 is pristine. The review agent posts it on every review, and a blocked score stops approval.

The score is about the change, not about who or what wrote it. A clear AI-written PR scores 0; a rambling human one doesn't.

## What it looks like

Every review the fleet posts ends with a section like this:

```
### AI Slop Score: 46/100 · blocked
Blocks at 35, warns at 15. Profile `elastio-v1`, Tier 1 + Jev. How this is scored

| Points | What                                       | Where                                              |
|-------:|--------------------------------------------|----------------------------------------------------|
|    8.0 | S05 Should be split · Scope                | independent changes are bundled into one PR (Jev 0.90) |
|    6.4 | P03 How to verify · PR clarity             | no How to verify section                           |
|    6.4 | P12 Long description · PR clarity          | 765 words; the budget for this change is 324       |
|    5.0 | Z02 Several changes in one title · Scope   | the title lists 6 changes                          |

To fix
- [ ] Split the independent parts into their own PRs. (S05)
- [ ] Add `## How to verify on dev`: numbered steps ... (P03)
```

A re-review adds what changed: `Was 46 at 1a2b3c4d. Fixed: P03, P12.`

## How the score is computed

1. **Signals.** A profile lists signals. Each one measures one thing, like "the description is longer than its budget" or "a new `console.log`". It gives a severity from 0 to 1:
   - *binary*: 1 if it fires at all.
   - *count*: `c / (c + k)`. One hit is half, more hits approach 1.
   - *density* (code signals): `c/(c+k) × max(0.5, min(1, 1000·c / (r · max(lines, 100))))`, per file, and the worst file counts. One slip in a small file costs little; the 100-line floor protects tiny PRs; padding a file with clean lines can at most halve it.
   - *ratio*: the detector says how far over the limit it is (a 147-character title against a 72 limit is 1.0).
   - *judged*: Jev answers a yes/no question with a probability, asked 3 times. Below 0.70 counts as no; 0.70 to 0.95 scales to 0..1, and 2 of the 3 answers must be at least 0.70. For per-unit questions (each comment, each hunk), several hits add up but one alone counts less.
2. **Points.** `weight × severity`, capped at the signal's `cap` (default: its weight).
3. **Category caps.** Each category has a cap, applied proportionally, so one kind of problem can't reach 100 alone.
4. **Score.** The sum, capped at 100, rounded.
5. **Verdict.**
   - `pass` below `thresholds.warn` (15).
   - `warn` from 15.
   - `block` from `thresholds.block` (35), but only when at least `min_families` (2) *substantive* categories each bring `min_family_points` (5). PR hygiene isn't substantive, so title format and issue numbers can't block alone. Over 35 without that corroboration stays a warning, and says so.
   - A **veto** signal (a committed secret; for Elastio, AI attribution in blue-stack-ami) blocks whatever the score.

Generated files, vendored code, lockfiles and fixtures are skipped by the code, comment and test signals, and don't count toward size; the secret check still scans them, so a key in a fixture is caught. Test files are skipped by most code signals, but suppressions without a reason, TODOs and commented-out code count there too. Deleted files count toward size at half weight.

Without Jev (no key, or `--no-jev`), the judged signals are listed as not assessed and the score uses the rest. Nothing is guessed.

## The default rules

| Category | Signals |
|---|---|
| PR clarity | How to verify section, title length, description over budget, description that restates the code, private references (`§5.4`, `AC2`, "Vitalii's 194"), session narration, promotional words, placeholders, a missing description. Jev: can a teammate say what and why from the opening; is it a work log; is a behaviour change buried. |
| Scope | Over 800 changed lines; a title listing several changes. Jev: should it be split. |
| Code | Debug output, suppressions without a reason, TypeScript `any`, Rust `.unwrap()`, swallowed Go errors, shell `\|\| true`, Python bare `except`, SQL built from strings, `todo!()`-style unfinished code. |
| Comments and docs | TODOs without a ticket, commented-out code. Jev: comments that narrate the code, docs that contradict it. |
| Tests | No test changes for a large change, tests that assert nothing, skipped tests without a reason. Jev: an untested behaviour change. |
| Structure | Copy-pasted blocks, new helpers nothing calls. Jev: abstractions nobody needs, failures turned into success. |
| PR hygiene | Conventional title, issue number everywhere, one closing issue, no `fixup!`/`wip` commits, no vague commit subjects, no secrets (veto). |

`bin/slop-score rules` prints the full table for a profile: what each rule checks, its points, how to fix it. Share that output with your team; it is how the Elastio rules gist is made.

Profiles can set `rules_url` to the published output of `slop-score rules`. Every score then links to the
company's complete rules, and each finding and checklist item links to that rule's `#rule-<lowercase ID>`
anchor. Keep `docs_url` for the general scoring explanation. The generated guide includes a pre-review
checklist, exemptions, company overrides with remedies, and all inherited rules; sharing only the overlay
JSON would omit those inherited checks. Refresh the published guide whenever either profile changes.

## Profiles

A profile is a JSON file. llm-brain ships `slop-profiles/default.json`. A company keeps its own in its overlay, `<overlay>/slop/<name>.json`, and picks it in the overlay `config.yaml`:

```yaml
slop:
  profiles: [acme, default]     # the first whose `when` matches the repo is used
```

A profile `extends` others. Maps merge by key, everything else replaces, so a company profile only lists what it changes:

```json
{
  "schemaVersion": 1,
  "id": "acme-v1",
  "title": "Acme",
  "extends": ["default"],
  "when": {"repos": ["acme/*"]},
  "skip": {"titles": ["^Release \\d"], "base_branches": ["production"]},
  "thresholds": {"warn": 15, "block": 30},
  "signals": {
    "P02": {"enabled": true},
    "K04": {"weight": 10, "params": {"patterns": {"rust": ["\\.unwrap\\(\\)", "\\.expect\\("]}}},
    "A01": {
      "title": "No direct prod hostnames", "category": "code", "detector": "regex_count", "norm": "count", "weight": 6,
      "what": "Code never hardcodes a production hostname.",
      "params": {"sources": ["added_code"], "patterns": ["\\bprod\\.acme\\.internal\\b"], "k": 1},
      "remedy": "Read the host from config."
    },
    "A02": {
      "title": "Feature flag missing", "category": "code", "detector": "jev", "norm": "judged", "weight": 12, "tier": 2,
      "what": "New user-facing behaviour ships behind a flag.",
      "params": {"unit": "hunk", "finding": "new behaviour without a flag",
                 "question": "Does this hunk add user-facing behaviour with no feature flag around it?",
                 "yes": "New behaviour runs for everyone with no flag check.", "no": "It is behind a flag, or not user-facing."},
      "remedy": "Put the new behaviour behind a flag."
    }
  }
}
```

### Signal fields

| Field | Meaning |
|---|---|
| `title`, `what`, `remedy` | Shown in the review and in `slop-score rules`. `what` says what is checked, `remedy` how to fix it. |
| `category` | A key of `categories`. |
| `detector` | A built-in detector (below), `regex_count` for your own patterns, or `jev` for a judged question. |
| `norm` | `binary`, `count`, `density`, `ratio`, `judged` or `veto`. |
| `weight`, `cap` | Maximum points; `cap` defaults to `weight`. |
| `enabled` | `false` turns an inherited signal off. |
| `when` | Same as review-rule modules: repo globs, file globs, fuzzy keywords in the added lines, `min` hits. |
| `exempt`, `only` | File classes to skip or to limit to: `G` generated/vendored/lockfiles, `T` tests, `F` fixtures, `D` docs. |
| `params.paths`, `params.exclude` | File globs the signal looks at. |
| `params` | Detector settings, e.g. `patterns`, `k`, `r`, `max_chars`. |

`regex_count` sources: `pr.body`, `pr.title`, `branch`, `commits.subject`, `commits.message`, `added_text`, `added_code` (strings stripped, comments removed), `added_comments`. `"match": "absent"` fires when nothing matches. `"include_excluded": true` scans generated and test files too.

Jev signals take `params.unit`: `pr` (the title, description, file list, commit subjects and a few hunks), `comment` (each added comment block, with the code after it), or `hunk` (each hunk with real code). At most `judging.max_units` units are asked per kind, picked by a stable hash, and each request stays under 24 KB. Credentials (including whole PEM private-key blocks) are found first and then blanked everywhere: in what is sent to Jev, in other signals' evidence, and in the posted section.

Built-in detectors: `title_format`, `issue_token`, `verify_section`, `title_length`, `body_length`, `code_heavy_prose`, `hype_words`, `thin_body`, `issue_ambiguity`, `fixup_commits`, `vague_commits`, `secrets`, `pr_size`, `title_multi`, `lang_regex`, `suppressions`, `go_swallowed_err`, `py_bare_except`, `sql_interp`, `todo_no_issue`, `commented_code`, `front_matter`, `no_tests`, `skipped_tests`, `duplicate_blocks`, `unused_private`. Their parameters are in `slop-profiles/default.json`.

`bin/slop-score validate <profile>` checks a profile: unknown categories or detectors, bad regexes, missing Jev questions, thresholds out of order.

## Waivers

When a rule is wrong for one spot, put the waiver on that line or one of the two lines above it:

```ts
// slop:allow K01 the CLI prints its result to stdout
console.log(result);
```

For PR-level rules, put it in the description: `<!-- slop:allow P12 release notes need the full list -->`. The reason must be at least 12 characters (`waivers.min_reason_chars`). Every waiver is listed under the review's table. Vetoes can't be waived. The fleet's own agents are told never to add waivers; people grant them.

## In the fleet

- **Reviews** (`fleet review-prs`): each PR head is scored before the reviewer starts. The section goes into the review verbatim, every time. A `block` means the event is `COMMENT`, never `APPROVE`. Scores are kept in `~/.local/state/llm-brain/slop/<pr>/<sha>.json` for the re-review delta.
- **Commit status**: with `slop.post_status` (and `pr_review.post`), the commit that was scored gets an `ai-slop-score` status: `failure` when blocked, `success` otherwise. If the author pushed while it was scoring, only the scored commit gets it; the new head is scored on the next tick. Make it a required check in branch protection to block merges for real.
- **The fleet's own PRs**: the implement and babysit prompts run `slop-score --post-status` after each push and fix what it lists until it is not blocked. Babysit won't merge a blocked PR. The implement prompt carries a description template that passes by design: 1-2 sentences of what and why, at most 5 change bullets, behaviour changes, how to verify, what wasn't tested.
- **PRs with no review on their current head** get the section as one PR comment instead, edited in place on each new head: the fleet's own PRs (every tick), and PRs it reviewed before the score existed (`slop.backfill_per_tick` of them per tick).
- `fleet slop <pr-url>` scores any PR, open or merged, and prints the section; `--post` also shows it on the PR (comment + status).

## The command

```
bin/slop-score --base origin/main --pr https://github.com/acme/api/pull/12     # inside a checkout of the PR head
bin/slop-score --base origin/main --title "fix(x): y" --body-file pr.md         # before the PR exists
bin/slop-score --base origin/main --no-jev --json                               # deterministic only, machine output
bin/slop-score rules --profile elastio                                          # the rules as markdown
```

| Flag | Meaning |
|---|---|
| `--base REF` | What to compare HEAD with (the merge base is used). Required. |
| `--pr URL` | Read title, description and branch from GitHub. |
| `--title`, `--body-file`, `--branch` | The same, by hand. |
| `--profile NAME\|PATH` | Use this profile instead of `slop.profiles`. |
| `--no-jev` | Deterministic signals only. |
| `--no-jev-cache` | Request fresh Jev samples; bypass the successful-sample cache. |
| `--previous FILE` | An earlier `slop.json`, to report what changed. |
| `--out-dir DIR` | Write `slop.json`, `slop.md` and the Jev requests there. |
| `--json`, `--markdown` | What to print (markdown by default). |
| `--post-status` | Set the `ai-slop-score` commit status (needs `--pr`). |

Exit status: 0 pass or warn (or skipped), 1 blocked, 2 error.

Tests: `python3 tests/slop_score_test.py` builds throwaway repos with known slop in Rust, Go, TypeScript, Python and shell and checks each detector. It needs Python 3.13 or newer.

## Calibrating

Start with the score posted but not required. Run `fleet slop` over the last few weeks of merged PRs; aim for well under 5% of PRs your team was happy with being blocked, and check that the ones you weren't happy with are. Tune detectors before weights: a noisy signal should be narrowed (`paths`, `exclude`, patterns) or turned off. When the profile changes, a re-review says so, so a lower score isn't mistaken for a better PR.

On blue-stack-uiv3, the first calibration (10 PRs from 2026-10-15/16) blocked the two PRs a reviewer had called hard to read (#877 at 46, #891 at 36). It warned on five that were long but clear, and passed three.

## Conserving judge and review tokens

Deterministic checks run locally and do not consume Claude tokens. Tier 2 calls Jev directly; it does not
launch a Claude agent. Identical successful Jev samples are cached for 24 hours (`judging.cache_ttl_s: 86400`;
`0` disables caching). Set `BRAIN_SLOP_CACHE_DIR` to relocate the cache; otherwise it uses
`$XDG_CACHE_HOME/llm-brain/slop-jev` or `~/.cache/llm-brain/slop-jev`.

The key includes the full redacted evidence packet, questions, judge model, endpoint, adapter code, and repeat
index. Three repeats remain three independent samples; one cached answer is never multiplied into three votes.
Changed evidence/questions/model invalidate the cache, and expired, partial, invalid or failed responses are
not reused. Cache files contain only timestamps and validated probabilities, not source code or credentials.
File locks prevent concurrent scorers from paying for the same sample twice. `jev_cache` in the score JSON
reports cache hits and request attempts. All deterministic checks are rerun, including secrets and waivers;
a head SHA alone is never treated as proof that the score is still current.

The scorer disables hidden HTTP POST retries for its judge calls (`JEV_RETRIES=0`) and terminates the whole
judge process group on timeout. Failures remain explicitly unassessed. The score thresholds and majority rule
are unchanged. Use `--no-jev-cache` when a fresh assessment is needed. `bin/jev` keeps its normal default retry
policy for other callers; `JEV_RETRIES` can override it explicitly.

New fleet reviews enclose their generated score in `<!-- llm-brain:score:start -->` and
`<!-- llm-brain:score:end -->`. Future review prompts omit only our old marked score blocks and our generated
standalone score comments, since the current head's full score is supplied separately. Findings, inline comments,
human follow-ups outside the markers, other reviewers' feedback, and legacy unmarked reviews are retained.
The public score remains complete; this trims repeated machine output from subsequent model input.

Offline regression checks: `python3 tests/slop_cache_test.py` and `python3 tests/review_feedback_test.py`.
