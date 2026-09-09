import { useMemo } from 'react';

import { useFreightTimelineControllerContext } from '@ui/features/project/pipelines/context/freight-timeline-controller-context';
import { indexPreviousFreight } from '@ui/features/project/pipelines/freight/freight-changed-utils';
import { useFilteredFreights } from '@ui/features/project/pipelines/freight/use-filtered-freights';
import { defaultPreferredFilter } from '@ui/features/project/pipelines/url-params/use-freight-timeline-controller-store';
import { Freight } from '@ui/gen/api/v2/models';

// usePreviousFreight resolves the piece of freight another one is compared
// with: the chronologically previous one from the same warehouse among the
// freight the timeline's filters leave visible -- the same baseline the
// timeline's highlight-changes option uses. It also exposes that option, which
// the freight details drawer shares with the timeline.
export const usePreviousFreight = (freight?: Freight, freights?: Freight[]) => {
  const ctx = useFreightTimelineControllerContext();
  const preferredFilter = ctx?.preferredFilter || defaultPreferredFilter;

  const visibleFreights = useFilteredFreights(freights || [], preferredFilter);

  const previousFreight = useMemo(
    () => indexPreviousFreight(visibleFreights)[freight?.metadata?.name || ''],
    [visibleFreights, freight]
  );

  return {
    previousFreight,
    highlightChanges: !!preferredFilter.highlightChanges,
    setHighlightChanges: (highlightChanges: boolean) =>
      ctx?.setPreferredFilter({ ...preferredFilter, highlightChanges }),
    // the option lives in the timeline's filter state; without it there is
    // nothing to toggle
    canToggle: !!ctx
  };
};
