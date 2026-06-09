import { faBolt } from '@fortawesome/free-solid-svg-icons';
import { FontAwesomeIcon } from '@fortawesome/react-fontawesome';

import type { Stage } from '@ui/gen/api/v1alpha1/generated_pb';

import {
  autoPromotionHoldStateActive,
  autoPromotionHoldStatePending,
  holdStateIcon,
  holdStateMessage,
  stageHasAutoPromotionHoldInState
} from './auto-promotion';

type AutoPromotionStatusIconProps = {
  stage: Stage;
  autoPromotionEnabled: boolean;
};

export const AutoPromotionStatusIcon = ({
  stage,
  autoPromotionEnabled
}: AutoPromotionStatusIconProps) => {
  // An active hold outranks a pending one when both exist.
  const holdState = stageHasAutoPromotionHoldInState(stage, autoPromotionHoldStateActive)
    ? autoPromotionHoldStateActive
    : stageHasAutoPromotionHoldInState(stage, autoPromotionHoldStatePending)
      ? autoPromotionHoldStatePending
      : undefined;

  if (!autoPromotionEnabled && !holdState) {
    return null;
  }

  const label = holdState ? holdStateMessage(holdState) : 'Auto-promotion enabled';

  return (
    <span title={label} aria-label={label} className='inline-flex mr-1.5 relative'>
      <FontAwesomeIcon icon={faBolt} className='text-[10px]' />
      {holdState && (
        <FontAwesomeIcon
          icon={holdStateIcon(holdState)}
          className='text-[7px] absolute'
          style={{ bottom: '-5px', right: '-3px' }}
        />
      )}
    </span>
  );
};
