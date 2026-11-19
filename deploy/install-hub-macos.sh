#!/bin/bash
# Replace a user's launchd hub while its job is unloaded, then load the new signature afresh.
set -euo pipefail
source_binary=${1:?usage: install-hub-macos.sh /path/to/new-hub [brain-directory]}
brain_directory=${2:-"$HOME/projects/llm-brain"}
label=com.llm-brain.hub
launch_domain="gui/$(id -u)"
plist="$HOME/Library/LaunchAgents/$label.plist"
target="$brain_directory/bin/hub"
staged="$target.install-$$"
trap 'rm -f "$staged"' EXIT
cp "$source_binary" "$staged"
chmod 755 "$staged"
codesign --force --sign - --identifier "$label" "$staged"
codesign --verify --strict "$staged"
cp "$target" "$target.previous"
if launchctl print "$launch_domain/$label" >/dev/null 2>&1; then
 launchctl bootout "$launch_domain/$label"
fi
mv "$staged" "$target"
if ! "$target" version >/dev/null || ! launchctl bootstrap "$launch_domain" "$plist"; then
 cp "$target.previous" "$staged"
 mv "$staged" "$target"
 "$target" version >/dev/null
 launchctl bootstrap "$launch_domain" "$plist" || true
 exit 1
fi
for attempt in {1..20}; do
 if curl --silent --fail --max-time 1 http://127.0.0.1:8787/api/cluster/nodes >/dev/null; then
  echo 'Hub installed and responding'
  exit 0
 fi
 sleep 0.25
done
echo 'Hub did not become ready; inspect the launchd service log' >&2
exit 1
