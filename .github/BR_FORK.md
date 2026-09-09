# The br fork of Kargo

This repository (`BotResources/kargo`) is a long-lived fork of
[akuity/kargo](https://github.com/akuity/kargo). It exists to carry a small
set of patches ("the br patch set") on top of upstream **release tags** and to
publish deployable artifacts for them.

## What the patch set contains

The patch set is exactly two commits on top of the upstream release tag:

1. `feat(ui)`: everything that could be sent upstream as one pull request:
   - a "Highlight changes" option in the freight timeline filters
     (implements upstream issue
     [akuity/kargo#6349](https://github.com/akuity/kargo/issues/6349))
   - the same comparison in the freight details drawer, which names and
     links to the piece of freight compared against
   - a page size selector (5/10/20/50, remembered by the browser) on the
     freight details artifacts table
2. `chore(br)`: everything that only makes sense in this fork:
   - chart defaults pointing at this fork's images
     (`ghcr.io/botresources/kargo`)
   - the YAML editor fetching its schemas from this fork
   - the `br-release` and `br-sync` workflows and this document

Keep the patch set as small as possible. If upstream ever merges an
equivalent of the feature, drop the `feat(ui)` commit during the next sync.

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

Prerequisite: a `BR_SYNC_TOKEN` repository secret holding a fine-grained
personal access token for `BotResources/kargo` with read/write access to
contents, workflows, actions and issues. The default `GITHUB_TOKEN` cannot
push a rebase that touches `.github/workflows` (upstream changes them between
releases), so without the secret br-sync fails at the push step with
"refusing to allow a GitHub App to create or update workflow ... without
`workflows` permission". This is what broke every sync from v1.10.8 to
v1.11.1.

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

## Keeping the patch set extractable

Never blend feature changes and br-infrastructure changes into the same
commit on `br/main` (including when resolving rebase conflicts). The
`feat(ui)` commit must stay cleanly cherry-pickable onto upstream `main` for
an eventual upstream PR (`git format-patch -1 <its sha>` is that PR), and
droppable if upstream merges an equivalent.

Do not grow the patch set either: a change to the feature is folded into the
`feat(ui)` commit and a change to the fork plumbing into the `chore(br)`
commit (amend, or `git commit --fixup` followed by
`git rebase --autosquash <release-tag>`), then `br/main` is force-pushed and
the current `vX.Y.Z-br` tag re-cut if nothing consumes it yet. Two commits
means at most two conflict stops per sync and a delta that is readable as
two patches.

## Operational notes

- GitHub disables scheduled workflows on forks after 60 days without repo
  activity; if br-sync stops firing, re-enable it under Actions and consider
  running it manually (`gh workflow run br-sync.yaml --repo BotResources/kargo`).
- The `ghcr.io/botresources/kargo` and `kargo-charts` packages must be public
  (or consumers need a pull secret): package settings → Change visibility.
- Full UI test suite has pre-existing failures upstream; verification
  deliberately runs only typecheck plus the feature's own tests.
- Since upstream 1.11 the UI talks to the REST `v1beta1` API (orval-generated
  hooks under `ui/src/gen/api/v2`) instead of the protobuf models; the patch
  set was rewritten for it during the v1.11.4 sync. vitest resolves the
  `@ui/*` aliases through `vite.config.mts`, so no separate vitest config is
  needed anymore.
- To eyeball the UI without a cluster, `ui/vite.config.mts` proxies
  `/v1beta1` to `API_URL` (default `http://localhost:30081`); a small mock
  serving `system/public-server-config` with `skipAuth: true` plus the
  project, freight, stages and warehouses endpoints is enough for the
  pipelines page and the freight details drawer.
