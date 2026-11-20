#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")"
version=${BUILD_VERSION-dev}
case "$version" in
    *[!0-9A-Za-z.+-]*|'') echo 'Invalid BUILD_VERSION' >&2; exit 1 ;;
esac
if [ "$version" != dev ]; then
    number='(0|[1-9][0-9]*)'
    identifier='(0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)'
    if ! printf '%s\n' "$version" | grep -Eq "^$number\.$number\.$number(-$identifier(\.$identifier)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$"; then
        echo 'BUILD_VERSION must be dev or a semantic version without the v prefix' >&2
        exit 1
    fi
fi
if [ "${1:-}" = '--version' ]; then
    printf '%s\n' "$version"
    exit 0
fi
target_os="${2:-windows}"
case "$target_os" in
    windows) target_arch="${3:-amd64}"; default_dir="dist/$version" ;;
    darwin)
        target_arch="${3:-arm64}"
        if [ "$target_arch" != arm64 ]; then
            echo 'The macOS build requires Apple Silicon (arm64).' >&2
            exit 1
        fi
        default_dir="dist/$version/darwin-arm64" ;;
    *) echo 'Supported targets: windows, darwin' >&2; exit 1 ;;
esac
case "$target_arch" in amd64|arm64) ;; *) echo 'Supported architectures: amd64, arm64' >&2; exit 1 ;; esac
output_dir="${1:-$default_dir}"
changelog=${BUILD_CHANGELOG:-CHANGELOG.md}
if [ ! -f "$changelog" ]; then
    echo 'Build changelog does not exist' >&2
    exit 1
fi
mkdir -p "$output_dir"
if [ "$target_os" = windows ]; then
    artifact='CodexUsageMonitor.exe'
    GOOS=windows GOARCH="$target_arch" CGO_ENABLED=0 go build -mod=readonly -buildvcs=false -trimpath \
    -ldflags "-s -w -H=windowsgui -X main.version=$version" \
    -o "$output_dir/CodexUsageMonitor.exe" ./cmd/codex-usage-monitor
else
    app_dir="$output_dir/Codex Usage Monitor.app/Contents"
    mkdir -p "$app_dir/MacOS"
    artifact='Codex Usage Monitor.app/Contents/MacOS/CodexUsageMonitor'
    GOOS=darwin GOARCH="$target_arch" CGO_ENABLED=0 go build -mod=readonly -buildvcs=false -trimpath \
        -ldflags "-s -w -X main.version=$version" -o "$output_dir/$artifact" ./cmd/codex-usage-monitor
    GOOS=darwin GOARCH="$target_arch" CGO_ENABLED=0 go test -c -mod=readonly -buildvcs=false -trimpath \
        -ldflags '-s -w' -o "$output_dir/MacAdapterTests" ./internal/mac
    bundle_version=${version%%[-+]*}
    if [ "$version" = dev ]; then bundle_version=0.0.0; fi
    cat > "$app_dir/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>com.codexusagemonitor.app</string>
<key>CFBundleExecutable</key><string>CodexUsageMonitor</string>
<key>CFBundleName</key><string>Codex Usage Monitor</string>
<key>CFBundleDisplayName</key><string>Codex Usage Monitor</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleShortVersionString</key><string>$bundle_version</string>
<key>CFBundleVersion</key><string>$bundle_version</string>
<key>LSMinimumSystemVersion</key><string>13.0</string>
<key>LSUIElement</key><true/>
<key>NSHighResolutionCapable</key><true/>
</dict></plist>
EOF
    cp tools/verify-macos.sh "$output_dir/verify-macos.sh"
    resources="$app_dir/Resources"
    mkdir -p "$resources"
    cp README.md README.zh-CN.md LICENSE THIRD-PARTY-NOTICES.md "$resources/"
    cp "$changelog" "$resources/CHANGELOG.md"
    cp -R LICENSES "$resources/"
    if [ "$(uname -s)" = Darwin ]; then
        codesign --force --sign - "$output_dir/Codex Usage Monitor.app"
        codesign --verify --strict "$output_dir/Codex Usage Monitor.app"
        label=$version
        if [ "$version" != dev ]; then label="v$version"; fi
        sh tools/release/package-macos-dmg.sh "$output_dir" "CodexUsageMonitor-$label-macos-$target_arch.dmg" "$changelog"
    fi
fi
cp README.md README.zh-CN.md LICENSE THIRD-PARTY-NOTICES.md "$output_dir/"
cp "$changelog" "$output_dir/CHANGELOG.md"
cp -R LICENSES "$output_dir/"
cp -R assets "$output_dir/"
if command -v sha256sum >/dev/null 2>&1; then
    (cd "$output_dir" && sha256sum "$artifact" > SHA256SUMS)
    if [ "$target_os" = darwin ]; then (cd "$output_dir" && sha256sum MacAdapterTests >> SHA256SUMS); fi
else
    (cd "$output_dir" && shasum -a 256 "$artifact" > SHA256SUMS)
    if [ "$target_os" = darwin ]; then (cd "$output_dir" && shasum -a 256 MacAdapterTests >> SHA256SUMS); fi
fi
