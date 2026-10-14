#!/usr/bin/env bash
# Bootstrap a Mac as an llm-brain fleet machine. Idempotent; safe to re-run.
#   git clone git@github.com:anchoo2kewl/llm-brain.git ~/projects/llm-brain && ~/projects/llm-brain/bootstrap.sh
# Prereq it cannot do for you: the logins printed at the end.
#   --no-clone   skip cloning the Elastio repos
#   --no-fleet   don't start the fleet loop or the hub on this machine
#   --shipper    only ship this machine's sessions and logs to the hub (a laptop); loads nothing else
#   --hub=URL    the hub the shipper sends to (default http://100.74.108.114:8787, the Studio)
set -euo pipefail
BRAIN="$(cd "$(dirname "$0")" && pwd)"
export PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"
CLONE=1 FLEET=1 SHIPPER=0 HUB_URL="http://100.74.108.114:8787"
for a in "$@"; do case "$a" in
  --no-clone) CLONE=0 ;; --no-fleet) FLEET=0 ;; --shipper) SHIPPER=1 ;; --hub=*) HUB_URL="${a#--hub=}" ;;
esac; done

load_agent() { # load_agent <label>: render the plist into ~/Library/LaunchAgents and (re)load it
  local a="$1" dst="$HOME/Library/LaunchAgents/$1.plist"
  mkdir -p "$HOME/Library/LaunchAgents" "$HOME/.local/state/llm-brain/logs"
  sed -e "s#__BRAIN__#$BRAIN#g; s#__HOME__#$HOME#g; s#__HUB__#$HUB_URL#g" "$BRAIN/launchd/$a.plist" >"$dst"
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

echo "== claude-backup (skills, memory, settings — two-way synced)"
[[ -d "$HOME/claude-backup" ]] || git clone -q git@github.com:anchoo2kewl/claude-backup.git "$HOME/claude-backup"
"$HOME/claude-backup/bin/restore.sh" | tail -2

if (( CLONE )); then
  echo "== elastio repos"
  mkdir -p "$HOME/elastio"
  grep -vE '^\s*(#|$)' "$BRAIN/config/repos.txt" | while read -r name remote; do
    [[ -d "$HOME/elastio/$name/.git" ]] || git clone -q "$remote" "$HOME/elastio/$name" || echo "   clone failed: $name"
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
