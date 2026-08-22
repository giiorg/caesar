import { ApiError } from '../api/errors'
import { ApiErrorPanel } from './api-error-panel'

/**
 * What a query that finished badly should say, correlation id included.
 *
 * THE NARROWING IS THE WHOLE POINT. `ApiErrorPanel` deliberately rethrows
 * anything that is not ours, and a query's `error` is not always an `ApiError`:
 * `unwrap` throws one, but openapi-fetch propagates fetch's own TypeError from
 * a request that never reached the server. Handing that straight to the panel
 * turns a dropped connection into a render-time throw in whichever surface is
 * hosting the query — which, on a screen that degrades section by section, is
 * exactly the failure mode the degradation exists to avoid.
 *
 * The `fallback` is the host's sentence for the no-status case, because only
 * the host knows what could not be read.
 */
export function QueryFailure(props: { error: unknown; fallback: string }) {
  if (props.error instanceof ApiError) return <ApiErrorPanel error={props.error} />
  return <p className="text-text-muted text-sm">{props.fallback}</p>
}
