import { queryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'
import type { components } from '../api/schema'

export type Run = components['schemas']['ApiRunResponse']

/**
 * One run, with its phases and artifact metadata.
 *
 * THE KEY IS `['run', runId]` AND IT IS THE ONE THE CACHE BRIDGE NAMES. A
 * stream frame carries the run it belongs to, and the bridge invalidates this
 * key for that run and no other — so a phase transition on one run does not
 * refetch every run detail the cache is holding. It is not the only key the
 * window invalidates: `['runs']` goes out on every frame and `['approvals']`
 * on an attention kind (realtime/cache-bridge.ts fire()).
 *
 * Both spellings are literal rather than a shared constant, deliberately, for
 * the reason `approvals-query.ts:10-14` records: a constant would make the two
 * agree by construction. What holds them together for the inbox is
 * `approvals.test.tsx`, end to end and through a real refetch. THERE IS NO
 * SUCH TEST FOR THIS KEY YET — the bridge's own suite asserts the string it
 * emits and nothing observes it. Task 16 is the first screen that can, and it
 * should assert the round trip rather than the spelling.
 *
 * No events here. `GET /runs/{id}/events` stays the poll-and-resume path and
 * no 12a screen subscribes to it; a run's timeline is built from the `phases`
 * this response already carries.
 */
export function runQueryOptions(runId: string) {
  return queryOptions({
    queryKey: ['run', runId],
    queryFn: async () =>
      unwrap(await api.GET('/api/v1/runs/{id}', { params: { path: { id: runId } } })),
    ...AUTH_SCOPED_QUERY_OPTIONS,
  })
}
