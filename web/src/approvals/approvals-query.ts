import { queryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import { AUTH_SCOPED_QUERY_OPTIONS } from '../api/query-client'

/**
 * The inbox, and the badge, and the only source of truth for either. The
 * stream never populates this — it only says the answer may have changed
 * (see realtime/cache-bridge.ts).
 *
 * The key is the one `createCacheBridge` invalidates. Both spellings are
 * literal `['approvals']` rather than a shared constant, deliberately: a
 * constant would make the two agree by construction and the invalidation
 * would then be true by definition rather than tested. `approvals.test.tsx`
 * is what holds them together, end to end and through a real refetch.
 */
export const approvalsQueryOptions = queryOptions({
  queryKey: ['approvals'],
  queryFn: async () => {
    const res = await unwrap(await api.GET('/api/v1/approvals'))
    // The contract marks `approvals` required AND nullable: required and
    // nullable are independent, and a Go nil slice marshals to null. The
    // handler happens to build `make([]ApprovalResponse, 0, len(list))`
    // (backend/internal/runs/api/api.go), so the wire carries `[]` today —
    // but reading `.length` off what the contract permits would throw a
    // TypeError on exactly the empty-inbox case, which is the case every
    // new user starts in.
    return res.approvals ?? []
  },
  ...AUTH_SCOPED_QUERY_OPTIONS,
})
