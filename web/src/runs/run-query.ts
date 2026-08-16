import { queryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'
import type { components } from '../api/schema'

export type Run = components['schemas']['ApiRunResponse']

/**
 * One run, with its phases and artifact metadata.
 *
 * THE KEY IS `['run', runId]` AND IT IS THE ONE THE CACHE BRIDGE NAMES. A
 * stream frame carries the run it belongs to, and the bridge invalidates
 * exactly this key for it — so a phase transition on the run being watched
 * refetches that run and nothing else. Both spellings are literal rather than
 * a shared constant, deliberately, for the reason `approvals-query.ts`
 * records: a constant would make the two agree by construction and the
 * invalidation would be true by definition rather than tested.
 *
 * No events here. `GET /runs/{id}/events` stays the poll-and-resume path and
 * no 12a screen subscribes to it; this run's timeline comes from the phases
 * the view returns.
 */
export function runQueryOptions(runId: string) {
  return queryOptions({
    queryKey: ['run', runId],
    queryFn: async () =>
      unwrap(await api.GET('/api/v1/runs/{id}', { params: { path: { id: runId } } })),
    ...AUTH_SCOPED_QUERY_OPTIONS,
  })
}
