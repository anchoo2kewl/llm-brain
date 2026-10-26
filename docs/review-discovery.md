Review discovery uses `owner.github`, independently of the CLI account's `@me` shorthand. It searches all open PRs assigned to, mentioning, or requesting the configured owner, plus review requests for the owner's teams. It deliberately ignores legacy `pr_review.updated_since` values. Repository scope, draft exclusion and self-review exclusion still apply; discovering an item does not authorize reviewing, merging or deploying it.

The read-only helper accepts merged config JSON on stdin and emits one complete candidate array. It paginates each search and team membership list. GitHub's 1,000-result search cap is handled by splitting creation-time ranges; incomplete, changing-length or unsplittable results fail discovery rather than silently dispatching a partial queue. Search indexing and concurrent GitHub changes still mean a subsequent run may discover additional items.

By default team discovery requires authenticating as the configured owner with permission to list their teams (normally `read:org`). For a different authentication identity, disable `pr_review.discover_teams` and explicitly configure every applicable `org/team` slug in `pr_review.teams`. An empty explicit list means no team requests will be searched in that mode.

Validate configuration without launching workers:

```bash
source bin/lib.sh
cfg -o=json '.' | bin/review-discovery
```

Deploy the scripts and config together after review. No Hub rebuild is needed for this change. Preserve existing pause state and scheduler state. A normal fleet tick also dispatches and babysits; enabling its scheduler is a separate operational decision. A review-only invocation uses `bin/fleet review-prs` and retains pause, capacity, budget and same-head gates. Obtain authorization before re-enabling automation stopped by an owner cancellation.

An opt-in review-only LaunchAgent template is provided at `launchd/com.llm-brain.reviews.plist`. It runs only `bin/fleet review-prs` every five minutes, with a separate `reviews.log`. Launchd serializes executions of this service; worker capacity and persisted same-head/budget-stop gates remain in the existing command. It does not refresh chat intake, dispatch issues or babysit PRs. Bootstrap does not install it automatically.

For recovery, first review and deploy the discovery code and the private scope configuration together. Set `pr_review.repo_regex: ".*"` in the private overlay if all repositories under the configured owners should be included. Validate discovery read-only and check authenticated-owner team permissions. Then, only with authorization to resume reviews, render the template's `__BRAIN__` and `__HOME__` placeholders to the deployment checkout and home directory, validate it with `plutil -lint`, and install it as `~/Library/LaunchAgents/com.llm-brain.reviews.plist`. Loading that service starts reviews immediately because `RunAtLoad` is true. Leave `com.llm-brain.fleet` unloaded; do not run the general bootstrap as part of this recovery. Preserve PAUSE/config pause settings; do not clear them to force a start. To stop review scheduling, boot out only `gui/<uid>/com.llm-brain.reviews`; already-running review workers need separate handling.
