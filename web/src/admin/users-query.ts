import { queryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'

/**
 * The account list — PROMOTED here out of `projects/grants-section.tsx`, which
 * is where Task 20 parked it and said this task would move it.
 *
 * `GET /users` is admin-only (`adminOnly`, `identity/api/api.go`) and this is
 * the group that owns the resource, so its permanent home is here rather than
 * inside the first group that happened to need it. The grant form's user picker
 * now imports it: TWO spellings of `['users']` would be TWO cache entries, so a
 * user created on this screen would not appear in that picker until something
 * else evicted it — a staleness bug with no failing test anywhere.
 *
 * `tools/arch/import-graph.ts` is satisfied by this placement and it is worth
 * naming, because the rule reads backwards at first glance: the two consumers
 * are `admin/users-section.tsx` and `projects/grants-section.tsx`, whose common
 * ancestor is `src` — but the required home is CLAMPED to the target's own
 * group root, so `admin/` is the answer rather than a demand to hoist this to
 * the top of the tree.
 *
 * AUTH_SCOPED because the answer changes with who is asking: a member gets a
 * 403 here, and a session that is demoted mid-visit must stop rendering a list
 * it is no longer allowed to read.
 *
 * `?? []` because `UserListResponse.users` is required AND nullable — a Go nil
 * slice marshals to null. `handleListUsers` builds a non-nil slice today, so
 * the null is a contract permission rather than an observed body; the
 * `users: null` default in `test/render-app.tsx` is what keeps this coalesce on
 * a live path instead of leaving it unreachable from every test in the tree.
 */
export const usersQueryOptions = queryOptions({
  queryKey: ['users'],
  queryFn: async () => (await unwrap(await api.GET('/api/v1/users'))).users ?? [],
  ...AUTH_SCOPED_QUERY_OPTIONS,
})
