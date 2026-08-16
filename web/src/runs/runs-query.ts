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
 * bridge invalidates the bare prefix `['runs']`, and react-query's filters
 * match by prefix unless `exact` is set — MEASURED on the pinned 5.101.4:
 * invalidating `['runs']` marked both `['runs',{status:'waiting'}]` and
 * `['runs',{}]` stale and left `['approvals']` alone. So a phase transition
 * refreshes the list the user is actually looking at, whatever they have
 * filtered to. Encoding the filters into the first element instead
 * (`['runs:waiting']`) would make that prefix match nothing, and the failure
 * would be invisible: the list would simply be as stale as the bounded refresh
 * floor allows. Nothing ASSERTS the match yet — it takes a mounted
 * `['runs', filters]` query to observe, which arrives with Task 13.
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
      // The contract marks `runs` required AND nullable: required and nullable
      // are independent, and a Go nil slice marshals to null. The handler
      // happens to build `make([]RunSummaryResponse, 0, len(list))`
      // (backend/internal/runs/api/dto.go:487), so the wire carries `[]`
      // today — but reading `.length` off what the contract permits would
      // throw a TypeError on exactly the empty case every new user starts in.
      return res.runs ?? []
    },
    ...AUTH_SCOPED_QUERY_OPTIONS,
  })
}
