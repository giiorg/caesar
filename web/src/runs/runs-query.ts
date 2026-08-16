import { queryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'
import type { components } from '../api/schema'

export type RunSummary = components['schemas']['ApiRunSummaryResponse']

/** The filters that are URL state on Runs and Tasks. */
export type RunFilters = { status?: string; taskId?: string }

/**
 * The runs list.
 *
 * THE KEY IS `['runs', filters]` AND THE SHAPE IS LOAD-BEARING. The cache
 * bridge invalidates the prefix `['runs']`, which react-query matches against
 * every filter variant — so a phase transition refreshes the list the user is
 * actually looking at, whatever they have filtered to. Encoding the filters
 * into the first element instead (`['runs:waiting']`) would make that prefix
 * match nothing, and the failure would be invisible: the list would simply be
 * as stale as the bounded refresh floor allows.
 *
 * ONE PAGE, deliberately. 12a's screens show the newest runs and infinite
 * scroll is not in the locked designs. The contract's `next_cursor` is
 * discarded here rather than absent from the wire, so the next plan can page
 * without a contract change.
 */
export function runsQueryOptions(filters: RunFilters = {}) {
  // Built by assignment rather than as a literal because tsconfig sets
  // `exactOptionalPropertyTypes`: `{ status: undefined }` is not assignable to
  // `{ status?: string }` under it, and an unfiltered list would not compile.
  const query: { status?: string; task_id?: string } = {}
  if (filters.status !== undefined) query.status = filters.status
  if (filters.taskId !== undefined) query.task_id = filters.taskId

  return queryOptions({
    queryKey: ['runs', filters],
    queryFn: async () => {
      const res = await unwrap(await api.GET('/api/v1/runs', { params: { query } }))
      // Required AND nullable, like every list in this contract: required and
      // nullable are independent, and a Go nil slice marshals to null. Reading
      // `.length` off what the contract permits would throw a TypeError on
      // exactly the empty case every new user starts in.
      return res.runs ?? []
    },
    ...AUTH_SCOPED_QUERY_OPTIONS,
  })
}
