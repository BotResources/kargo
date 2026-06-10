import { Tag } from 'antd';
import Link from 'antd/es/typography/Link';
import classNames from 'classnames';
import { ReactNode } from 'react';

import {
  getGitCommitURL,
  getImageSource
} from '@ui/features/freight-timeline/open-container-initiative-utils';
import {
  Chart,
  ArtifactReference as GenericArtifactReference,
  GitCommit,
  Image
} from '@ui/gen/api/v1alpha1/generated_pb';

import { ArtifactIcon } from './artifact-icon';
import { humanComprehendableArtifact } from './artifact-parts-utils';
import { shortVersion } from './short-version-utils';

type FreightArtifactProps = {
  artifact: GitCommit | Chart | Image | GenericArtifactReference;
  expand?: boolean;
  // version unchanged from the previous freight; render de-emphasized
  muted?: boolean;
  // the previous freight's version of this artifact, when it differs; renders
  // the diff view: artifact name + previous version struck through + current
  previousVersion?: string;
};

export const FreightArtifact = (props: FreightArtifactProps) => {
  const artifactType = props.artifact?.$typeName;

  const mutedProps = props.muted ? { color: 'default' } : {};

  // the diff view stacks the artifact name above the version; center both lines
  const tagClassName = classNames({
    'opacity-60': props.muted,
    'text-center': !!props.previousVersion
  });

  const Previous: ReactNode = props.previousVersion ? (
    <span className='line-through opacity-50 mr-1'>{props.previousVersion}</span>
  ) : null;

  if (artifactType === 'github.com.akuity.kargo.api.v1alpha1.ArtifactReference') {
    return (
      <Tag color='geekblue' bordered={false} {...mutedProps} className={tagClassName}>
        {Previous}
        {shortVersion(props.artifact.version)}
      </Tag>
    );
  }

  let Expand: ReactNode;

  if (props.expand) {
    Expand = (
      <span className='text-[10px] ml-1'>
        {humanComprehendableArtifact(props.artifact.repoURL)}
      </span>
    );
  }

  // in the diff view the artifact name sits on its own line above the
  // old -> new version to save horizontal space
  const Name: ReactNode = props.previousVersion ? (
    <div className='text-[10px] leading-3'>
      {humanComprehendableArtifact(props.artifact.repoURL)}
    </div>
  ) : null;

  if (artifactType === 'github.com.akuity.kargo.api.v1alpha1.GitCommit') {
    const url = getGitCommitURL(props.artifact.repoURL, props.artifact.id);

    // prioritize semver; use shortVersion for tags, 7-char slice for raw commit hashes
    const displayId = props.artifact.tag
      ? shortVersion(props.artifact.tag)
      : props.artifact.id.slice(0, 7);

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

        <ArtifactIcon artifactType={artifactType} className='mr-1' />

        {Previous}

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

  if (artifactType === 'github.com.akuity.kargo.api.v1alpha1.Chart') {
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

        <ArtifactIcon artifactType={artifactType} className='mr-1' />

        {Previous}

        {shortVersion(props.artifact.version)}

        {Expand}
      </Tag>
    );
  }

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

      <ArtifactIcon artifactType={artifactType} className='mr-1' />

      {Previous}

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
};
