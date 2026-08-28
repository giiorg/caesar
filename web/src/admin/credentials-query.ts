import { queryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'
import type { components } from '../api/schema'

export type Credential = components['schemas']['ApiCredentialResponse']

/**
 * The credential list — PROMOTED here out of `projects/repo-form.tsx`, which is
 * where Task 19 parked it and said this task would move it, for the reason that
 * file wrote down: `GET /credentials` is admin-only and this is the group that
 * owns the resource, so its permanent home is here rather than inside the first
 * group that happened to need it.
 *
 * THREE CONSUMERS NOW, which is what makes the move obligatory rather than
 * tidy: `admin/credentials-section.tsx` renders the list, `admin/harness-form`
 * filters it into a picker, and `projects/repo-form.tsx` filters it into a
 * different one. Two spellings of `['credentials']` would be two cache entries,
 * so a credential created on the Credentials screen would not appear in the
 * repo form's picker until something else evicted it — a staleness bug with no
 * failing test anywhere. Same argument `users-query.ts` records.
 *
 * `tools/arch/import-graph.ts` puts it here and the rule reads backwards at
 * first glance: the consumers' common ancestor is `src`, but the required home
 * is CLAMPED to the target's own group root, so `admin/` is the answer rather
 * than a demand to hoist this to the top of the tree.
 *
 * AUTH_SCOPED because the answer changes with who is asking: a member gets a
 * 403 here, and a session demoted mid-visit must stop rendering a list it is no
 * longer allowed to read.
 *
 * `?? []` because `CredentialListResponse.credentials` is required AND
 * nullable — a Go nil slice marshals to null. The coalesce is KILLABLE and that
 * took deliberate spelling downstream: `repo-form.tsx`'s picker narrows with
 * `if (credentials === undefined)` rather than `?? []` precisely so that
 * deleting this coalesce reddens a test instead of being absorbed by the
 * caller. See that function's comment for the measurement.
 */
export const credentialsQueryOptions = queryOptions({
  queryKey: ['credentials'],
  queryFn: async () => (await unwrap(await api.GET('/api/v1/credentials'))).credentials ?? [],
  ...AUTH_SCOPED_QUERY_OPTIONS,
})
