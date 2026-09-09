import { describe, expect, it } from 'vitest';

import { ArtifactReference, Freight, GitCommit, Image } from '@ui/gen/api/v2/models';

import {
  changedFirst,
  collectArtifactVersions,
  compareFreightArtifacts,
  indexPreviousFreight,
  isArtifactAdded,
  isArtifactChanged,
  previousArtifactVersion
} from './freight-changed-utils';

const freight = (spec: Partial<Freight>): Freight => spec as Freight;

describe('collectArtifactVersions', () => {
  it('returns an empty map for undefined freight', () => {
    expect(collectArtifactVersions(undefined).size).toBe(0);
  });

  it('collects versions for all artifact kinds', () => {
    const versions = collectArtifactVersions(
      freight({
        commits: [{ repoURL: 'https://github.com/akuity/repo.git', id: 'abc1234def5678' }],
        charts: [{ repoURL: 'https://charts.example.com', name: 'my-chart', version: '1.2.3' }],
        images: [{ repoURL: 'ghcr.io/akuity/image', tag: 'v1.0.0', digest: 'sha256:abc' }],
        artifacts: [{ subscriptionName: 'my-subscription', version: '9.9.9' }]
      })
    );

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
    const versions = collectArtifactVersions(
      freight({
        commits: [
          { repoURL: 'https://github.com/akuity/repo.git', id: 'abc1234def5678', tag: 'v2.0.0' }
        ]
      })
    );

    expect(versions.get('commit:https://github.com/akuity/repo.git')).toEqual({
      version: 'abc1234def5678',
      display: 'v2.0.0'
    });
  });

  it('falls back to the tag when an image has no digest', () => {
    const versions = collectArtifactVersions(
      freight({ images: [{ repoURL: 'ghcr.io/akuity/image', tag: 'v1.0.0' }] })
    );

    expect(versions.get('image:ghcr.io/akuity/image')).toEqual({
      version: 'v1.0.0',
      display: 'v1.0.0'
    });
  });

  it('tells a chart without a name apart from a generic artifact', () => {
    const versions = collectArtifactVersions(
      freight({
        charts: [{ repoURL: 'oci://ghcr.io/akuity/charts/kargo', version: '1.2.3' }],
        artifacts: [{ subscriptionName: 'other', artifactType: 'custom', version: '1.2.3' }]
      })
    );

    expect([...versions.keys()]).toEqual([
      'chart:oci://ghcr.io/akuity/charts/kargo:',
      'artifact:other'
    ]);
  });
});

describe('isArtifactChanged', () => {
  const previous = collectArtifactVersions(
    freight({
      commits: [{ repoURL: 'https://github.com/akuity/repo.git', id: 'abc1234' }],
      charts: [{ repoURL: 'https://charts.example.com', name: 'my-chart', version: '1.2.3' }],
      images: [{ repoURL: 'ghcr.io/akuity/image', tag: 'v1.0.0', digest: 'sha256:abc' }],
      artifacts: [{ subscriptionName: 'my-subscription', version: '9.9.9' }]
    })
  );

  it('reports unchanged artifacts', () => {
    expect(
      isArtifactChanged({ repoURL: 'https://github.com/akuity/repo.git', id: 'abc1234' }, previous)
    ).toBe(false);

    expect(
      isArtifactChanged(
        // tag is irrelevant when digests match
        { repoURL: 'ghcr.io/akuity/image', tag: 'v2.0.0', digest: 'sha256:abc' },
        previous
      )
    ).toBe(false);

    expect(
      isArtifactChanged({ subscriptionName: 'my-subscription', version: '9.9.9' }, previous)
    ).toBe(false);
  });

  it('reports changed artifacts', () => {
    expect(
      isArtifactChanged({ repoURL: 'https://github.com/akuity/repo.git', id: 'def5678' }, previous)
    ).toBe(true);

    expect(
      isArtifactChanged(
        // same tag, new digest: the tag was re-pointed
        { repoURL: 'ghcr.io/akuity/image', tag: 'v1.0.0', digest: 'sha256:def' },
        previous
      )
    ).toBe(true);

    expect(
      isArtifactChanged(
        { repoURL: 'https://charts.example.com', name: 'my-chart', version: '1.2.4' },
        previous
      )
    ).toBe(true);

    expect(
      isArtifactChanged({ subscriptionName: 'my-subscription', version: '10.0.0' }, previous)
    ).toBe(true);
  });

  it('reports artifacts absent from the previous freight as changed', () => {
    expect(
      isArtifactChanged({ repoURL: 'ghcr.io/akuity/other', tag: 'v1.0.0', digest: 'x' }, previous)
    ).toBe(true);
  });

  it('reports everything changed when there is no previous freight', () => {
    const empty = collectArtifactVersions(undefined);

    expect(
      isArtifactChanged({ repoURL: 'https://github.com/akuity/repo.git', id: 'abc1234' }, empty)
    ).toBe(true);
  });
});

