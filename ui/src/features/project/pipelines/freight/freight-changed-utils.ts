import {
  flattenFreightOrigin,
  TableSource
} from '@ui/features/freight/flatten-freight-origin-utils';
import {
  ArtifactReference,
  Chart,
  Freight,
  FreightReference,
  GitCommit,
  Image
} from '@ui/gen/api/v2/models';

import { FreightArtifactItem, getFreightArtifacts } from './freight-artifact-list-utils';
import { shortVersion } from './short-version-utils';

export type ArtifactVersionInfo = {
  // the value compared to decide whether the artifact changed (exact, e.g. an
  // image digest or full commit id)
  version: string;
  // the human-readable form of that version (e.g. an image tag), used to show
  // the previous version struck through next to the new one
  display: string;
};

export type ArtifactDescriptor = ArtifactVersionInfo & {
  // identity distinguishes which artifact a freight entry refers to (stable
  // across freight from the same warehouse); version is the revision the
  // freight pins it to
  identity: string;
};

// describeArtifact derives an artifact's identity and version. The REST models
// carry no discriminator, so the kind is inferred from the fields each one
// has, checked in an order where a field two kinds share (tag on commits and
// images, version on charts and generic artifacts) cannot misclassify.
export const describeArtifact = (artifact: FreightArtifactItem): ArtifactDescriptor => {
  if ('id' in artifact) {
    const commit = artifact as GitCommit;

    return {
      identity: `commit:${commit.repoURL || ''}`,
      version: commit.id || '',
      display: commit.tag ? shortVersion(commit.tag) : (commit.id || '').slice(0, 7)
    };
  }

  if ('subscriptionName' in artifact || 'artifactType' in artifact) {
    const reference = artifact as ArtifactReference;

    return {
      identity: `artifact:${reference.subscriptionName || ''}`,
      version: reference.version || '',
      display: shortVersion(reference.version)
    };
  }

  if ('digest' in artifact || 'tag' in artifact) {
    const image = artifact as Image;

    return {
      identity: `image:${image.repoURL || ''}`,
      // the digest decides whether an image changed; a tag can be re-pointed
      version: image.digest || image.tag || '',
      display: image.tag ? shortVersion(image.tag) : shortVersion(image.digest)
    };
  }

  const chart = artifact as Chart;

  return {
    identity: `chart:${chart.repoURL || ''}:${chart.name || ''}`,
    version: chart.version || '',
    display: shortVersion(chart.version)
  };
};

export const artifactIdentity = (artifact: FreightArtifactItem): string =>
  describeArtifact(artifact).identity;

export const artifactVersion = (artifact: FreightArtifactItem): ArtifactVersionInfo => {
  const { version, display } = describeArtifact(artifact);

  return { version, display };
};

export const collectArtifactVersions = (
  freight?: Freight | FreightReference | null
): Map<string, ArtifactVersionInfo> => {
  const versions = new Map<string, ArtifactVersionInfo>();

  for (const artifact of getFreightArtifacts(freight || undefined)) {
    const { identity, version, display } = describeArtifact(artifact);

    versions.set(identity, { version, display });
  }

  return versions;
};

// an artifact counts as changed when the previous freight did not contain it
// or pinned it to a different version
export const isArtifactChanged = (
  artifact: FreightArtifactItem,
  previousVersions: Map<string, ArtifactVersionInfo>
): boolean => {
  const { identity, version } = describeArtifact(artifact);

  return previousVersions.get(identity)?.version !== version;
};

// an artifact counts as added when the previous freight did not contain it at
// all -- as opposed to a version bump, where it existed under a different
// version. This lets the UI tell "new package" apart from "new version"
export const isArtifactAdded = (
  artifact: FreightArtifactItem,
  previousVersions: Map<string, ArtifactVersionInfo>
): boolean => !previousVersions.has(artifactIdentity(artifact));

// the previous freight's human-readable version of this artifact, only when
// it differs from the current one (i.e. what to strike through in the UI)
export const previousArtifactVersion = (
  artifact: FreightArtifactItem,
  previousVersions: Map<string, ArtifactVersionInfo>
): string | undefined => {
  const { identity, version } = describeArtifact(artifact);
  const previous = previousVersions.get(identity);

  if (!previous || previous.version === version) {
    return undefined;
  }

  return previous.display;
};

