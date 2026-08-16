import type { QueryClient } from '@tanstack/react-query'
import type { EventFrame } from './sse-connection'

/**
 * The kinds that move the approvals INBOX, and nothing else. Mirrors
 * backend/internal/runs/events.go — every string below is a constant there
 * (`EventGateWaiting`, `EventGateDecided`, `EventRunStarted`,
 * `EventRunSucceeded`, `EventRunFailed`, `EventRunDead`), checked rather than
 * remembered.
 *
 * IT NO LONGER GOVERNS WHAT THE BRIDGE READS. Since Plan 12a every frame is
 * routed to its run and to the runs list (see `onFrame`), because every frame
 * belongs to a run and any frame may change what the list shows. This set
 * decides one thing: whether the window also invalidates `['approvals']`.
 *
 * A kind added to the server and not here costs a REFRESH of the inbox, never
 * correctness: the bounded refresh floor in `api/query-client.ts` still
 * catches it within the stated interval, which is the same guarantee that
 * covers a silent grant narrowing. The reverse mistake is the expensive one —
 * a kind here that the server emits in bulk would turn a chatty run into a
 * refetch loop on the one query rendered on every page. `phase.*` reached the
 * attention stream in Plan 12a and is deliberately absent for that reason: the
 * inbox does not change when a phase ticks.
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
 * server sends. A frame invalidates the run it names and the runs list; a
 * frame in the attention set also invalidates the approvals query, and the
 * badge follows from the refetched list.
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
  let inboxDirty = false
  /**
   * The runs named by frames in the open window. A SET, not the last id: runs
   * in flight interleave, and keeping one would drop the others silently and
   * only under concurrency. A Set rather than an array for the same reason the
   * window exists — twenty phase transitions on one run are one invalidation.
   */
  const dirtyRuns = new Set<string>()

  function fire() {
    // Reset BEFORE the invalidation, so the next frame arms a new window. Left
    // set, the bridge goes permanently deaf after its first burst while every
    // single-burst test still passes.
    scheduled = false
    const runs = [...dirtyRuns]
    const inbox = inboxDirty
    dirtyRuns.clear()
    inboxDirty = false

    if (inbox) void qc.invalidateQueries({ queryKey: ['approvals'] })
    // The LIST, unconditionally and prefix-matched across every filter
    // variant: `['runs', {...}]` keys all start with `'runs'`, and a run that
    // changed could enter or leave any of them. Unconditional because every
    // frame belongs to a run and any frame may change the row that run renders
    // as. This is also why the list query key puts its filters in a second
    // element rather than in the string.
    void qc.invalidateQueries({ queryKey: ['runs'] })
    for (const runId of runs) void qc.invalidateQueries({ queryKey: ['run', runId] })
  }

  return {
    onFrame(f: EventFrame): void {
      const runId = f.frame.run_id
      // A frame that cannot be routed is dropped whole, rather than routed to
      // `['run', undefined]`: the key would hash fine and invalidate a query
      // nothing observes, so the mistake would never surface.
      if (typeof runId !== 'string' || runId === '') return
      dirtyRuns.add(runId)
      if (ATTENTION_KINDS.has(f.frame.kind)) inboxDirty = true
      // A run emitting twenty events costs one refetch per key. The guard is on
      // the window, not on a `pending` flag: nothing arms a window except a
      // frame that named a run, so a window that exists always has something
      // to say.
      if (scheduled) return
      scheduled = true
      later(fire, debounceMs)
    },
  }
}
