Review discovery uses `owner.github`, independently of the CLI account's `@me` shorthand. It searches all open PRs assigned to, mentioning, or requesting the configured owner, plus review requests for the owner's teams. It deliberately ignores legacy `pr_review.updated_since` values. Repository scope, draft exclusion and self-review exclusion still apply; discovering an item does not authorize reviewing, merging or deploying it.

The read-only helper accepts merged config JSON on stdin and emits one complete candidate array. It paginates each search and team membership list. GitHub's 1,000-result search cap is handled by splitting creation-time ranges; incomplete, changing-length or unsplittable results fail discovery rather than silently dispatching a partial queue. Search indexing and concurrent GitHub changes still mean a subsequent run may discover additional items.

By default team discovery requires authenticating as the configured owner with permission to list their teams (normally `read:org`). For a different authentication identity, disable `pr_review.discover_teams` and explicitly configure every applicable `org/team` slug in `pr_review.teams`. An empty explicit list means no team requests will be searched in that mode.

Validate configuration without launching workers:

```bash
source bin/lib.sh
cfg -o=json '.' | bin/review-discovery
```

Deploy the scripts and config together after review. No Hub rebuild is needed for this change. Preserve existing pause state and scheduler state. A normal fleet tick also dispatches and babysits; enabling its scheduler is a separate operational decision. A review-only invocation uses `bin/fleet review-prs` and retains pause, capacity, budget and same-head gates. Obtain authorization before re-enabling automation stopped by an owner cancellation.
