import { ApiError } from '../api/errors'
import { ApiErrorPanel } from './api-error-panel'

/**
 * Whatever went wrong when a MUTATION failed — the narrowing every button needs
 * before it can render `ApiErrorPanel` at all.
 *
 * A mutation's `error` is NOT always an `ApiError`: `unwrap` throws one, but
 * openapi-fetch propagates fetch's own TypeError from a request that never
 * reached the server, and `ApiErrorPanel` deliberately rethrows anything that is
 * not ours. Handing it an unnarrowed error turns a dropped connection into a
 * render-time throw in whichever route is hosting the button.
 *
 * The `null` arm is that request — a dropped connection, a backend restarting
 * mid-click — where there is no status, no problem document and therefore no
 * correlation id to render. The wording is `useAppForm`'s, deliberately: it
 * names no cause, because a transport failure, an unparseable 200 and a bug in
 * the mutation all arrive here and only the first is a connection problem.
 *
 * SHARED rather than copied, because it is a SENTENCE. Two hosts render this
 * (the gate controls and the run header's Retry) and a second copy of the
 * wording is how the two come to disagree about what a dropped connection
 * looks like — with nothing failing when they do.
 */
export function ErrorSurface(props: { failure: ApiError | null }) {
  if (props.failure !== null) return <ApiErrorPanel error={props.failure} />
  return (
    <p role="alert" className="text-danger text-sm">
      the request could not be completed — check your connection and try again
    </p>
  )
}

/** The narrowing above, for a caller that must also single out a status. */
export const asApiError = (error: unknown): ApiError | null =>
  error instanceof ApiError ? error : null
