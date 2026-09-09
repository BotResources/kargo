import { Tag } from 'antd';
import Link from 'antd/es/typography/Link';
import classNames from 'classnames';
import { ReactNode } from 'react';

import {
  isArtifactChart,
  isArtifactGitCommit,
  isArtifactImage
} from '@ui/features/assemble-freight/artifact-type-guards';
import {
  getGitCommitURL,
  getImageSource
} from '@ui/features/freight-timeline/open-container-initiative-utils';
import { ArtifactReference, Chart, GitCommit, Image } from '@ui/gen/api/v2/models';

import { ArtifactIcon } from './artifact-icon';
import { humanComprehendableArtifact } from './artifact-parts-utils';
import { shortVersion } from './short-version-utils';

type FreightArtifactProps = {
  artifact: GitCommit | Chart | Image | ArtifactReference;
  expand?: boolean;
  // version unchanged from the previous freight; render de-emphasized
  muted?: boolean;
  // the previous freight's version of this artifact, when it differs; renders
  // the diff view: artifact name + previous version struck through + current
  previousVersion?: string;
  // the artifact is absent from the previous freight entirely (a new package,
  // not a version bump); renders the diff view with a "new" marker in place of
  // the struck-through previous version
  added?: boolean;
};

export const FreightArtifact = (props: FreightArtifactProps) => {
  let Expand: ReactNode;

  if (props.expand) {
    Expand = (
      <span className='text-[10px] ml-1'>{humanComprehendableArtifact(props.artifact)}</span>
    );
  }

  const mutedProps = props.muted ? { color: 'default' } : {};

  // both a version bump and a new package use the diff layout: the artifact
  // name stacked above the version
  const isDiffView = !!props.previousVersion || !!props.added;

  // the diff view stacks the artifact name above the version; center both lines
  const tagClassName = classNames({
    'opacity-60': props.muted,
    'text-center': isDiffView
  });

  // bump: the previous version struck through; addition: a "new" marker. Both
  // sit just left of the current version so the two diff states read alike
  const Marker: ReactNode = props.previousVersion ? (
    <span className='line-through opacity-50 mr-1'>{props.previousVersion}</span>
  ) : props.added ? (
    <span className='uppercase opacity-50 mr-1 text-[10px] font-semibold'>new</span>
  ) : null;

  // in the diff view the artifact name sits on its own line above the
  // old -> new version (or "new" marker) to save horizontal space
  const Name: ReactNode = isDiffView ? (
    <div className='text-[10px] leading-3'>
      {'subscriptionName' in props.artifact
        ? props.artifact.subscriptionName
        : humanComprehendableArtifact(props.artifact)}
    </div>
  ) : null;

  if (isArtifactGitCommit(props.artifact)) {
    const url = getGitCommitURL(props.artifact.repoURL || '', props.artifact.id || '');

    // prioritize semver; use shortVersion for tags, 7-char slice for raw commit hashes
    const displayId = props.artifact.tag
      ? shortVersion(props.artifact.tag)
      : props.artifact.id?.slice(0, 7);

    const TagComponent = (
      <Tag
        title={props.artifact.repoURL}
        bordered={false}
        color='geekblue'
        key={props.artifact.id}
        {...mutedProps}
        className={tagClassName}
      >
        {Name}

        <ArtifactIcon artifact={props.artifact} className='mr-1' />

        {Marker}

        {displayId}

        {Expand}
      </Tag>
    );

    if (url) {
      return (
        <Link
          key={props.artifact.repoURL}
          href={url}
          target='_blank'
          onClick={(e) => e.stopPropagation()}
        >
          {TagComponent}
        </Link>
      );
    }

    return TagComponent;
  }

  if (isArtifactChart(props.artifact)) {
    return (
      <Tag
        title={`${props.artifact.repoURL}:${props.artifact.version}`}
        bordered={false}
        color='geekblue'
        key={props.artifact.repoURL}
        {...mutedProps}
        className={tagClassName}
      >
        {Name}

        <ArtifactIcon artifact={props.artifact} className='mr-1' />

        {Marker}

        {shortVersion(props.artifact.version)}

        {Expand}
      </Tag>
    );
  }

  if (isArtifactImage(props.artifact)) {
    let imageSourceFromOci = '';

    if (props.artifact.annotations) {
      imageSourceFromOci = getImageSource(props.artifact.annotations);
    }

    const TagComponent = (
      <Tag
        title={`${props.artifact.repoURL}:${props.artifact.tag}`}
        bordered={false}
        color='geekblue'
        key={props.artifact?.repoURL}
        {...mutedProps}
        className={classNames(tagClassName, 'hover:cursor-default')}
      >
        {Name}

        <ArtifactIcon artifact={props.artifact} className='mr-1' />

        {Marker}

        {shortVersion(props.artifact?.tag)}

        {Expand}
      </Tag>
    );

    if (imageSourceFromOci) {
      return (
        <Link
          key={props.artifact?.repoURL}
          href={imageSourceFromOci}
          target='_blank'
          onClick={(e) => e.stopPropagation()}
        >
          {TagComponent}
        </Link>
      );
    }

    return TagComponent;
  }

  return (
    <Tag color='geekblue' bordered={false} {...mutedProps} className={tagClassName}>
      {Name}

      {Marker}

      {shortVersion(props.artifact.version)}
    </Tag>
  );
};
