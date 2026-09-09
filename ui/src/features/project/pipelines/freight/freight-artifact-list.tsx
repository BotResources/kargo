import { FontAwesomeIcon } from '@fortawesome/react-fontawesome';
import { Flex, Tooltip, Typography } from 'antd';
import { useMemo } from 'react';

import { Freight, FreightReference } from '@ui/gen/api/v2/models';

import { FreightArtifact } from './freight-artifact';
import {
  DEFAULT_MAX_ARTIFACTS,
  getFreightArtifacts,
  getHiddenFreightArtifactCounts
} from './freight-artifact-list-utils';
import {
  changedFirst,
  collectArtifactVersions,
  groupArtifactsByKind,
  isArtifactAdded,
  isArtifactChanged,
  previousArtifactVersion
} from './freight-changed-utils';

type FreightArtifactListProps = {
  freight?: Freight | FreightReference;
  // max is the number of artifacts to render before collapsing the rest into a
  // "+N more" indicator.
  max?: number;
  expand?: boolean;
  // when set, each artifact is compared against this freight: versions that
  // did not change render de-emphasized, versions that did show the previous
  // one struck through (or a "new" marker for an artifact the previous freight
  // lacked), and changed artifacts sort first so they stay visible despite
  // `max`.
  previousFreight?: Freight | FreightReference;
};

// FreightArtifactList renders up to `max` artifact tags for a piece of Freight,
// collapsing any overflow into a "+N more" indicator that reveals a per-kind
// breakdown of the hidden artifacts on hover. Shared by the freight timeline
// card and the Stage drawer's current-freight panel. It renders a flat fragment
// so callers control the surrounding layout.
export const FreightArtifactList = ({
  freight,
  max = DEFAULT_MAX_ARTIFACTS,
  expand,
  previousFreight
}: FreightArtifactListProps) => {
  const previousVersions = useMemo(
    () => (previousFreight ? collectArtifactVersions(previousFreight) : null),
    [previousFreight]
  );

  const artifacts = useMemo(() => {
    const all = getFreightArtifacts(freight);

    return previousVersions ? changedFirst(all, previousVersions) : all;
  }, [freight, previousVersions]);

  const overflow = artifacts.length - max;

  // changedFirst reorders the artifacts, so tally the ones actually sliced off
  const hiddenCounts = useMemo(
    () =>
      previousVersions
        ? getHiddenFreightArtifactCounts(groupArtifactsByKind(artifacts.slice(max)), 0)
        : getHiddenFreightArtifactCounts(freight, max),
    [artifacts, freight, max, previousVersions]
  );

  return (
    <>
      {artifacts.slice(0, max).map((artifact, i) => (
        <FreightArtifact
          key={i}
          artifact={artifact}
          expand={expand}
          muted={!!previousVersions && !isArtifactChanged(artifact, previousVersions)}
          previousVersion={
            previousVersions ? previousArtifactVersion(artifact, previousVersions) : undefined
          }
          added={!!previousVersions && isArtifactAdded(artifact, previousVersions)}
        />
      ))}
      {overflow > 0 && (
        <Tooltip
          title={
            <Flex vertical align='center'>
              {hiddenCounts.map(({ icon, count }) => (
                <div key={icon.iconName} className='text-[10px]'>
                  {count}x <FontAwesomeIcon icon={icon} />
                </div>
              ))}
            </Flex>
          }
        >
          <Typography.Text type='secondary' className='text-[10px] cursor-default'>
            +{overflow} more
          </Typography.Text>
        </Tooltip>
      )}
    </>
  );
};
