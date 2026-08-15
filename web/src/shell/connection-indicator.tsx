import type { ConnectionState } from '../realtime/connection-state'

/**
 * The type is IMPORTED, not declared here.
 *
 * The plan's snippet for this file opened with its own
 * `export type ConnectionState = 'live' | 'reconnecting' | 'stopped'`. That is
 * the edge `src/realtime/connection-state.ts` was created to remove: the
 * transport PRODUCES this state and the shell merely DISPLAYS it, so a copy
 * living beside the indicator either duplicates the union (two spellings of one
 * contract, free to drift) or points `realtime/` at the chrome. The arch
 * matrix does not ban that direction — which is exactly why it has to be right
 * by construction. See the comment in connection-state.ts.
 *
 * `live` renders NOTHING. The indicator is not a status light that goes green;
 * it is an admission that appears when the stream is gone. A badge that is
 * always present trains people to stop reading it.
 */
export function ConnectionIndicator(props: { state: ConnectionState }) {
  if (props.state === 'live') return null
  return (
    <p role="status" className="bg-warning/10 text-warning px-3 py-1 text-center font-mono text-xs">
      {props.state === 'reconnecting'
        ? 'reconnecting — this view may be out of date'
        : 'live updates stopped — reload to resume'}
    </p>
  )
}
