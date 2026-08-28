import { queryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'
import type { components } from '../api/schema'

export type Run = components['schemas']['ApiRunResponse']

/**
 * One run, with its phases and artifact metadata.
 *
 * THE KEY IS `['run', runId]` AND IT IS THE ONE THE CACHE BRIDGE NAMES ON EVERY
 * WINDOW. A stream frame carries the run it belongs to, and the bridge
 * invalidates this key for that run and no other — so a phase transition on one
 * run does not refetch every run detail the cache is holding.
 *
 * It is the ONLY key on that per-window tier, since Task 12. The collections and
 * the event log used to go out on every frame beside it; now a phase-only window
 * defers them to one trailing refresh per `PHASE_TIER_THROTTLE_MS`, and only a
 * window carrying a non-`phase.*` kind sends the whole set at once. This key is
 * per-window because it is what the open run-detail screen is drawn from, and
 * it is invalidated `exact` because `['run', runId, 'events']` sits under it and
 * has its own tier (`realtime/cache-bridge.ts`).
 *
 * Both spellings are literal rather than a shared constant, deliberately, for
 * the reason `approvals-query.ts:10-14` records: a constant would make the two
 * agree by construction. What holds them together for the inbox is
 * `approvals.test.tsx`, end to end and through a real refetch. THIS key got its
 * own such test when run detail landed: `runs.$runId.test.tsx`'s "advances live
 * when a phase transition arrives" drives a real frame down a scripted stream
 * and waits for the rendered phase state to change, which is the only thing in
 * the tree that would notice a typo in either literal.
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
