import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';

import {
  ArtifactReferenceSchema,
  ChartSchema,
  FreightSchema,
  GitCommitSchema,
  ImageSchema
} from '../../../../gen/api/v1alpha1/generated_pb';

import {
  collectArtifactVersions,
  isArtifactChanged,
  previousArtifactVersion
} from './freight-changed-utils';

describe('collectArtifactVersions', () => {
  it('returns an empty map for undefined freight', () => {
    expect(collectArtifactVersions(undefined).size).toBe(0);
  });

  it('collects versions for all artifact kinds', () => {
    const freight = create(FreightSchema, {
      commits: [{ repoURL: 'https://github.com/akuity/repo.git', id: 'abc1234def5678' }],
      charts: [{ repoURL: 'https://charts.example.com', name: 'my-chart', version: '1.2.3' }],
      images: [{ repoURL: 'ghcr.io/akuity/image', tag: 'v1.0.0', digest: 'sha256:abc' }],
      artifacts: [{ subscriptionName: 'my-subscription', version: '9.9.9' }]
    });

    const versions = collectArtifactVersions(freight);

    expect(versions.get('commit:https://github.com/akuity/repo.git')).toEqual({
      version: 'abc1234def5678',
      display: 'abc1234' // raw commit hash shortened to 7 chars
    });
    expect(versions.get('chart:https://charts.example.com:my-chart')).toEqual({
      version: '1.2.3',
      display: '1.2.3'
    });
    // digest decides identity-change; tag is what humans see
    expect(versions.get('image:ghcr.io/akuity/image')).toEqual({
      version: 'sha256:abc',
      display: 'v1.0.0'
    });
    expect(versions.get('artifact:my-subscription')).toEqual({
      version: '9.9.9',
      display: '9.9.9'
    });
  });

  it('prefers the commit tag for display when present', () => {
    const freight = create(FreightSchema, {
      commits: [
        { repoURL: 'https://github.com/akuity/repo.git', id: 'abc1234def5678', tag: 'v2.0.0' }
      ]
    });

    expect(
      collectArtifactVersions(freight).get('commit:https://github.com/akuity/repo.git')
    ).toEqual({ version: 'abc1234def5678', display: 'v2.0.0' });
  });

  it('falls back to the tag when an image has no digest', () => {
    const freight = create(FreightSchema, {
      images: [{ repoURL: 'ghcr.io/akuity/image', tag: 'v1.0.0' }]
    });

    expect(collectArtifactVersions(freight).get('image:ghcr.io/akuity/image')).toEqual({
      version: 'v1.0.0',
      display: 'v1.0.0'
    });
  });
});

describe('isArtifactChanged', () => {
  const previous = collectArtifactVersions(
    create(FreightSchema, {
      commits: [{ repoURL: 'https://github.com/akuity/repo.git', id: 'abc1234' }],
      charts: [{ repoURL: 'https://charts.example.com', name: 'my-chart', version: '1.2.3' }],
      images: [{ repoURL: 'ghcr.io/akuity/image', tag: 'v1.0.0', digest: 'sha256:abc' }],
      artifacts: [{ subscriptionName: 'my-subscription', version: '9.9.9' }]
    })
  );

  it('reports unchanged artifacts', () => {
    expect(
      isArtifactChanged(
        create(GitCommitSchema, { repoURL: 'https://github.com/akuity/repo.git', id: 'abc1234' }),
        previous
      )
    ).toBe(false);

    expect(
      isArtifactChanged(
        create(ImageSchema, {
          repoURL: 'ghcr.io/akuity/image',
          tag: 'v2.0.0', // tag is irrelevant when digests match
          digest: 'sha256:abc'
        }),
        previous
      )
    ).toBe(false);

    expect(
      isArtifactChanged(
        create(ArtifactReferenceSchema, { subscriptionName: 'my-subscription', version: '9.9.9' }),
        previous
      )
    ).toBe(false);
  });

  it('reports changed artifacts', () => {
    expect(
      isArtifactChanged(
        create(GitCommitSchema, { repoURL: 'https://github.com/akuity/repo.git', id: 'def5678' }),
        previous
      )
    ).toBe(true);

    expect(
      isArtifactChanged(
        create(ChartSchema, {
          repoURL: 'https://charts.example.com',
          name: 'my-chart',
          version: '1.2.4'
        }),
        previous
      )
    ).toBe(true);
  });

  it('treats artifacts absent from the previous freight as changed', () => {
    expect(
      isArtifactChanged(
        create(ImageSchema, { repoURL: 'ghcr.io/akuity/other', tag: 'v1' }),
        previous
      )
    ).toBe(true);
  });

  it('distinguishes charts with the same repoURL by name', () => {
    expect(
      isArtifactChanged(
        create(ChartSchema, {
          repoURL: 'https://charts.example.com',
          name: 'other-chart',
          version: '1.2.3'
        }),
        previous
      )
    ).toBe(true);
  });
});

describe('previousArtifactVersion', () => {
  const previous = collectArtifactVersions(
    create(FreightSchema, {
      images: [{ repoURL: 'ghcr.io/akuity/image', tag: 'v1.0.0', digest: 'sha256:abc' }]
    })
  );

  it('returns the previous display version for a changed artifact', () => {
    expect(
      previousArtifactVersion(
        create(ImageSchema, {
          repoURL: 'ghcr.io/akuity/image',
          tag: 'v1.1.0',
          digest: 'sha256:def'
        }),
        previous
      )
    ).toBe('v1.0.0');
  });

  it('returns undefined for an unchanged artifact', () => {
    expect(
      previousArtifactVersion(
        create(ImageSchema, {
          repoURL: 'ghcr.io/akuity/image',
          tag: 'v1.0.0',
          digest: 'sha256:abc'
        }),
        previous
      )
    ).toBeUndefined();
  });

  it('returns undefined for an artifact new to this freight', () => {
    expect(
      previousArtifactVersion(
        create(ImageSchema, { repoURL: 'ghcr.io/akuity/new', tag: 'v1' }),
        previous
      )
    ).toBeUndefined();
  });
});
