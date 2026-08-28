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
 * IT IS ALSO NOT THE TIER PREDICATE, and that is the mistake this paragraph
 * exists to prevent. Task 12 splits the window into an immediate tier and a
 * throttled one, and the question there is "did this window see any non-`phase.*`
 * kind?" — which this set cannot answer, because it omits `run.created` and
 * `run.superseded`, both of which move a collection and neither of which
 * belongs in the inbox. Two different questions, deliberately two predicates.
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
 * The phase tier's trailing-refresh throttle. 5s = the client-wide `staleTime`
 * (`api/query-client.ts`): the throttled tier then never serves data older than
 * the staleness the design already accepts, and phase dots advance well inside
 * the locked "live dots" bound (decisions §7) against the 60s refresh floor.
 * Asserted by value in the tests, because a constant nobody asserts is a
 * constant anybody can retune to 60s without a red.
 */
export const PHASE_TIER_THROTTLE_MS = 5_000

/**
 * Typed frame → query invalidation. No network, no writes.
 *
 * THE BRIDGE INVALIDATES; IT NEVER RECONSTRUCTS. This is not a fallback for a
 * missing snapshot discriminator — it is the only correct reading of what the
 * server sends. A frame invalidates the run it names; a frame in the attention
 * set also invalidates the approvals query, and the badge follows from the
 * refetched list.
 *
 * TWO TIERS, AND THE PREDICATE IS `phase.*` (Task 12). What every window used
 * to fire unconditionally was the fan: inbox, runs list, task list and the run,
 * on every single frame. A run in flight emits far more phase transitions than
 * anything else, so the fan made a chatty run a drumbeat on four queries at
 * once. Now:
 *
 *   - the RUN DTO stays per-window, and goes out EXACT. Exact because the
 *     events log is keyed `['run', id, 'events']` and used to ride this prefix;
 *     it now states its tier membership instead, so a phase stream costs one
 *     run-DTO refetch per window and NOTHING else by side effect.
 *   - a window that saw any NON-`phase.*` kind refetches the collections and the
 *     event logs NOW. Terminal events stay instant, which is the half of the
 *     old behaviour worth keeping.
 *   - a PHASE-ONLY window schedules one trailing refresh per
 *     PHASE_TIER_THROTTLE_MS. Deferring those collections to the 60s floor
 *     instead would be a regression dressed as tuning: RunCard and the Tasks
 *     rows draw PhaseDots off `phase_summary`, so the collections render phase
 *     progress and Home's live dots are locked design.
 *
 * THE DEBOUNCE TIMER IS THE ONLY WAY ANYTHING LEAVES THIS MODULE. The plan
 * also returned a `flush()` and every one of its tests called it; nothing in
 * `use-attention-stream.ts` ever would, so the suite proved a path production
 * does not run while the path it does run stayed unasserted — deleting the
 * scheduling arm left all five plan tests green and the shipped bridge inert.
 * `setTimeoutImpl` is the seam instead: a test that wants the window closed
 * closes it by firing the timer, which is what the browser does. The throttle
 * tier rides THE SAME option — one seam, two uses — for the same reason.
 *
 * No `close()`, deliberately. A pending window that outlives its stream fires
 * into a query with no observers, which marks it stale and fetches nothing —
 * so cancellation would buy one untestable branch and no behaviour.
 */
