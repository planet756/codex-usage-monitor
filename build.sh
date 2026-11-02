#!/usr/bin/env sh
set -eu
cd "$(dirname "$0")"
version=$(sed -n 's/^var version = "\([^"]*\)".*$/\1/p' cmd/codex-usage-monitor/main.go)
case "$version" in
    *[!0-9A-Za-z.+-]*|'') echo 'Invalid source version declaration' >&2; exit 1 ;;
esac
if ! printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$'; then
    echo 'Invalid source version declaration' >&2
    exit 1
fi
if [ "${1:-}" = '--version' ]; then
    printf '%s\n' "$version"
    exit 0
fi
output_dir="${1:-dist/$version}"
mkdir -p "$output_dir"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -mod=readonly -buildvcs=false -trimpath \
    -ldflags '-s -w -H=windowsgui' \
    -o "$output_dir/CodexUsageMonitor.exe" ./cmd/codex-usage-monitor
cp README.md README.zh-CN.md CHANGELOG.md LICENSE THIRD-PARTY-NOTICES.md "$output_dir/"
cp -R LICENSES "$output_dir/"
cp -R assets "$output_dir/"
(cd "$output_dir" && sha256sum CodexUsageMonitor.exe > SHA256SUMS)
