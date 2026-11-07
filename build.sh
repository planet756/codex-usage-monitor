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
output_dir="${1:-dist/$version}"
changelog=${BUILD_CHANGELOG:-CHANGELOG.md}
if [ ! -f "$changelog" ]; then
    echo 'Build changelog does not exist' >&2
    exit 1
fi
mkdir -p "$output_dir"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -mod=readonly -buildvcs=false -trimpath \
    -ldflags "-s -w -H=windowsgui -X main.version=$version" \
    -o "$output_dir/CodexUsageMonitor.exe" ./cmd/codex-usage-monitor
cp README.md README.zh-CN.md LICENSE THIRD-PARTY-NOTICES.md "$output_dir/"
cp "$changelog" "$output_dir/CHANGELOG.md"
cp -R LICENSES "$output_dir/"
cp -R assets "$output_dir/"
(cd "$output_dir" && sha256sum CodexUsageMonitor.exe > SHA256SUMS)
