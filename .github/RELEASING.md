# Maintaining and releasing

This branch supports Windows amd64. Keep platform work on its own development
branch until it is ready to merge.

## Development

Develop on a short-lived branch, push it and open a PR targeting `main`.
Review the final diff and wait for `CI required`, then squash merge manually.
All branch pushes and PRs targeting `main` or `release/*` run CI without publishing.

## Release

1. Select a tested `main` commit and create `release/X.Y` for stabilization.
2. Add necessary fixes through PRs targeting that release branch and bring
   applicable fixes back to `main`. Complete Windows functional acceptance.
3. Create an annotated tag on the exact verified commit and push that tag:

   ```sh
   git tag -a v0.1.0 RELEASE_SHA -m "Release v0.1.0"
   git push origin v0.1.0
   ```

A local tag does not start GitHub Actions. Pushing `vX.Y.Z` or a prerelease such as
`v0.1.0-rc.1` validates the tag snapshot, runs native Windows tests, builds once,
and publishes that tested EXE and portable ZIP with `SHA256SUMS`.
The source version stays `dev`; `build.sh` injects the version from the tag.
The workflow does not enforce membership in a particular branch.

Release notes are generated from commit subjects, including direct commits.
The baseline is the highest lower published SemVer: stable releases ignore
previews; previews may use earlier previews of the same full version. Drafts
are ignored. A missing baseline tag aborts release preparation. Cross-branch
patch-equivalence comparison filters already-published cherry-picked changes;
materially edited backports may still appear. Merge commits are omitted.
Conventional `feat:`, `fix:`, `perf:`, `refactor:` and `docs:` subjects are grouped,
while other subjects remain in Other Changes. Breaking changes take precedence.
The generated notes populate both the Release body and packaged `CHANGELOG.md`;
no version or changelog commits are written back to any branch.

Publication creates or reuses a draft, uploads every asset, verifies the tag has
not moved, and only then makes the release public. Retry infrastructure or upload
failures on the same tag. Code changes require a new tag. Never move a published
tag or replace published binaries.

The release entry point is `.github/workflows/tag-release.yml`; the former
branch-triggered `release.yml` and its version-PR configuration are removed.
Repository rules remain configured in GitHub. Publication only needs
`contents: write` on its publish job, with no automated PR creation.
