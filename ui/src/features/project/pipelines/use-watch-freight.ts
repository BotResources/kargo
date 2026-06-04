import { createClient } from '@connectrpc/connect';
import { createConnectQueryKey } from '@connectrpc/connect-query';
import { useQueryClient } from '@tanstack/react-query';
import { useEffect, useRef } from 'react';

import { transportWithAuth } from '@ui/config/transport';
import { queryCache } from '@ui/features/utils/cache';
import {
  isExpiredResourceVersionError,
  isSameOrOlderResourceVersion
} from '@ui/features/utils/resource-version';
import { queryFreight } from '@ui/gen/api/service/v1alpha1/service-KargoService_connectquery';
import { KargoService, QueryFreightResponse } from '@ui/gen/api/service/v1alpha1/service_pb';
import { Freight } from '@ui/gen/api/v1alpha1/generated_pb';

const queryFreightKey = (project: string, origins: string[]) =>
  createConnectQueryKey({
    cardinality: 'finite',
    schema: queryFreight,
    input: { project, origins },
    transport: transportWithAuth
  });

const upsertFreight = (
  current: QueryFreightResponse | undefined,
  freight: Freight
): QueryFreightResponse => {
  const existing = current?.groups?.['']?.freight || [];
  const found = existing.some((f) => f?.metadata?.name === freight?.metadata?.name);
  const updated = found
    ? existing.map((f) => (f?.metadata?.name === freight?.metadata?.name ? freight : f))
    : [...existing, freight];
  return {
    ...current,
    resourceVersion: freight.metadata?.resourceVersion || current?.resourceVersion || '',
    groups: {
      ...current?.groups,
      '': { ...current?.groups?.[''], freight: updated }
    }
  } as QueryFreightResponse;
};

const deleteFreight = (
  current: QueryFreightResponse | undefined,
  freight: Freight
): QueryFreightResponse =>
  ({
    ...current,
    resourceVersion: freight.metadata?.resourceVersion || current?.resourceVersion || '',
    groups: {
      ...current?.groups,
      '': {
        ...current?.groups?.[''],
        freight: (current?.groups?.['']?.freight || []).filter(
          (f) => f?.metadata?.name !== freight?.metadata?.name
        )
      }
    }
  }) as QueryFreightResponse;

export const useWatchFreight = (
  project: string,
  origins: string[],
  resourceVersion: string,
  enabled: boolean
) => {
  const client = useQueryClient();

  // Keep the latest seed resourceVersion in a ref rather than an effect
  // dependency. Each freight event writes back into the same queryFreight cache
  // entry this value is derived from, so depending on it would abort and
  // restart the watch on every event. The watch loop reads the freshest
  // resourceVersion from the cache and only falls back to this seed for the
  // initial connect.
  const seedResourceVersionRef = useRef(resourceVersion);
  seedResourceVersionRef.current = resourceVersion;

  useEffect(() => {
    if (!project || !enabled) {
      return;
    }

    const cancel = new AbortController();

    const watchFreight = async () => {
      const promiseClient = createClient(KargoService, transportWithAuth);

      while (!cancel.signal.aborted) {
        const currentResourceVersion =
          queryCache.freight.get(project, origins)?.resourceVersion ||
          seedResourceVersionRef.current ||
          '';
        const stream = promiseClient.watchFreight(
          {
            project,
            origins,
            resourceVersion: currentResourceVersion
          },
          { signal: cancel.signal }
        );

        try {
          for await (const e of stream) {
            const freight = e.freight;

            if (!freight) {
              continue;
            }

            const currentFreight = queryCache.freight.get(project, origins);

            // Skip ADDED events for freight that already exists in the cache.
            // Kubernetes watches replay all existing objects as ADDED on connect,
            // which duplicates the initial GET and causes unnecessary re-renders.
            if (e.type === 'ADDED') {
              const existing = currentFreight?.groups?.['']?.freight || [];
              const current = existing.find((f) => f?.metadata?.name === freight?.metadata?.name);
              if (isSameOrOlderResourceVersion(current, freight)) {
                continue;
              }
            }

            if (e.type === 'DELETED') {
              // Remove from all queryFreight caches for this project, including
              // warehouse-filtered variants, which use a different cache key.
              client.setQueriesData<QueryFreightResponse>(
                {
                  queryKey: createConnectQueryKey({
                    cardinality: 'finite',
                    schema: queryFreight,
                    input: { project },
                    transport: transportWithAuth
                  }),
                  exact: false
                },
                (current) => deleteFreight(current, freight)
              );
            } else {
              const updatedFreight = upsertFreight(currentFreight, freight);
              client.setQueryData(queryFreightKey(project, origins), updatedFreight);
            }
          }
          return;
        } catch (err) {
          if (cancel.signal.aborted) {
            return;
          }
          if (isExpiredResourceVersionError(err)) {
            await client.refetchQueries({
              queryKey: queryFreightKey(project, origins),
              exact: true
            });
            continue;
          }
          throw err;
        }
      }
    };

    watchFreight().catch(() => undefined);

    return () => cancel.abort();
    // resourceVersion is intentionally excluded; it is read via
    // seedResourceVersionRef so freight events don't restart the watch.
  }, [client, enabled, origins, project]);
};
