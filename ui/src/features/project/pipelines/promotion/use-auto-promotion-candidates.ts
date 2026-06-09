import { useGetStageAutoPromotionCandidates } from '@ui/gen/api/v2/core/core';

// useAutoPromotionCandidates wraps the generated auto-promotion-candidates
// query and unwraps the response envelope. The raw query is exposed so
// callers can inspect isLoading/isError/refetch; customFetch throws on
// non-2xx, so isError is the only failure channel.
export const useAutoPromotionCandidates = (project: string, stage: string, enabled: boolean) => {
  const query = useGetStageAutoPromotionCandidates(project, stage, { query: { enabled } });
  return {
    query,
    candidates: query.data?.status === 200 ? query.data.data.candidates : undefined
  };
};
