#!/usr/bin/env bash
# Bootstrap a Mac as an llm-brain fleet machine. Idempotent; safe to re-run.
#   git clone <this repo> ~/projects/llm-brain && ~/projects/llm-brain/bootstrap.sh
# Private settings (people, repos, house rules) live in an overlay dir: see README "Overlay".
# Prereq it cannot do for you: the logins printed at the end.
#   --no-clone   skip cloning the repos listed in <overlay>/repos.txt
#   --no-fleet   don't start the fleet loop or the hub on this machine
#   --shipper    only ship this machine's sessions and logs to the hub (a laptop); loads nothing else
#   --hub=URL    the hub the shipper sends to (default: hub_url from config)
set -euo pipefail
BRAIN="$(cd "$(dirname "$0")" && pwd)"
export PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"
BRAIN_DIR="$BRAIN"; source "$BRAIN/bin/lib.sh"   # cfg, overlay_dir
CLONE=1 FLEET=1 SHIPPER=0 HUB_URL=""
for a in "$@"; do case "$a" in
  --no-clone) CLONE=0 ;; --no-fleet) FLEET=0 ;; --shipper) SHIPPER=1 ;; --hub=*) HUB_URL="${a#--hub=}" ;;
esac; done
[[ -n "$HUB_URL" ]] || HUB_URL="$(cfg ".hub_url")"

load_agent() { # load_agent <label>: render the plist into ~/Library/LaunchAgents and (re)load it
  local a="$1" dst="$HOME/Library/LaunchAgents/$1.plist"
  mkdir -p "$HOME/Library/LaunchAgents" "$HOME/.local/state/llm-brain/logs"
  sed -e "s#__BRAIN__#$BRAIN#g; s#__HOME__#$HOME#g; s#__HUB__#$HUB_URL#g" \
      -e "s#__ORG__#$(cfg '.pr_owners[0] // ""')#g; s#__ISSUE_REPO__#$(cfg '.intake.repos[0] // ""')#g" \
      -e "s#__PEOPLE__#$(cfg -o=json '.people' | jq -r 'to_entries | map("\(.key)=\(.value)") | join(",")')#g" \
      "$BRAIN/launchd/$a.plist" >"$dst"
  launchctl bootout "gui/$(id -u)/$a" 2>/dev/null || true
  launchctl bootstrap "gui/$(id -u)" "$dst"
  echo "   loaded $a"
}

build_hub() {
  echo "== hub (Go + Turso)"
  command -v go >/dev/null || brew install -q go >/dev/null
  make -s -C "$BRAIN/hub" build
  echo "   built $BRAIN/bin/hub ($("$BRAIN/bin/hub" version))"
}

if (( SHIPPER )); then
  command -v brew >/dev/null || { echo "install Homebrew first: https://brew.sh"; exit 1; }
  build_hub
  echo "== launchd (shipper only → $HUB_URL)"
  load_agent com.llm-brain.hub-ship
  exit 0
fi

echo "== tools"
command -v brew >/dev/null || { echo "install Homebrew first: https://brew.sh"; exit 1; }
brew install -q gh jq yq coreutils fswatch codex node rsync go >/dev/null
command -v ocr >/dev/null || npm install -g @alibaba-group/open-code-review >/dev/null
command -v claude >/dev/null || curl -fsSL https://claude.ai/install.sh | bash

# Optional: a repo that syncs your agent config (skills, memory, settings) between machines.
# Set `config_sync: {repo: <git url>, dir: <path>, restore: <script inside it>}` in your overlay.
if [[ "$(cfg '.config_sync.repo // ""')" != "" ]]; then
  CS_DIR="$(cfg_path '.config_sync.dir')"
  echo "== config sync ($CS_DIR)"
  [[ -d "$CS_DIR" ]] || git clone -q "$(cfg '.config_sync.repo')" "$CS_DIR"
  [[ -n "$(cfg '.config_sync.restore // ""')" ]] && "$CS_DIR/$(cfg '.config_sync.restore')" | tail -2
fi

if (( CLONE )) && OV="$(overlay_dir)" && [[ -f "$OV/repos.txt" ]]; then
  WR="$(cfg_path '.work_root')"
  echo "== repos → $WR"
  mkdir -p "$WR"
  grep -vE '^\s*(#|$)' "$OV/repos.txt" | while read -r name remote; do
    [[ -d "$WR/$name/.git" ]] || git clone -q "$remote" "$WR/$name" || echo "   clone failed: $name"
  done
fi

build_hub

echo "== launchd"
# The Python dashboard was replaced by the hub (same port).
launchctl bootout "gui/$(id -u)/com.llm-brain.dashboard" 2>/dev/null || true
rm -f "$HOME/Library/LaunchAgents/com.llm-brain.dashboard.plist"
agents=()
(( FLEET )) && agents+=(com.llm-brain.fleet com.llm-brain.hub)
for a in "${agents[@]+"${agents[@]}"}"; do load_agent "$a"; done

echo "== logins (each must say logged in)"
gh auth status >/dev/null 2>&1 && echo "   gh: ok" || echo "   gh: run  gh auth login -h github.com -p ssh -w"
claude auth status >/dev/null 2>&1 && echo "   claude: ok" || echo "   claude: run  claude auth login"
codex login status >/dev/null 2>&1 && echo "   codex: ok" || echo "   codex: run  codex login --device-auth"
"$BRAIN/bin/fleet" providers