describe('isArtifactAdded', () => {
  const previous = collectArtifactVersions(
    freight({ images: [{ repoURL: 'ghcr.io/akuity/image', tag: 'v1.0.0', digest: 'sha256:abc' }] })
  );

  it('is true only for artifacts the previous freight did not contain', () => {
    expect(
      isArtifactAdded({ repoURL: 'ghcr.io/akuity/other', tag: 'v1.0.0', digest: 'x' }, previous)
    ).toBe(true);

    // a version bump is not an addition
    expect(
      isArtifactAdded({ repoURL: 'ghcr.io/akuity/image', tag: 'v2.0.0', digest: 'y' }, previous)
    ).toBe(false);
  });
});

describe('previousArtifactVersion', () => {
  const previous = collectArtifactVersions(
    freight({
      commits: [{ repoURL: 'https://github.com/akuity/repo.git', id: 'abc1234def', tag: 'v1.0.0' }],
      images: [{ repoURL: 'ghcr.io/akuity/image', tag: 'v1.0.0', digest: 'sha256:abc' }]
    })
  );

  it('returns the previous display version for a changed artifact', () => {
    const image: Image = { repoURL: 'ghcr.io/akuity/image', tag: 'v1.1.0', digest: 'sha256:def' };
    const commit: GitCommit = {
      repoURL: 'https://github.com/akuity/repo.git',
      id: 'fff0000',
      tag: 'v1.1.0'
    };

    expect(previousArtifactVersion(image, previous)).toBe('v1.0.0');
    expect(previousArtifactVersion(commit, previous)).toBe('v1.0.0');
  });

  it('returns undefined for unchanged or added artifacts', () => {
    expect(
      previousArtifactVersion(
        { repoURL: 'ghcr.io/akuity/image', tag: 'v1.0.0', digest: 'sha256:abc' },
        previous
      )
    ).toBeUndefined();

    const added: ArtifactReference = { subscriptionName: 'new', version: '1' };

    expect(previousArtifactVersion(added, previous)).toBeUndefined();
  });
});

describe('changedFirst', () => {
  it('moves changed artifacts ahead of unchanged ones, keeping order within each group', () => {
    const previous = collectArtifactVersions(
      freight({
        images: [
          { repoURL: 'a', tag: '1', digest: 'a1' },
          { repoURL: 'b', tag: '1', digest: 'b1' },
          { repoURL: 'c', tag: '1', digest: 'c1' },
          { repoURL: 'd', tag: '1', digest: 'd1' }
        ]
      })
    );

    const ordered = changedFirst(
      [
        { repoURL: 'a', tag: '1', digest: 'a1' },
        { repoURL: 'b', tag: '2', digest: 'b2' },
        { repoURL: 'c', tag: '1', digest: 'c1' },
        { repoURL: 'd', tag: '2', digest: 'd2' }
      ],
      previous
    );

    expect(ordered.map((i) => i.repoURL)).toEqual(['b', 'd', 'a', 'c']);
  });
});

