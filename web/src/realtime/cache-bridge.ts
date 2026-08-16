import type { QueryClient } from '@tanstack/react-query'
import type { EventFrame } from './sse-connection'

/**
 * The kinds that move the approvals inbox. Mirrors
 * backend/internal/runs/events.go — every string below is a constant there
 * (`EventGateWaiting`, `EventGateDecided`, `EventRunStarted`,
 * `EventRunSucceeded`, `EventRunFailed`, `EventRunDead`), checked rather than
 * remembered.
 *
 * A kind added there and not here costs a REFRESH, never correctness: the
 * bounded refresh floor in `api/query-client.ts` still catches it within the
 * stated interval, which is the same guarantee that covers a silent grant
 * narrowing. The reverse mistake is the expensive one — a kind here that the
 * server emits in bulk would turn a chatty run into a refetch loop — so this
 * set is deliberately the attention vocabulary and not the whole one.
 */
export const ATTENTION_KINDS: ReadonlySet<string> = new Set([
  'gate.waiting',
  'gate.decided',
  'run.started',
  'run.succeeded',
  'run.failed',
  'run.dead',
])

/** How long frames are pooled before one invalidation goes out. */
const DEFAULT_DEBOUNCE_MS = 250

/**
 * Typed frame → query invalidation. No network, no writes.
 *
 * THE BRIDGE INVALIDATES; IT NEVER RECONSTRUCTS. This is not a fallback for a
 * missing snapshot discriminator — it is the only correct reading of what the
 * server sends. A frame in the attention set invalidates the approvals query
 * and the badge follows from the refetched list.
 *
 * THE DEBOUNCE TIMER IS THE ONLY WAY ANYTHING LEAVES THIS MODULE. The plan
 * also returned a `flush()` and every one of its tests called it; nothing in
 * `use-attention-stream.ts` ever would, so the suite proved a path production
 * does not run while the path it does run stayed unasserted — deleting the
 * scheduling arm left all five plan tests green and the shipped bridge inert.
 * `setTimeoutImpl` is the seam instead: a test that wants the window closed
 * closes it by firing the timer, which is what the browser does.
 *
 * No `close()`, deliberately. A pending window that outlives its stream fires
 * into a query with no observers, which marks it stale and fetches nothing —
 * so cancellation would buy one untestable branch and no behaviour.
 */
export function createCacheBridge(
  qc: QueryClient,
  opts?: { debounceMs?: number; setTimeoutImpl?: typeof setTimeout },
) {
  const debounceMs = opts?.debounceMs ?? DEFAULT_DEBOUNCE_MS
  const later = opts?.setTimeoutImpl ?? setTimeout
  let scheduled = false

  function fire() {
    // Reset BEFORE the invalidation, so the next frame arms a new window. Left
    // set, the bridge goes permanently deaf after its first burst while every
    // single-burst test still passes.
    scheduled = false
    void qc.invalidateQueries({ queryKey: ['approvals'] })
  }

  return {
    onFrame(f: EventFrame): void {
      if (!ATTENTION_KINDS.has(f.frame.kind)) return
      // A run emitting twenty events costs one refetch. The guard is on the
      // window, not on a `pending` flag: nothing arms a window except a frame
      // that belongs in the inbox, so a window that exists always has
      // something to say.
      if (scheduled) return
      scheduled = true
      later(fire, debounceMs)
    },
  }
}
