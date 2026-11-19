#!/bin/bash
# Build "Brain Hub.app" (the native Mac client) and a zip of it in app-mac/dist/.
# Extra arguments go to `swift build`, for example `--arch arm64 --arch x86_64` for a universal app (needs Xcode).
# Needs Vendor/GhosttyKit.xcframework (./fetch-ghostty.sh) and Go (for brain-attach, built universal).
# The version is the desktop client's, from app/src-tauri/tauri.conf.json, so one client-v* tag covers both apps.
set -euo pipefail
cd "$(dirname "$0")"

version=$(plutil -extract version raw -o - ../app/src-tauri/tauri.conf.json)
swift build -c release "$@"
bin=$(swift build -c release "$@" --show-bin-path)

app="dist/Brain Hub.app"
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$bin/BrainHub" "$app/Contents/MacOS/BrainHub"
# brain-attach runs in each terminal pane and holds the hub connection.
for arch in arm64 amd64; do
  (cd ../attach && CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags=-s -o "$OLDPWD/.build/brain-attach-$arch" .)
  (cd ../attach && CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath -ldflags=-s -o "$OLDPWD/.build/brain-update-$arch" ./cmd/brain-update)
done
lipo -create -output "$app/Contents/MacOS/brain-update" .build/brain-update-arm64 .build/brain-update-amd64
codesign --force --sign - "$app/Contents/MacOS/brain-update"
lipo -create -output "$app/Contents/MacOS/brain-attach" .build/brain-attach-arm64 .build/brain-attach-amd64
codesign --force --sign - "$app/Contents/MacOS/brain-attach"
# Ghostty's terminfo and themes: libghostty finds them next to the executable, so a `theme =` line works.
if [ -d Vendor/share ]; then cp -R Vendor/share/terminfo Vendor/share/ghostty "$app/Contents/Resources/"; fi
cp ../app/src-tauri/icons/icon.icns "$app/Contents/Resources/AppIcon.icns"
# The MCP bridge any MCP client starts (Settings → MCP shows the config).
install -m 755 ../bin/brain-mcp "$app/Contents/Resources/brain-mcp"
cat > "$app/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleIdentifier</key><string>app.llmbrain.hub</string>
  <key>CFBundleName</key><string>Brain Hub</string>
  <key>CFBundleDisplayName</key><string>Brain Hub</string>
  <key>CFBundleExecutable</key><string>BrainHub</string>
  <key>CFBundleIconFile</key><string>AppIcon</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$version</string>
  <key>CFBundleVersion</key><string>$version</string>
  <key>LSMinimumSystemVersion</key><string>14.0</string>
  <key>LSApplicationCategoryType</key><string>public.app-category.developer-tools</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>NSPrincipalClass</key><string>NSApplication</string>
  <!-- Hubs on the tailnet can use http://. HubURL.swift refuses every other host. -->
  <key>NSAppTransportSecurity</key><dict><key>NSAllowsArbitraryLoads</key><true/></dict>
</dict>
</plist>
EOF
codesign --force --sign - "$app"
codesign --verify "$app"
rm -f "dist/BrainHub-mac-$version.zip"
ditto -c -k --keepParent "$app" "dist/BrainHub-mac-$version.zip"
echo "Built $app and dist/BrainHub-mac-$version.zip"
