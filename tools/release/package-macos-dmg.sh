#!/bin/sh
set -eu
if [ "$(uname -s)" != Darwin ]; then
    echo 'DMG creation and verification require macOS.' >&2
    exit 1
fi

output_dir=$(cd "$1" && pwd)
name=$2
changelog=$3
app='Codex Usage Monitor.app'
work=$(mktemp -d "$output_dir/.dmg-XXXXXXXX")
mountpoint="$work/mount"
attached=false
cleanup() {
    if [ "$attached" = true ]; then
        hdiutil detach "$mountpoint" || return 1
    fi
    rm -rf "$work"
}
trap cleanup EXIT
mkdir "$work/stage" "$mountpoint"
ditto "$output_dir/$app" "$work/stage/$app"
ln -s /Applications "$work/stage/Applications"
hdiutil create -volname 'Codex Usage Monitor' -srcfolder "$work/stage" -format UDZO "$work/image.dmg"
hdiutil verify "$work/image.dmg"
hdiutil attach -nobrowse -readonly -mountpoint "$mountpoint" "$work/image.dmg"
attached=true
test "$(readlink "$mountpoint/Applications")" = /Applications
codesign --verify --strict "$mountpoint/$app"
test "$(lipo -archs "$mountpoint/$app/Contents/MacOS/CodexUsageMonitor")" = arm64
cmp "$output_dir/$app/Contents/MacOS/CodexUsageMonitor" "$mountpoint/$app/Contents/MacOS/CodexUsageMonitor"
cmp "$changelog" "$mountpoint/$app/Contents/Resources/CHANGELOG.md"
hdiutil detach "$mountpoint"
attached=false
mv "$work/image.dmg" "$output_dir/$name"
