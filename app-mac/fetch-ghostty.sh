#!/bin/bash
# Put libghostty in Vendor/: GhosttyKit.xcframework and share/ (terminfo, themes), built from the commit in
# ghostty.commit. With Xcode, this builds it with Zig; without, it downloads the `GhosttyKit-<commit>` artifact
# of the mac-app workflow (needs the GitHub CLI, logged in).
set -euo pipefail
cd "$(dirname "$0")"
commit=$(cat ghostty.commit)
name="GhosttyKit-$commit"
rm -rf Vendor && mkdir Vendor

if xcode-select -p 2>/dev/null | grep -q Xcode.app && command -v zig >/dev/null; then
  src=$(mktemp -d)
  git -C "$src" init -q
  git -C "$src" fetch -q --depth 1 https://github.com/ghostty-org/ghostty.git "$commit"
  git -C "$src" checkout -q FETCH_HEAD
  (cd "$src" && zig build -Doptimize=ReleaseFast -Demit-xcframework=true -Dxcframework-target=universal \
    -Demit-macos-app=false -Di18n=false -Demit-terminfo=true -Demit-themes=true)
  mkdir Vendor/share
  cp -R "$src/macos/GhosttyKit.xcframework" Vendor/
  # SwiftPM takes a static library only as lib*.a.
  mv Vendor/GhosttyKit.xcframework/macos-arm64_x86_64/ghostty-internal.a Vendor/GhosttyKit.xcframework/macos-arm64_x86_64/libghostty.a
  plutil -replace AvailableLibraries.0.LibraryPath -string libghostty.a Vendor/GhosttyKit.xcframework/Info.plist
  cp -R "$src/zig-out/share/terminfo" "$src/zig-out/share/ghostty" Vendor/share/
  rm -rf "$src"
else
  repo=$(gh repo view --json nameWithOwner --jq .nameWithOwner)
  run=$(gh api "repos/$repo/actions/artifacts?name=$name" --jq '[.artifacts[] | select(.expired | not)][0].workflow_run.id')
  [ -n "$run" ] && [ "$run" != null ] || { echo "No $name artifact: run the mac-app workflow first." >&2; exit 1; }
  tmp=$(mktemp -d)
  gh run download "$run" --repo "$repo" -n "$name" -D "$tmp"
  tar -xzf "$tmp/GhosttyKit.tgz" -C Vendor --strip-components 1
  rm -rf "$tmp"
fi
echo "Vendor/ has libghostty $commit"
