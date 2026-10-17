# Overlay

The overlay is a private folder with everything about you. The public repo holds only general defaults.

## Layout

```
llm-brain-overlay/
  config.yaml              # merged over config/fleet.yaml
  repos.txt                # repos to clone into work_root
  house/<prompt>.md        # extra instructions for one prompt
  rules/<name>/rule.json   # private review rule modules
```

Start from `examples/overlay/` and keep the result in a private repo.

## Where it is found

In this order:

1. `$BRAIN_OVERLAY`
2. `overlay:` in `~/.config/llm-brain/local.yaml`
3. `~/projects/llm-brain-overlay`

A leading `~` is expanded. If the folder does not exist, there is no overlay and only the defaults apply.

## config.yaml

Same keys as `config/fleet.yaml`, merged on top of it. `~/.config/llm-brain/local.yaml` is merged on top of this. See [Configuration](configuration.md) for every key.

The example sets the usual personal values:

```yaml
owner: {name: Sam, github: sam-dev, notify: ""}
people: {mona-l: Mona, octo-b: Octo}
intake:
  repos: [acme/platform]
  assignee: sam-dev
  authors: [mona-l, octo-b]
  created_since: "2026-01-01"
pr_owners: [acme]
reviewers: [octo-b]
pr_review:
  owners: [acme]
  extra_rule_modules: [acme-house]
chat:
  enabled: true
  trusted: {U0000000002: teammate-a}
  channel_repos: {handoff: acme/web}
```

## repos.txt

One repo per line: a folder name and a git URL. Blank lines and `#` comments are skipped.

```
platform git@github.com:acme/platform.git
```

`bootstrap.sh` clones each into `work_root/<name>` unless it is already there. `bin/chat-watch` also reads the GitHub URLs here so short refs like `platform#12` resolve.

## house/<prompt>.md

Extra instructions added to one prompt, in place of its `{{HOUSE}}` placeholder. The file name matches the prompt in `prompts/`:

| File | Added to |
| --- | --- |
| `house/implement.md` | Implement workers |
| `house/babysit.md` | Babysit workers |
| `house/pr-review.md` | PR reviews |
| `house/chat-insights.md` | The chat digest |

`prompts/review.md` (the internal cross-review) has no house section.

Example:

```markdown
- PR titles follow Conventional Commits with a scope.
- Run `make preflight` before every push.
```

## rules/<name>/rule.json

A private review rule module, in the same format as the built-in ones. List its name in `pr_review.extra_rule_modules`. `bin/review-rules` looks in `review-rules/` first, then in the overlay's `rules/`. See [PR review](pr-review.md#writing-a-module).
