# shellcheck shell=bash
# Shared helpers for the llm-brain fleet scripts. Source, don't execute.

BRAIN_DIR="${BRAIN_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
BRAIN_LOCAL="${BRAIN_LOCAL:-$HOME/.config/llm-brain}"
BRAIN_STATE="${BRAIN_STATE:-$HOME/.local/state/llm-brain}"
HOST="$(hostname -s)"
mkdir -p "$BRAIN_LOCAL" "$BRAIN_STATE"/{runs,prs,logs,reviews}

export PATH="/opt/homebrew/bin:/usr/local/bin:$HOME/.local/bin:$PATH"

log() { printf '%s [%s] %s\n' "$(date '+%F %T')" "$HOST" "$*" >&2; }
die() { log "ERROR: $*"; exit 1; }

# cfg '.roles.implementer' — reads the shared config with local overrides on top.
cfg() {
  local local_cfg="$BRAIN_LOCAL/local.yaml"
  if [[ -f "$local_cfg" ]]; then
    yq eval-all '. as $i ireduce ({}; . * $i)' "$BRAIN_DIR/config/fleet.yaml" "$local_cfg" | yq "$@"
  else
    yq "$@" "$BRAIN_DIR/config/fleet.yaml"
  fi
}

paused() {
  [[ -f "$BRAIN_LOCAL/PAUSE" ]] || [[ "$(cfg '.paused')" == "true" ]]
}

provider_available() {
  local p="$1"
  [[ "$(cfg ".providers.$p.enabled")" == "true" ]] && command -v "$(cfg ".providers.$p.cli")" >/dev/null 2>&1
}

available_providers() {
  local p
  for p in $(cfg '.providers | keys | .[]'); do
    provider_available "$p" && echo "$p"
  done
}

implementer() {
  local p; p="$(cfg '.roles.implementer')"
  provider_available "$p" && { echo "$p"; return; }
  available_providers | head -1
}

# Reviewer is a different provider whenever one exists; same provider only
# when it is the only one available.
reviewer() {
  local impl want p
  impl="$(implementer)"
  want="$(cfg '.roles.reviewer')"
  if [[ "$want" != "$impl" ]] && provider_available "$want"; then echo "$want"; return; fi
  for p in $(available_providers); do
    [[ "$p" != "$impl" ]] && { echo "$p"; return; }
  done
  echo "$impl"
}

running_workers() { # running_workers [work|review|all]
  local kind="${1:-all}" n=0 f pid k
  for f in "$BRAIN_STATE"/runs/*.pid; do
    [[ -e "$f" ]] || continue
    k="$(basename "$f" .pid)"
    case "$kind" in work) [[ "$k" == review-* ]] && continue ;; review) [[ "$k" == review-* ]] || continue ;; esac
    pid="$(cat "$f")"
    if kill -0 "$pid" 2>/dev/null; then n=$((n + 1)); else rm -f "$f"; fi
  done
  echo "$n"
}

worker_running() { # worker_running <key>
  local f="$BRAIN_STATE/runs/$1.pid"
  [[ -f "$f" ]] && kill -0 "$(cat "$f")" 2>/dev/null
}

notify() {
  local msg="$1"
  log "notify: $msg"
  if [[ "$(cfg '.notify.macos')" == "true" ]] && command -v osascript >/dev/null; then
    osascript -e "display notification \"${msg//\"/\'}\" with title \"llm-brain ($HOST)\"" >/dev/null 2>&1 || true
  fi
}

# Render a prompt template: {{KEY}} placeholders replaced from env vars of the same name.
render() {
  local tpl="$1" out key
  out="$(cat "$tpl")"
  for key in $(grep -oE '\{\{[A-Z_]+\}\}' "$tpl" | sort -u | tr -d '{}'); do
    out="${out//\{\{$key\}\}/${!key:-}}"
  done
  printf '%s' "$out"
}
