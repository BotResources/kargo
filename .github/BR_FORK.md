# The br fork of Kargo

This repository (`BotResources/kargo`) is a long-lived fork of
[akuity/kargo](https://github.com/akuity/kargo). It exists to carry a small
set of patches ("the br patch set") on top of upstream **release tags** and to
publish deployable artifacts for them.

## What the patch set contains

- `feat(ui)`: a "Highlight changes" toggle in the freight timeline filters
  (implements upstream issue
  [akuity/kargo#6349](https://github.com/akuity/kargo/issues/6349))
- `chore(br)`: chart defaults point at this fork's images
  (`ghcr.io/botresources/kargo`)
- `ci(br)`: the `br-release` and `br-sync` workflows and this document

Keep the patch set as small as possible. If upstream ever merges an
equivalent feature, drop the corresponding commit during the next sync.

## Branch, tag, and artifact model

- `main` mirrors upstream `main` (not actively used; kept for reference)
- `br/main` is the default branch: the br patch set rebased on top of the
  newest upstream release tag `vX.Y.Z`
- `vX.Y.Z-br` tags mark each rebase result; each has a GitHub release
- Artifacts per tag:
  - Container image: `ghcr.io/botresources/kargo:vX.Y.Z-br`
  - Helm chart: `oci://ghcr.io/botresources/kargo-charts/kargo`,
    version `X.Y.Z-br`

The current base of `br/main` is always derivable as the newest `v*-br` tag
with the `-br` suffix stripped.

## Automation

- `.github/workflows/br-sync.yaml` (daily + manual): detects a new upstream
  release, rebases the patch set onto it, verifies (UI typecheck + the
  feature's unit tests), force-pushes `br/main`, tags `vX.Y.Z-br`, and
  dispatches `br-release`. On any failure it opens an issue with step-by-step
  resolution instructions.
- `.github/workflows/br-release.yaml` (on `v*-br` tag push or dispatch):
  builds and pushes the multi-arch image and the Helm chart, then creates the
  GitHub release. On failure it opens an issue.

Upstream workflows (`ci.yaml`, `release.yaml`, ...) are intentionally left
untouched to minimize rebase conflicts; their scheduled triggers stay
disabled on this fork, and both br workflows are no-ops on `akuity/kargo`
(`if: github.repository != 'akuity/kargo'`) in case they ever land in a
diff sent upstream.

## Manual sync (what br-sync does, by hand)

```bash
git clone https://github.com/BotResources/kargo.git kargo-br && cd kargo-br
git checkout br/main
git remote add upstream https://github.com/akuity/kargo.git
git fetch upstream --tags
git rebase --onto <new-release-tag> <current-base-tag> br/main
# resolve conflicts if any, then verify:
cd ui && pnpm install && pnpm typecheck && \
  pnpm vitest run src/features/project/pipelines/freight/freight-changed-utils.test.ts && cd ..
git push --force origin br/main
git tag <new-release-tag>-br && git push origin refs/tags/<new-release-tag>-br
gh workflow run br-release.yaml --repo BotResources/kargo --ref refs/tags/<new-release-tag>-br
```

## Operational notes

- GitHub disables scheduled workflows on forks after 60 days without repo
  activity; if br-sync stops firing, re-enable it under Actions and consider
  running it manually (`gh workflow run br-sync.yaml --repo BotResources/kargo`).
- The `ghcr.io/botresources/kargo` and `kargo-charts` packages must be public
  (or consumers need a pull secret): package settings → Change visibility.
- Full UI test suite has pre-existing failures upstream; verification
  deliberately runs only typecheck plus the feature's own tests.
