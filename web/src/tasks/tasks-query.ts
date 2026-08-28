import { queryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'
import type { components } from '../api/schema'

export type Task = components['schemas']['ApiTaskResponse']

/**
 * Every task the caller can see.
 *
 * UNPAGINATED, AND NOT BY THIS CLIENT'S CHOICE. `GET /tasks` takes no cursor
 * (runs/api/api.go:580-582 says so in the run list's comment: tasks and
 * approvals already have the unbounded cliff), so there is no page to ask for
 * and no `next_cursor` to discard. That asymmetry with `runsQueryOptions` — one
 * page of runs, all of the tasks — is what makes the Tasks screen's missing-run
 * case real rather than defensive: an old task's latest run is simply not on
 * the runs page, and the row has to render anyway.
 *
 * THE KEY IS `['tasks']` WITH NOTHING AFTER IT. There are no filters on this
 * screen — the grouping is computed client-side from a status the list already
 * carries — so a second element would be an empty object that only made the key
 * harder to read. The cache bridge invalidates this exact prefix, and every
 * frame still reaches it — but since Task 12 not every frame reaches it at
 * once: a window carrying any non-`phase.*` kind refetches this list
 * immediately, while a phase-only burst collapses into one trailing refresh per
 * `PHASE_TIER_THROTTLE_MS`. The Tasks screen's groups come from a run's STATUS,
 * which is a lifecycle fact, so the tier that moves a row is the immediate one;
 * the throttled tier is what keeps its PhaseDots advancing.
 */
export const tasksQueryOptions = queryOptions({
  queryKey: ['tasks'],
  queryFn: async () => {
    const res = await unwrap(await api.GET('/api/v1/tasks'))
    // `tasks` is required AND nullable: required and nullable are independent,
    // and a Go nil slice marshals to null. `handleListTasks` happens to build
    // `make([]TaskResponse, 0, len(list))` today, so the wire carries `[]` — but
    // reading an array method off what the contract permits throws on exactly
    // the case every new user starts in. Same trade, same reason, as
    // `runsQueryOptions`.
    return res.tasks ?? []
  },
  ...AUTH_SCOPED_QUERY_OPTIONS,
})