describe('compareFreightArtifacts', () => {
  const previous = freight({
    commits: [{ repoURL: 'https://github.com/akuity/repo.git', id: 'abc1234def', tag: 'v1.0.0' }],
    charts: [{ repoURL: 'https://charts.example.com', name: 'old-chart', version: '1.0.0' }],
    images: [
      { repoURL: 'ghcr.io/akuity/same', tag: 'v1.0.0', digest: 'sha256:same' },
      { repoURL: 'ghcr.io/akuity/bumped', tag: 'v1.0.0', digest: 'sha256:old' }
    ]
  });

  const current = freight({
    commits: [{ repoURL: 'https://github.com/akuity/repo.git', id: 'fff0000000', tag: 'v1.1.0' }],
    images: [
      { repoURL: 'ghcr.io/akuity/same', tag: 'v1.0.0', digest: 'sha256:same' },
      { repoURL: 'ghcr.io/akuity/bumped', tag: 'v1.1.0', digest: 'sha256:new' },
      { repoURL: 'ghcr.io/akuity/added', tag: 'v0.1.0', digest: 'sha256:added' }
    ]
  });

  it('annotates every current artifact, appends removed ones, and puts changes first', () => {
    const rows = compareFreightArtifacts(current, previous);

    expect(
      rows.map((row) => [
        row.type === 'other' ? row.subscriptionName : row.repoURL,
        row.change.status,
        row.change.previous && 'tag' in row.change.previous ? row.change.previous.tag : undefined
      ])
    ).toEqual([
      ['ghcr.io/akuity/bumped', 'changed', 'v1.0.0'],
      ['ghcr.io/akuity/added', 'added', undefined],
      ['https://github.com/akuity/repo.git', 'changed', 'v1.0.0'],
      ['ghcr.io/akuity/same', 'unchanged', undefined],
      ['https://charts.example.com', 'removed', undefined]
    ]);

    // the previous row keeps its full detail so the table can show the
    // previous version in the same form as the current one
    expect(rows[2].change.previous).toMatchObject({ type: 'git', id: 'abc1234def' });
  });

  it('treats everything as added without a previous freight', () => {
    const rows = compareFreightArtifacts(current, undefined);

    expect(rows.map((row) => row.change.status)).toEqual(['added', 'added', 'added', 'added']);
  });

  it('carries the digest through the table rows so a re-pointed tag counts as a change', () => {
    const rows = compareFreightArtifacts(
      freight({ images: [{ repoURL: 'ghcr.io/akuity/img', tag: 'latest', digest: 'sha256:b' }] }),
      freight({ images: [{ repoURL: 'ghcr.io/akuity/img', tag: 'latest', digest: 'sha256:a' }] })
    );

    expect(rows[0].change).toMatchObject({
      status: 'changed',
      previous: { type: 'image', tag: 'latest', digest: 'sha256:a' }
    });
  });
});

describe('indexPreviousFreight', () => {
  const at = (hoursAgo: number) => new Date(Date.UTC(2026, 0, 10, 12 - hoursAgo)).toISOString();

  const w1a = freight({
    metadata: { name: 'w1-a', creationTimestamp: at(3) },
    origin: { name: 'w1' }
  });
  const w1b = freight({
    metadata: { name: 'w1-b', creationTimestamp: at(2) },
    origin: { name: 'w1' }
  });
  const w2a = freight({
    metadata: { name: 'w2-a', creationTimestamp: at(1) },
    origin: { name: 'w2' }
  });
  const w1c = freight({
    metadata: { name: 'w1-c', creationTimestamp: at(0) },
    origin: { name: 'w1' }
  });

  it('pairs each freight with the previous one from the same warehouse', () => {
    const previous = indexPreviousFreight([w1c, w2a, w1b, w1a]);

    expect(previous['w1-c']).toBe(w1b);
    expect(previous['w1-b']).toBe(w1a);
    expect(previous['w1-a']).toBeUndefined();
    expect(previous['w2-a']).toBeUndefined();
  });

  it('does not depend on the input order', () => {
    expect(indexPreviousFreight([w1a, w1c, w2a, w1b])['w1-c']).toBe(w1b);
  });

  it('skips collapsed-freight placeholders', () => {
    const placeholder = { ...w1b, count: 3 };

    const previous = indexPreviousFreight([w1c, placeholder, w1a]);

    expect(previous['w1-c']).toBe(w1a);
    expect(previous['w1-b']).toBeUndefined();
  });
});
