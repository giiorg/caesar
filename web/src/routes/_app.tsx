import { useQuery, useSuspenseQuery } from '@tanstack/react-query'
import {
  createFileRoute,
  type ErrorComponentProps,
  Outlet,
  redirect,
  useNavigate,
} from '@tanstack/react-router'
import { useEffect } from 'react'
import { ApiError } from '../api/errors'
import { approvalsQueryOptions } from '../approvals/approvals-query'
import { meQueryOptions } from '../auth/me-query'
import { ApiErrorPanel } from '../errors/api-error-panel'
import { StreamStatusProvider } from '../realtime/stream-status'
import { useAttentionStream } from '../realtime/use-attention-stream'
import { AppShell } from '../shell/app-shell'

/**
 * Every protected route nests under this layout, and its loader resolves the
 * session BEFORE anything renders.
 *
 * A route that fetches nothing of its own — /more, /admin, /repos — gives a
 * per-fetch 401 guard nothing to act on, so the session has to be resolved by
 * the layout rather than by the routes. Without this loader the visitor still
 * reaches /login, but only once AppLayout has rendered and its own query has
 * thrown, and then from the error boundary onto a PUSHED entry with the
 * unviewable route sitting behind it. `_app.test.tsx`'s first two cases assert
 * the replace-not-push that doing it here buys, on both route shapes.
 */
export const Route = createFileRoute('/_app')({
  loader: async ({ context }) => {
    try {
      await context.queryClient.ensureQueryData(meQueryOptions)
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) throw redirect({ to: '/login' })
      throw e
    }
  },
  component: AppLayout,
  errorComponent: AppErrorBoundary,
})

function AppLayout() {
  /**
   * `error` IS READ, AND IT IS NOT REDUNDANT WITH SUSPENSE — it is the only
   * thing that can see a session expiring in the background.
   *
   * useSuspenseQuery does NOT throw once the query holds data. It hard-sets
   * `throwOnError` to its own default AFTER spreading the caller's options
   * (react-query 5.101.4, useSuspenseQuery.js), and that default is
   * `(_error, query) => query.state.data === undefined`. So passing
   * `throwOnError: true` through meQueryOptions is silently discarded, and a
   * refetch that 401s leaves the stale `data` in place. Nothing throws, and
   * without the check below the shell would keep rendering to an expired
   * session indefinitely — exactly what this layout exists to prevent,
   * deferred past the first load.
   *
   * The same non-throwing behaviour is what makes `.error` readable here at
   * all: the failure is reported as state rather than raised. The plan
   * specified a SECOND, disabled observer
   * (`useQuery({ ...meQueryOptions, enabled: false, throwOnError: false })`)
   * for this, to avoid doubling AUTH_SCOPED_QUERY_OPTIONS' interval and focus
   * refetch. MEASURED: unnecessary. Both observers read the same cache entry
   * and report the same error, so the extra hook adds a subscription and no
   * information. Dropping it changes no test and no fetch count; making the
   * expiry unobservable (`const error = undefined`) still fails the
   * expired-session test, so this line remains falsifiable.
   */
  const { data, error } = useSuspenseQuery(meQueryOptions)
  /**
   * THE ONE STREAM THIS TAB OPENS. Everything below reads `useStreamStatus()`;
   * a second call to the hook would be a second connection.
   */
  const status = useAttentionStream()
  /**
   * The badge count, and the reason it is a plain `useQuery` here rather than
   * the route's `useSuspenseQuery`.
   *
   * The badge is chrome: it is rendered on every protected page, including the
   * five that have nothing to do with approvals, so it must not suspend them
   * and it must not take them down. On failure `data` is undefined and the
   * badge simply does not render — the same rendering as an empty inbox, which
   * is a real cost stated plainly: a broken approvals endpoint looks like a
   * quiet one from the nav. The screen that must not lie about it is
   * `/approvals` itself, and there the loader plus `useSuspenseQuery` route the
   * failure into `AppErrorBoundary` where it becomes visible with its
   * correlation id.
   *
   * A 401 is the exception and it never reaches this line: `unwrap` sends every
   * non-exempt 401 through the client's redirect interceptor.
   */
  const approvals = useQuery(approvalsQueryOptions)
  const navigate = useNavigate()
  const expired = error instanceof ApiError && error.status === 401
  useEffect(() => {
    if (expired) void navigate({ to: '/login' })
  }, [expired, navigate])
  if (expired) return null

  return (
    <AppShell me={data} connection={status.connection} approvalCount={approvals.data?.length ?? 0}>
      {/* The provider wraps the OUTLET and not the shell, and that boundary is
          the design: chrome is HANDED what it renders (§7), routes READ the
          stream's state from context. A shell that reached for the context
          would have two ways to learn one value. */}
      <StreamStatusProvider value={status}>
        <Outlet />
      </StreamStatusProvider>
    </AppShell>
  )
}

/**
 * Route-level errors: a child loader that failed, or a query that threw
 * because it had no data to fall back on. The FIRST load's 401 is caught by
 * the loader above and redirected there; a background expiry is caught by
 * AppLayout reading the query's `error`, because useSuspenseQuery will not
 * throw over cached data. This handles the rest.
 *
 * The 401 branch is kept here anyway — a child route's own loader can 401
 * without /auth/me having been refetched, and that is a real expiry too.
 *
 * NOTE this path PUSHES a history entry where the loader's redirect replaces
 * one. That is correct here: by the time a boundary renders, the failed route
 * is already the current entry. `_app.test.tsx` asserts the loader's
 * replace-not-push on the first-load path, which is what distinguishes the
 * two mechanisms — they are otherwise visibly identical.
 *
 * The redirect is a navigate-in-effect rather than `throw redirect` because
 * this is a render, not a loader; throwing a redirect from a component is not
 * how the router accepts one.
 */
function AppErrorBoundary({ error }: ErrorComponentProps) {
  const navigate = useNavigate()
  const expired = error instanceof ApiError && error.status === 401
  useEffect(() => {
    if (expired) void navigate({ to: '/login' })
  }, [expired, navigate])
  // Render nothing while the navigation lands; a flash of an error panel on
  // the way to the login form reads as a bug to the person it happens to.
  if (expired) return null
  // Everything else gets the panel — the only surface outside form submission
  // that renders correlation_id. It rethrows anything that is not an ApiError,
  // so a genuine render bug still reaches the root rather than being dressed
  // up as a server error.
  return <ApiErrorPanel error={error} />
}

export function useMe() {
  return useSuspenseQuery(meQueryOptions).data
}
