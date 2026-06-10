import {
  ArtifactReference,
  Chart,
  Freight,
  GitCommit,
  Image
} from '@ui/gen/api/v1alpha1/generated_pb';

import { shortVersion } from './short-version-utils';

export type FreightArtifactType = GitCommit | Chart | Image | ArtifactReference;

export type ArtifactVersionInfo = {
  // the value compared to decide whether the artifact changed (exact, e.g. an
  // image digest or full commit id)
  version: string;
  // the human-readable form of that version (e.g. an image tag), used to show
  // the previous version struck through next to the new one
  display: string;
};

// identity distinguishes which artifact a freight entry refers to (stable
// across freight from the same warehouse); version is the revision the
// freight pins it to
export const artifactIdentity = (artifact: FreightArtifactType): string => {
  switch (artifact.$typeName) {
    case 'github.com.akuity.kargo.api.v1alpha1.GitCommit':
      return `commit:${artifact.repoURL}`;
    case 'github.com.akuity.kargo.api.v1alpha1.Chart':
      return `chart:${artifact.repoURL}:${artifact.name}`;
    case 'github.com.akuity.kargo.api.v1alpha1.Image':
      return `image:${artifact.repoURL}`;
    default:
      return `artifact:${artifact.subscriptionName}`;
  }
};

export const artifactVersion = (artifact: FreightArtifactType): ArtifactVersionInfo => {
  switch (artifact.$typeName) {
    case 'github.com.akuity.kargo.api.v1alpha1.GitCommit':
      return {
        version: artifact.id,
        display: artifact.tag ? shortVersion(artifact.tag) : artifact.id.slice(0, 7)
      };
    case 'github.com.akuity.kargo.api.v1alpha1.Chart':
      return { version: artifact.version, display: shortVersion(artifact.version) };
    case 'github.com.akuity.kargo.api.v1alpha1.Image':
      return {
        version: artifact.digest || artifact.tag,
        display: artifact.tag ? shortVersion(artifact.tag) : shortVersion(artifact.digest)
      };
    default:
      return { version: artifact.version, display: shortVersion(artifact.version) };
  }
};

export const collectArtifactVersions = (freight?: Freight): Map<string, ArtifactVersionInfo> => {
  const versions = new Map<string, ArtifactVersionInfo>();

  for (const artifact of [
    ...(freight?.commits || []),
    ...(freight?.charts || []),
    ...(freight?.images || []),
    ...(freight?.artifacts || [])
  ]) {
    versions.set(artifactIdentity(artifact), artifactVersion(artifact));
  }

  return versions;
};

// an artifact counts as changed when the previous freight did not contain it
// or pinned it to a different version
export const isArtifactChanged = (
  artifact: FreightArtifactType,
  previousVersions: Map<string, ArtifactVersionInfo>
): boolean =>
  previousVersions.get(artifactIdentity(artifact))?.version !== artifactVersion(artifact).version;

// an artifact counts as added when the previous freight did not contain it at
// all -- as opposed to a version bump, where it existed under a different
// version. This lets the UI tell "new package" apart from "new version"
export const isArtifactAdded = (
  artifact: FreightArtifactType,
  previousVersions: Map<string, ArtifactVersionInfo>
): boolean => !previousVersions.has(artifactIdentity(artifact));

// the previous freight's human-readable version of this artifact, only when
// it differs from the current one (i.e. what to strike through in the UI)
export const previousArtifactVersion = (
  artifact: FreightArtifactType,
  previousVersions: Map<string, ArtifactVersionInfo>
): string | undefined => {
  const previous = previousVersions.get(artifactIdentity(artifact));

  if (!previous || previous.version === artifactVersion(artifact).version) {
    return undefined;
  }

  return previous.display;
};
