import { QueryClient } from '@tanstack/react-query'

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // The stream says "something you are showing has changed" and never what
      // it changed to, so REST is the source of truth for list AND badge.
      // Staleness is therefore cheap to resolve and never guessed at.
      staleTime: 5_000,
      retry: (failureCount, error) => {
        // A 4xx will not become a 2xx by asking again, and retrying a 401
        // multiplies the redirect. Unclassified failures surface as permanent,
        // matching the backend's safe direction.
        const status = (error as { status?: number }).status ?? 0
        return status >= 500 && failureCount < 2
      },
    },
  },
})

/**
 * THE BOUNDED REFRESH FLOOR (§3). The stream cannot be the only refresh path,
 * because a NARROWING is silent: losing one grant out of several does not close
 * the stream and emits no event — the server keeps it open, drops frames from
 * the departed project, and its own test asserts that "a narrowing is not a
 * close". Grant deletion writes an audit record and no run_event, so nothing on
 * the wire will ever mention it.
 *
 * A cache refreshed only by attention frames would keep rendering an approval
 * the user can no longer act on, indefinitely, with no bug anywhere in the
 * client. This caps the exposure at a stated number of seconds. The server
 * still rejects every action on that row; what is at stake is the UI telling
 * the truth.
 *
 * Roadmap row 10a makes this relaxable — when the wire can express a scope
 * change, the interval can go.
 */
export const AUTH_SCOPED_QUERY_OPTIONS = {
  refetchOnWindowFocus: true,
  refetchInterval: 60_000,
} as const
