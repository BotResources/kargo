import { faHourglassHalf, faPause } from '@fortawesome/free-solid-svg-icons';

import type {
  AutoPromotionHold,
  Freight,
  FreightReference,
  Stage
} from '@ui/gen/api/v1alpha1/generated_pb';
import type { AutoPromotionCandidate } from '@ui/gen/api/v2/models/autoPromotionCandidate';

export type OriginLike = {
  kind?: string;
  name?: string;
};

export type AutoPromotionHoldEntry = {
  key: string;
  hold: AutoPromotionHold;
  origin?: OriginLike;
};

export const autoPromotionHoldStateActive = 'Active';
export const autoPromotionHoldStatePending = 'Pending';

export const originKey = (origin?: OriginLike) => {
  if (!origin?.kind || !origin?.name) {
    return '';
  }
  return `${origin.kind}/${origin.name}`;
};

export const originLabel = (origin?: OriginLike) => {
  if (!origin?.kind || !origin?.name) {
    return 'this origin';
  }
  return `${origin.kind}/${origin.name}`;
};

export const getAutoPromotionCandidate = (
  candidates: AutoPromotionCandidate[] | undefined,
  origin?: OriginLike
) => {
  const key = originKey(origin);
  if (!key) {
    return undefined;
  }
  return candidates?.find((candidate) => originKey(candidate.origin) === key);
};

export const getAutoPromotionCandidateName = (
  candidates: AutoPromotionCandidate[] | undefined,
  freight: Pick<Freight | FreightReference, 'origin'> | undefined
) => getAutoPromotionCandidate(candidates, freight?.origin)?.freightName;

export const getAutoPromotionHold = (stage: Stage | undefined, origin?: OriginLike) => {
  const key = originKey(origin);
  if (!key) {
    return undefined;
  }
  return stage?.status?.autoPromotionHolds?.[key];
};

export const stageHasAutoPromotionHoldInState = (stage: Stage | undefined, state: string) =>
  Object.values(stage?.status?.autoPromotionHolds || {}).some((hold) => hold?.state === state);

export const holdStateIcon = (state?: string) =>
  state === autoPromotionHoldStatePending ? faHourglassHalf : faPause;

export const holdStateMessage = (state?: string) =>
  state === autoPromotionHoldStatePending
    ? 'Rollback promotion pending. Auto-promotion will pause if it succeeds.'
    : 'Auto-promotion paused after rollback.';

export const getAutoPromotionHoldEntries = (stage: Stage | undefined): AutoPromotionHoldEntry[] =>
  Object.entries(stage?.status?.autoPromotionHolds || {})
    .map(([key, hold]) => ({ key, hold, origin: hold.origin }))
    .sort((lhs, rhs) => {
      if (lhs.hold.state !== rhs.hold.state) {
        return lhs.hold.state === autoPromotionHoldStateActive ? -1 : 1;
      }
      return lhs.key.localeCompare(rhs.key);
    });
