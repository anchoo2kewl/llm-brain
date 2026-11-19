# Migration service folder options

`optional-move-folders.patch` contains the complete tested hub/agent implementation and regression tests for optional destination paths and durable folder mappings. It targets the migration-control service at commit `8177346` (which includes terminal move/clone jobs and transcript import). The public desktop source snapshot predates that service API, so this deployment patch keeps its implementation reviewable without replacing unrelated control-plane source.

Apply to that migration-service checkout with `git am`, run `go test ./...` from `hub`, and build `go build -o ../bin/hub .`. Update the source hub and destination agents together. Existing agents that do not acknowledge strict folder selection cause the job to fail before resume; the source stays running.

The database change only adds `move_folders`. Existing moves remain compatible, including the automatic folder fallback when no mapping or explicit path is selected.
