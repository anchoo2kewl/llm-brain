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

# Config layers, later wins: config/fleet.yaml (public defaults) → <overlay>/config.yaml
# (private: people, repos, house rules) → ~/.config/llm-brain/local.yaml (this machine).
# The overlay dir comes from $BRAIN_OVERLAY, else local.yaml's `overlay:`, else ~/projects/llm-brain-overlay.
overlay_dir() {
  local d="${BRAIN_OVERLAY:-}"
  [[ -z "$d" && -f "$BRAIN_LOCAL/local.yaml" ]] && d="$(yq '.overlay // ""' "$BRAIN_LOCAL/local.yaml")"
  d="${d:-$HOME/projects/llm-brain-overlay}"; d="${d/#\~/$HOME}"
  [[ -d "$d" ]] && echo "$d"
}

# cfg '.roles.implementer'  (any yq args)
cfg() {
  local files=("$BRAIN_DIR/config/fleet.yaml") ov
  ov="$(overlay_dir)"
  [[ -n "$ov" && -f "$ov/config.yaml" ]] && files+=("$ov/config.yaml")
  [[ -f "$BRAIN_LOCAL/local.yaml" ]] && files+=("$BRAIN_LOCAL/local.yaml")
  yq eval-all '. as $i ireduce ({}; . * $i)' "${files[@]}" | yq "$@"
}

# house <prompt>: the overlay's extra instructions for a prompt (house/<prompt>.md), or nothing.
house() {
  local ov; ov="$(overlay_dir)"
  [[ -n "$ov" && -f "$ov/house/$1.md" ]] && cat "$ov/house/$1.md"
  return 0
}

# This machine's id is HOST (`hostname -s`): claims, logs and state use it, so it never changes. HOST_NAME is what
# people read (claim comments, notifications, prompts); set `host_name` in ~/.config/llm-brain/local.yaml to change it.
HOST_NAME="${BRAIN_HOST_NAME:-$(cfg '.host_name // ""' 2>/dev/null || true)}"
HOST_NAME="${HOST_NAME:-$HOST}"

# Expand ~ in a config path.
cfg_path() { local p; p="$(cfg "$1")"; echo "${p/#\~/$HOME}"; }

# Variables every prompt can use: who we work for, who the people are, and the overlay's house notes.
WORK_ROOT="${FLEET_WORK_ROOT:-$(cfg_path '.work_root')}"
prompt_env() { # prompt_env <prompt-name>
  export OWNER="$(cfg '.owner.name')" OWNER_GH="$(cfg '.owner.github')" WORK_ROOT
  export OWNER_ALIASES="$(cfg -o=json '.owner.aliases // []' | jq -r 'join(", ")')"
  export PEOPLE="$(cfg -o=json '.people' | jq -r 'to_entries | map("\(.value) (@\(.key))") | join(", ") | if . == "" then "your teammates" else . end')"
  export HOUSE="$(house "$1")"
  [[ -n "$HOUSE" ]] || HOUSE="(none)"
}

# notes_for <owner/repo#n>...: what trusted people said about these in chat (bin/chat-watch), oldest first.
notes_for() {
  local r out=""
  for r in "$@"; do
    local repo="${r%%#*}" f
    f="$BRAIN_STATE/notes/${repo/\//__}__${r##*#}.md"
    [[ -f "$f" ]] && out+="$(cat "$f")"$'\n'
  done
  printf '%s' "${out:-(none)}"
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
    osascript -e "display notification \"${msg//\"/\'}\" with title \"llm-brain ($HOST_NAME)\"" >/dev/null 2>&1 || true
  fi
}

# Render a prompt template: {{KEY}} placeholders replaced from env vars of the same name.
render() {
  local tpl="$1" out key
  # bash 5.2+ turns `&` in a ${x//p/r} replacement into the matched text; values (PR text, feedback) contain `&`.
  shopt -u patsub_replacement 2>/dev/null || true
  out="$(cat "$tpl")"
  for key in $(grep -oE '\{\{[A-Z_]+\}\}' "$tpl" | sort -u | tr -d '{}'); do
    out="${out//\{\{$key\}\}/${!key:-}}"
  done
  printf '%s' "$out"
}