export function createCacheBridge(
  qc: QueryClient,
  opts?: { debounceMs?: number; throttleMs?: number; setTimeoutImpl?: typeof setTimeout },
) {
  const debounceMs = opts?.debounceMs ?? DEFAULT_DEBOUNCE_MS
  const throttleMs = opts?.throttleMs ?? PHASE_TIER_THROTTLE_MS
  const later = opts?.setTimeoutImpl ?? setTimeout
  let scheduled = false
  let inboxDirty = false
  /**
   * Did the open window see any non-`phase.*` kind? That — and NOT
   * `ATTENTION_KINDS`, which omits `run.created` and `run.superseded` and whose
   * own doc scopes it to the inbox — is the collection-immediate predicate.
   */
  let lifecycleDirty = false
  let throttlePending = false
  /**
   * The runs named by frames in the open window. A SET, not the last id: runs
   * in flight interleave, and keeping one would drop the others silently and
   * only under concurrency. A Set rather than an array for the same reason the
   * window exists — twenty phase transitions on one run are one invalidation.
   */
  const dirtyRuns = new Set<string>()
  /** Runs whose phase frames arrived during a throttle hold; refreshed when it fires. */
  const throttledRuns = new Set<string>()

  function fire() {
    // Reset BEFORE the invalidation, so the next frame arms a new window. Left
    // set, the bridge goes permanently deaf after its first burst while every
    // single-burst test still passes. There are two of these flags now, and
    // both are proved by the same shape of test.
    scheduled = false
    const runs = [...dirtyRuns]
    const inbox = inboxDirty
    const lifecycle = lifecycleDirty
    dirtyRuns.clear()
    inboxDirty = false
    lifecycleDirty = false

    // OUTSIDE the tier branch, not inside it. Today `inbox` implies `lifecycle`
    // — no member of ATTENTION_KINDS starts with `phase.` — so the two spellings
    // are indistinguishable by any test. They stop being indistinguishable the
    // day a phase kind joins the attention set, and then the nested spelling
    // would drop the inbox refresh on the floor entirely: the throttled tier
    // below does not carry `['approvals']`, and nothing else would. The inbox
    // is guarded by its own predicate at its own level.
    if (inbox) void qc.invalidateQueries({ queryKey: ['approvals'] })

    // EXACT: see the tier paragraph above. The events log states its membership
    // rather than riding this prefix.
    for (const runId of runs) void qc.invalidateQueries({ queryKey: ['run', runId], exact: true })

    if (lifecycle) {
      // The LISTS, prefix-matched across every filter variant: `['runs', {...}]`
      // keys all start with `'runs'`, and a run that changed could enter or
      // leave any of them. This is also why the list query key puts its filters
      // in a second element rather than in the string.
      void qc.invalidateQueries({ queryKey: ['runs'] })
      // THE TASK LIST, for the same reason and one more. `TaskResponse.Status`
      // IS the latest run's status (runs/api/dto.go:22-23), so a frame that
      // moves a run moves the TASK between the Tasks screen's four attention
      // groups — and a task's group is the whole design of that screen.
      void qc.invalidateQueries({ queryKey: ['tasks'] })
      // A pending throttle hold is ABSORBED here rather than left to fire: the
      // runs it was holding are refreshed now, by this window. The union is
      // what makes the absorption cost nothing — invalidating one events key
      // twice in a row is two HTTP refetches, because react-query's
      // `cancelRefetch` default aborts the first and issues a second.
      for (const runId of new Set([...runs, ...throttledRuns]))
        void qc.invalidateQueries({ queryKey: ['run', runId, 'events'] })
      throttledRuns.clear()
      return
    }

    for (const runId of runs) throttledRuns.add(runId)
    if (throttlePending) return
    throttlePending = true
    later(() => {
      // BEFORE the invalidations — the same deafness guard as `scheduled`, and
      // the reason there are two tests for it rather than one.
      throttlePending = false
      const held = [...throttledRuns]
      throttledRuns.clear()
      // ABSORBED: a lifecycle window that fired while this timer was in flight
      // already refetched the collections and drained `throttledRuns`, and the
      // timer cannot be cancelled through the `setTimeoutImpl` seam. Returning
      // on an empty hold is what makes absorption total — without it the
      // orphaned timer lands a second, pointless `['runs']` + `['tasks']`
      // refetch a few seconds after the lifecycle event, which is precisely the
      // drumbeat this tier exists to remove. The guard is on the HELD SET
      // rather than on a cancellation flag because the set is the honest
      // question: "is there anything left to refresh?"
      if (held.length === 0) return
      void qc.invalidateQueries({ queryKey: ['runs'] })
      void qc.invalidateQueries({ queryKey: ['tasks'] })
      for (const runId of held) void qc.invalidateQueries({ queryKey: ['run', runId, 'events'] })
    }, throttleMs)
  }

  return {
    onFrame(f: EventFrame): void {
      const runId = f.frame.run_id
      // A frame that cannot be routed is dropped whole, rather than routed to
      // `['run', undefined]`: the key would hash fine and invalidate a query
      // nothing observes, so the mistake would never surface.
      if (typeof runId !== 'string' || runId === '') return
      dirtyRuns.add(runId)
      if (!f.frame.kind.startsWith('phase.')) lifecycleDirty = true
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
