#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../.."

: "${GH_REPO:?}" "${RELEASE_TAG:?}" "${RELEASE_VERSION:?}" "${RELEASE_SHA:?}" "${RELEASE_TAG_OBJECT:?}" "${RELEASE_PRERELEASE:?}"
metadata=dist/release-metadata/metadata.json
notes=dist/release-metadata/CHANGELOG.md
jq -e --arg tag "$RELEASE_TAG" --arg sha "$RELEASE_SHA" --arg version "$RELEASE_VERSION" \
  --arg object "$RELEASE_TAG_OBJECT" --arg prerelease "$RELEASE_PRERELEASE" \
  '.tag == $tag and .sha == $sha and .version == $version and .tag_object == $object and (.prerelease | tostring) == $prerelease' "$metadata" > /dev/null

# Compare the tag object itself so annotated tags work and retagging is rejected.
actual_tag=$(gh api "repos/$GH_REPO/git/ref/tags/$RELEASE_TAG" --jq '.object.sha')
test "$actual_tag" = "$RELEASE_TAG_OBJECT"
(cd dist/windows && sha256sum --check SHA256SUMS)
(cd dist/windows-installer && sha256sum --check SHA256SUMS)
cmp "$notes" dist/windows/CHANGELOG.md

prefix="CodexUsageMonitor-$RELEASE_TAG"
mkdir -p dist/releases
setup="$prefix-windows-amd64-setup.exe"
test -s "dist/windows-installer/$setup"
cp "dist/windows-installer/$setup" "dist/releases/$setup"
(cd dist/windows && zip -qr "../releases/$prefix-windows-amd64-portable.zip" .)
assets=("$setup" "$prefix-windows-amd64-portable.zip")
(cd dist/releases && sha256sum "${assets[@]}" > SHA256SUMS)
assets+=(SHA256SUMS)

release_state=$(gh api --paginate "repos/$GH_REPO/releases?per_page=100" \
  --jq '.[] | {tag_name, draft}' | jq -sr --arg tag "$RELEASE_TAG" '.[] | select(.tag_name == $tag) | .draft')
if [ "$release_state" = false ]; then
  echo 'This release is already published; use a new version' >&2
  exit 1
fi
extra=()
if [ "$RELEASE_PRERELEASE" = true ]; then extra+=(--latest=false); fi
if [ -z "$release_state" ]; then
  gh release create "$RELEASE_TAG" --verify-tag --draft --title "$RELEASE_TAG" \
    --notes-file "$notes" --prerelease="$RELEASE_PRERELEASE" "${extra[@]}"
else
  gh release edit "$RELEASE_TAG" --title "$RELEASE_TAG" \
    --notes-file "$notes" --prerelease="$RELEASE_PRERELEASE" "${extra[@]}"
fi
test "$(gh release view "$RELEASE_TAG" --json isDraft --jq '.isDraft')" = true
(cd dist/releases && gh release upload "$RELEASE_TAG" "${assets[@]}" --clobber)
actual_tag=$(gh api "repos/$GH_REPO/git/ref/tags/$RELEASE_TAG" --jq '.object.sha')
test "$actual_tag" = "$RELEASE_TAG_OBJECT"
release_status=$(gh release view "$RELEASE_TAG" --json isDraft,databaseId,assets --jq '{draft: .isDraft, id: .databaseId, assets: [.assets[].name]}')
if ! jq -e --arg exe "$setup" --arg zip "$prefix-windows-amd64-portable.zip" \
  '(.assets | sort) == ([$exe, $zip, "SHA256SUMS"] | sort)' <<< "$release_status" > /dev/null; then
  echo 'The draft must contain exactly the Windows setup EXE, portable ZIP and checksums.' >&2
  exit 1
fi
release_id=$(jq -er 'select(.draft == true) | .id | select(type == "number" and . > 0)' <<< "$release_status")
# Let GitHub compare stable versions instead of making an older maintenance
# release the latest solely because it finished publishing later.
make_latest=legacy
if [ "$RELEASE_PRERELEASE" = true ]; then make_latest=false; fi
gh api --method PATCH "repos/$GH_REPO/releases/$release_id" \
  -F draft=false -f "make_latest=$make_latest" --silent
