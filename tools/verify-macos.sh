#!/usr/bin/env sh
set -eu
if [ "$(uname -s)" != Darwin ]; then
    echo 'Run this verification helper on macOS.' >&2
    exit 1
fi
package_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
app="${1:-$package_dir/Codex Usage Monitor.app}"
if [ ! -f "$app/Contents/Info.plist" ]; then
    echo 'Provide the path to Codex Usage Monitor.app.' >&2
    exit 1
fi
plutil -lint "$app/Contents/Info.plist"
# Development bundles use a local ad-hoc signature. This is not Developer ID
# signing or notarization; it requires no account, certificate or credential.
if ! codesign --verify --strict "$app" >/dev/null 2>&1; then
    codesign --force --sign - "$app"
fi
codesign --verify --strict "$app"
"$app/Contents/MacOS/CodexUsageMonitor" --version
if [ -f "$package_dir/MacAdapterTests" ]; then
    "$package_dir/MacAdapterTests" -test.v
fi
open "$app"