// changedFirst orders artifacts so those that differ from the previous freight
// come before unchanged ones (stable within each group), keeping changes
// visible where only the first few artifacts are displayed
export const changedFirst = <T extends FreightArtifactItem>(
  artifacts: T[],
  previousVersions: Map<string, ArtifactVersionInfo>
): T[] =>
  [...artifacts].sort(
    (a, b) =>
      Number(!isArtifactChanged(a, previousVersions)) -
      Number(!isArtifactChanged(b, previousVersions))
  );

// tableSourceToArtifact turns a row of the artifacts table back into the
// artifact shape the comparison helpers understand
export const tableSourceToArtifact = (source: TableSource): FreightArtifactItem => {
  switch (source.type) {
    case 'image':
      return {
        repoURL: source.repoURL,
        tag: source.tag,
        digest: source.digest,
        annotations: source.annotations
      };
    case 'git':
      return {
        repoURL: source.repoURL,
        id: source.id,
        tag: source.tag,
        branch: source.branch,
        message: source.message,
        author: source.author,
        committer: source.committer
      };
    case 'helm':
      return { repoURL: source.repoURL, name: source.name, version: source.version };
    default: {
      // eslint-disable-next-line @typescript-eslint/no-unused-vars
      const { type, ...reference } = source;

      return reference;
    }
  }
};

export type ArtifactChangeStatus = 'changed' | 'added' | 'unchanged' | 'removed';

export type ArtifactChange = {
  status: ArtifactChangeStatus;
  // the previous freight's row for this artifact, for a changed artifact
  previous?: TableSource;
};

export type ComparedTableSource = TableSource & { change: ArtifactChange };

const changeRank: Record<ArtifactChangeStatus, number> = {
  changed: 0,
  added: 0,
  unchanged: 1,
  removed: 2
};

// compareFreightArtifacts builds the rows of the artifacts table for a piece
// of freight compared with a previous one: every artifact of `current`,
// annotated with how it differs from `previous`, followed by the artifacts
// `previous` had that `current` dropped. Changed and added artifacts come
// first so they land on the table's first page.
export const compareFreightArtifacts = (
  current: Freight | FreightReference | undefined | null,
  previous: Freight | FreightReference | undefined | null
): ComparedTableSource[] => {
  const previousVersions = collectArtifactVersions(previous);
  const currentSources = flattenFreightOrigin(current);

  const previousSources = new Map(
    flattenFreightOrigin(previous).map((source) => [
      artifactIdentity(tableSourceToArtifact(source)),
      source
    ])
  );

  const rows: ComparedTableSource[] = currentSources.map((source) => {
    const artifact = tableSourceToArtifact(source);

    if (isArtifactAdded(artifact, previousVersions)) {
      return { ...source, change: { status: 'added' } };
    }

    if (isArtifactChanged(artifact, previousVersions)) {
      return {
        ...source,
        change: { status: 'changed', previous: previousSources.get(artifactIdentity(artifact)) }
      };
    }

    return { ...source, change: { status: 'unchanged' } };
  });

  const currentIdentities = new Set(
    currentSources.map((source) => artifactIdentity(tableSourceToArtifact(source)))
  );

  const removed: ComparedTableSource[] = [...previousSources]
    .filter(([identity]) => !currentIdentities.has(identity))
    .map(([, source]) => ({ ...source, change: { status: 'removed' } }));

  return [...rows, ...removed].sort(
    (a, b) => changeRank[a.change.status] - changeRank[b.change.status]
  );
};

const creationTime = (freight: Freight): number =>
  new Date(freight?.metadata?.creationTimestamp || '').getTime() || 0;

// indexPreviousFreight maps each freight's name to the chronologically previous
// freight from the same warehouse among the given freight, or undefined for
// the oldest of its warehouse. Entries carrying a count are the freight
// timeline's collapsed-freight placeholders and are skipped.
export const indexPreviousFreight = (
  freights: (Freight & { count?: number })[]
): Record<string, Freight | undefined> => {
  const previousByName: Record<string, Freight | undefined> = {};
  const lastSeenByOrigin: Record<string, Freight> = {};

  // oldest first
  const ordered = freights
    .filter((f) => !f.count)
    .sort((a, b) => creationTime(a) - creationTime(b));

  for (const freight of ordered) {
    const origin = freight?.origin?.name || '';

    previousByName[freight?.metadata?.name || ''] = lastSeenByOrigin[origin];
    lastSeenByOrigin[origin] = freight;
  }

  return previousByName;
};
