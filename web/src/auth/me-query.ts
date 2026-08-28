import { queryOptions, useSuspenseQuery } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'

export const meQueryOptions = queryOptions({
  queryKey: ['auth', 'me'],
  queryFn: async () => unwrap(await api.GET('/api/v1/auth/me')),
  ...AUTH_SCOPED_QUERY_OPTIONS,
  // The session's own probe: a failure here is an ANSWER ("no session"), not a
  // transport problem to retry into.
  retry: false,
})

/**
 * The signed-in user and their grants, for the screens that BRANCH on them.
 *
 * IT LIVES BESIDE THE QUERY IT WRAPS, and it did not always. `useMe` was
 * declared in `routes/_app.tsx` when Task 4 exported it with no consumers, and
 * that stayed invisible while `repos.index.tsx` was the only importer. The
 * second one made `tools/arch/import-graph.ts` demand that `routes/_app.tsx`
 * move into `routes/_app/` — where TanStack's file convention cannot put a
 * layout route, since the path would become `/_app/_app`. The rule was pointing
 * at a real misplacement rather than at an awkward case of itself: a hook over
 * `meQueryOptions` is an `auth/` concern that happened to be declared in a route
 * file. Moving it here is what the checker was asking for, and the
 * unsatisfiable-looking demand goes away with it.
 *
 * `useSuspenseQuery`, not `useQuery`: `_app`'s loader has already awaited this
 * exact query on every protected route, so it resolves from the cache and adds
 * no request. A caller mounted outside that layout would suspend, which is the
 * honest behaviour — there is no signed-in user to branch on yet.
 */
export function useMe() {
  return useSuspenseQuery(meQueryOptions).data
}
