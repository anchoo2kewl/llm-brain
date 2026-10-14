#!/usr/bin/env bash
# Bootstrap a Mac as an llm-brain fleet machine. Idempotent; safe to re-run.
#   git clone git@github.com:anchoo2kewl/llm-brain.git ~/projects/llm-brain && ~/projects/llm-brain/bootstrap.sh
# Prereq it cannot do for you: the logins printed at the end.
#   --no-clone   skip cloning the Elastio repos
#   --no-fleet   don't start the fleet loop on this machine
set -euo pipefail
BRAIN="$(cd "$(dirname "$0")" && pwd)"
export PATH="$HOME/.local/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"
CLONE=1 FLEET=1
for a in "$@"; do case "$a" in --no-clone) CLONE=0 ;; --no-fleet) FLEET=0 ;; esac; done

echo "== tools"
command -v brew >/dev/null || { echo "install Homebrew first: https://brew.sh"; exit 1; }
brew install -q gh jq yq coreutils fswatch codex node rsync >/dev/null
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

echo "== launchd"
agents=()
(( FLEET )) && agents+=(com.llm-brain.fleet)
for a in "${agents[@]+"${agents[@]}"}"; do
  dst="$HOME/Library/LaunchAgents/$a.plist"
  sed -e "s#__BRAIN__#$BRAIN#g; s#__HOME__#$HOME#g" "$BRAIN/launchd/$a.plist" >"$dst"
  launchctl bootout "gui/$(id -u)/$a" 2>/dev/null || true
  launchctl bootstrap "gui/$(id -u)" "$dst"
  echo "   loaded $a"
done

echo "== logins (each must say logged in)"
gh auth status >/dev/null 2>&1 && echo "   gh: ok" || echo "   gh: run  gh auth login -h github.com -p ssh -w"
claude auth status >/dev/null 2>&1 && echo "   claude: ok" || echo "   claude: run  claude auth login"
codex login status >/dev/null 2>&1 && echo "   codex: ok" || echo "   codex: run  codex login --device-auth"
"$BRAIN/bin/fleet" providers
