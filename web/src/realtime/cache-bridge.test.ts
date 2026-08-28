import { QueryClient } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createCacheBridge, PHASE_TIER_THROTTLE_MS } from './cache-bridge'
import { fakeTimer } from './fake-timer.testkit'
import type { EventFrame } from './sse-connection'

/**
 * A frame as the wire really shapes it. `run_id` defaults to `r1` so the
 * pre-existing single-run tests read as they always did; the routing block
 * passes it.
 */
const frame = (id: number, kind: string, runId = 'r1'): EventFrame => ({
  kind: 'event',
  id,
  event: kind,
  frame: { id, run_id: runId, kind, payload: { node_id: 'gate' }, occurred_at: '' },
})

/** The debounce every test below arms its windows with. */
const WINDOW = 10
/** And the throttle, kept distinct from WINDOW so `runOnly` can tell them apart. */
const HOLD = 1_000

/**
 * Every query key handed to `invalidateQueries`, in call order.
 *
 * A READER over the spy this file already uses, not a second recording
 * mechanism: the bridge now invalidates several different keys per window, and
 * `toHaveBeenCalledTimes(n)` cannot say WHICH. Keeping one spy means the count
 * assertions and the key assertions are reading the same source.
 */
const invalidated = (spy: { mock: { calls: unknown[][] } }): unknown[] =>
  spy.mock.calls.map((c) => (c[0] as { queryKey?: unknown } | undefined)?.queryKey)

/**
 * The WHOLE filter object, for the one assertion that is about a field other
 * than the key.
 *
 * `exact: true` on the run DTO is the load-bearing half of the tier split — it
 * is what stops `['run', id, 'events']` riding the prefix — and `invalidated`
 * above cannot see it: drop the flag and every key assertion in this file stays
 * green. So the run key is asserted through this reader instead, and the flag
 * is falsifiable where it is written rather than only three files away in the
 * HTTP-counting suite.
 */
const filters = (spy: { mock: { calls: unknown[][] } }): unknown[] =>
  spy.mock.calls.map((c) => c[0])

afterEach(() => {
  vi.useRealTimers()
})

/** A bridge with both timers injected and both delays distinguishable. */
function bridgeOn(qc: QueryClient) {
  const t = fakeTimer()
  return {
    t,
    b: createCacheBridge(qc, { debounceMs: WINDOW, throttleMs: HOLD, setTimeoutImpl: t.impl }),
  }
}

describe('createCacheBridge', () => {
  it('invalidates the inbox, the collections, the run and its log on a LIFECYCLE frame', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const { t, b } = bridgeOn(qc)
    b.onFrame(frame(1, 'gate.waiting'))
    expect(spy).not.toHaveBeenCalled() // not until the window closes
    t.runOnly(WINDOW)
    expect(invalidated(spy)).toEqual([
      ['approvals'],
      ['run', 'r1'],
      ['runs'],
      ['tasks'],
      ['run', 'r1', 'events'],
    ])
    // The whole fan, immediately — that half of the old behaviour is kept on
    // purpose, because a terminal event must not wait out a throttle.
    expect(t.count).toBe(0) // and it armed no trailing refresh
  })

  it('invalidates the run DTO EXACTLY, so the event log does not ride its prefix', () => {
    // The flag no key assertion in this file can see. `['run', id, 'events']`
    // starts with `['run', id]`, and react-query matches invalidation filters by
    // prefix unless `exact` is set — so without this the log would refetch on
    // every phase window, which is the single largest term in the old fan.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const { t, b } = bridgeOn(qc)
    b.onFrame(frame(1, 'phase.started'))
    t.runOnly(WINDOW)
    expect(filters(spy)).toEqual([{ queryKey: ['run', 'r1'], exact: true }])
  })

  it('NEVER writes cache data from an event payload', () => {
    // setQueryData from an event payload is wrong under BOTH of the server's
    // stated behaviours: the opening frames are the most recent 500 events,
    // not a projection of open gates, so a gate still waiting from before that
    // window is not on the stream at all; and resume deliberately re-sends a
    // backlog. Invalidation is idempotent under both. Reconstruction is not.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'setQueryData')
    const { t, b } = bridgeOn(qc)
    b.onFrame(frame(1, 'gate.waiting'))
    t.run()
    expect(spy).not.toHaveBeenCalled()
  })

  it('coalesces a burst into one window and one invalidation per key', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const { t, b } = bridgeOn(qc)
    for (let i = 0; i < 20; i++) b.onFrame(frame(i, 'run.started'))
    // ONE window, not twenty. Asserting only the invalidation count would not
    // say this: twenty windows all firing produce one invalidation too, because
    // the first clears `pending` and the other nineteen no-op. Then a run
    // emitting twenty events would arm twenty timers, which is the cost this
    // debounce exists to avoid.
    expect(t.count).toBe(1)
    t.runOnly(WINDOW)
    // Five, not twenty-four: the inbox, the run, the run list, the task list,
    // and the one event log these twenty frames all named. MEASURED: with
    // `dirtyRuns` an array rather than a Set, this assertion reports 24 — the
    // window coalesces the timers and not the invalidations, which is half the
    // saving and none of the point.
    expect(spy).toHaveBeenCalledTimes(5)
  })

  it('opens a NEW window for a frame that arrives after the last one closed', () => {
    // The bug this catches is a bridge that goes permanently deaf: leave
    // `scheduled` set after firing and the first burst is the only one that
    // ever reaches the cache. Nothing about the first window's behaviour
    // distinguishes the two, so it takes a second burst to see it.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const { t, b } = bridgeOn(qc)
    b.onFrame(frame(1, 'gate.waiting'))
    t.runOnly(WINDOW)
    b.onFrame(frame(2, 'gate.decided'))
    expect(t.count).toBe(1) // a second window was armed
    t.runOnly(WINDOW)
    expect(spy).toHaveBeenCalledTimes(10) // two windows, five keys each
  })

  it('is idempotent under a replayed backlog', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const { t, b } = bridgeOn(qc)
    b.onFrame(frame(5, 'gate.waiting'))
    t.runOnly(WINDOW)
    b.onFrame(frame(5, 'gate.waiting')) // the same event, re-delivered on resume
    t.runOnly(WINDOW)
    expect(spy).toHaveBeenCalledTimes(10) // two windows of five, one truth
  })

  it('debounces on real timers with no injection at all', () => {
    // The defaults are what ships. Every seam in `sse-connection.ts` is
    // injected by every one of its tests, so the browser is the first thing to
    // execute any of them; that is a known hole in this tree and it is cheap
    // not to repeat it here. This is also the only test that can tell a default
    // of 250ms from a default of 0 or undefined.
    vi.useFakeTimers()
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const b = createCacheBridge(qc)
    b.onFrame(frame(1, 'gate.waiting'))
    vi.advanceTimersByTime(249)
    expect(spy).not.toHaveBeenCalled()
    vi.advanceTimersByTime(1)
    expect(spy).toHaveBeenCalledTimes(5)
  })

  it('reads the kind from the frame BODY, not from the SSE event name', () => {
    // On the wire the two always agree — the server writes
    // `event: %s` from the same string it puts in the payload
    // (backend/internal/realtime/sse.go) — which is exactly why a suite whose
    // fixtures carry both can never tell which one the bridge consulted.
    // MEASURED: with matching fixtures, swapping `f.frame.kind` for `f.event`
    // survived every other test in this file.
    //
    // The body wins because the body is the record: `run_events.kind` is what
    // the backend stored and what `GET /approvals` is a projection of, while
    // `event:` is a routing label for EventSource-shaped consumers this client
    // deliberately is not (see sse-connection.ts). The parser also does not
    // require the field at all — it defaults to `''` — so a frame that lost its
    // event line must still move the inbox.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const { t, b } = bridgeOn(qc)
    b.onFrame({ ...frame(1, 'gate.waiting'), event: '' })
    t.runOnly(WINDOW)
    // The INBOX by name, because the inbox is the only key the kind decides
    // alone. MEASURED: with `f.event` in place of `f.frame.kind`, this window
    // carries exactly `['run','r1']` and a trailing refresh — an empty `event`
    // is not `phase.*` either, so the TIER reads the same both ways and only
    // the inbox separates them.
    expect(invalidated(spy)).toContainEqual(['approvals'])
  })

  it('waits the FULL window before firing, so a burst is one refetch and not two', () => {
    const qc = new QueryClient()
    const t = fakeTimer()
    const b = createCacheBridge(qc, { debounceMs: 250, setTimeoutImpl: t.impl })
    b.onFrame(frame(1, 'gate.waiting'))
    // Pins the delay to the value asked for. A bridge that scheduled `0` would
    // pass every other test here and coalesce nothing at all in a browser,
    // where frames arrive on separate task ticks rather than in one loop.
    expect(t.delays()).toEqual([250])
  })
})

describe('the phase tier', () => {
  it('costs a phase-only window the run DTO and NOTHING else, until the throttle fires', () => {
    // (a). The whole task in one test: what a chatty run costs per window, and
    // what it costs per throttle interval instead.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const { t, b } = bridgeOn(qc)
    b.onFrame(frame(1, 'phase.started'))
    t.runOnly(WINDOW)
    expect(invalidated(spy)).toEqual([['run', 'r1']])
    t.runOnly(HOLD)
    expect(invalidated(spy)).toEqual([['run', 'r1'], ['runs'], ['tasks'], ['run', 'r1', 'events']])
  })

  it('collapses TEN phase windows into ONE trailing refresh', () => {
    // (b). The drumbeat collapse, asserted by count rather than by keys: ten
    // windows, ten run-DTO refreshes (the tier that stays per-window), and ONE
    // set of collections. MEASURED against the old bridge: 40.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const { t, b } = bridgeOn(qc)
    for (let w = 0; w < 10; w++) {
      for (let i = 0; i < 4; i++) b.onFrame(frame(w * 4 + i, 'phase.started'))
      t.runOnly(WINDOW)
    }
    expect(spy).toHaveBeenCalledTimes(10) // one exact run DTO per window
    expect(t.count).toBe(1) // ONE throttle timer armed across all ten
    t.runOnly(HOLD)
    expect(invalidated(spy).slice(10)).toEqual([['runs'], ['tasks'], ['run', 'r1', 'events']])
  })

  it('sends a NON-phase kind outside the attention set straight to the collections', () => {
    // (c). `run.superseded` is the kind that separates the two predicates: it is
    // NOT in ATTENTION_KINDS — a superseded run does not move the inbox — but it
    // moves both collections, so it must not be throttled. MEASURED: gating the
    // tier on `ATTENTION_KINDS.has(kind)` instead of `!kind.startsWith('phase.')`
    // fails here and nowhere else in this file, because every other lifecycle
    // fixture is an attention kind.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const { t, b } = bridgeOn(qc)
    b.onFrame(frame(1, 'run.superseded'))
    t.runOnly(WINDOW)
    expect(invalidated(spy)).toEqual([['run', 'r1'], ['runs'], ['tasks'], ['run', 'r1', 'events']])
    expect(invalidated(spy)).not.toContainEqual(['approvals']) // still inbox-guarded
    expect(t.count).toBe(0) // and nothing was deferred
  })

  it('ABSORBS a pending hold into a lifecycle window, and the orphaned timer says nothing', () => {
    // (d). The timer cannot be cancelled through the `setTimeoutImpl` seam, so
    // the empty-hold return is the only thing standing between a lifecycle event
    // and a spurious collection refetch five seconds later. The assertion is on
    // the TOTAL call count across firing it, not on any one key: a guard that
    // merely skipped the events key would still land `['runs']` + `['tasks']`.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const { t, b } = bridgeOn(qc)
    b.onFrame(frame(1, 'phase.started'))
    t.runOnly(WINDOW)
    b.onFrame(frame(2, 'run.succeeded'))
    t.runOnly(WINDOW)
    const settled = spy.mock.calls.length
    // The log is invalidated ONCE by that window even though r1 is both its own
    // dirty run and the held one — the union in `fire()`. Without it react-query
    // aborts the first refetch and issues a second, which is a real extra
    // request and not a duplicate no-op.
    expect(invalidated(spy).filter((k) => JSON.stringify(k).includes('events'))).toHaveLength(1)
    t.runOnly(HOLD) // the orphan
    expect(spy).toHaveBeenCalledTimes(settled)
  })

  it('absorbs without going deaf: a phase frame after the absorption still gets its refresh', () => {
    // (d2). The other side of (d). A guard that absorbed by latching a flag
    // would swallow the NEXT run's trailing refresh too; a guard on the held set
    // cannot, because a new frame refills the set.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const { t, b } = bridgeOn(qc)
    b.onFrame(frame(1, 'phase.started'))
    t.runOnly(WINDOW) // arms the hold
    b.onFrame(frame(2, 'run.succeeded'))
    t.runOnly(WINDOW) // absorbs it
    b.onFrame(frame(3, 'phase.started', 'r2')) // a NEW run, after the absorption
    t.runOnly(WINDOW)
    const before = spy.mock.calls.length
    t.runOnly(HOLD) // whichever timer is pending, the held run is refreshed exactly once
    expect(invalidated(spy).slice(before)).toEqual([['runs'], ['tasks'], ['run', 'r2', 'events']])
  })

  it('arms the trailing refresh at PHASE_TIER_THROTTLE_MS, which is 5000', () => {
    // (e). The constant by VALUE, because the argument for 5s is external to the
    // code — it is the client-wide staleTime — and nothing else in the tree would
    // notice it being retuned to the 60s refresh floor, which is the change that
    // would quietly turn Home's locked live dots into a minute-stale list.
    expect(PHASE_TIER_THROTTLE_MS).toBe(5_000)
    const qc = new QueryClient()
    const t = fakeTimer()
    const b = createCacheBridge(qc, { debounceMs: WINDOW, setTimeoutImpl: t.impl })
    b.onFrame(frame(1, 'phase.started'))
    t.runOnly(WINDOW)
    expect(t.delays()).toEqual([PHASE_TIER_THROTTLE_MS])
  })

  it('goes deaf on NEITHER flag: a window and a hold both re-arm after firing', () => {
    // (f). Two flags now, `scheduled` and `throttlePending`, and each is reset
    // before its own invalidations. A single burst cannot tell either mistake
    // from correct behaviour, so both need a second round to see.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const { t, b } = bridgeOn(qc)
    b.onFrame(frame(1, 'phase.started'))
    t.runOnly(WINDOW)
    t.runOnly(HOLD)
    const before = spy.mock.calls.length
    b.onFrame(frame(2, 'phase.started'))
    expect(t.count).toBe(1) // `scheduled` re-armed
    t.runOnly(WINDOW)
    expect(t.count).toBe(1) // `throttlePending` re-armed
    t.runOnly(HOLD)
    expect(invalidated(spy).slice(before)).toEqual([
      ['run', 'r1'],
      ['runs'],
      ['tasks'],
      ['run', 'r1', 'events'],
    ])
  })
})

describe('routing a frame to its run', () => {
  it('routes an INTERMEDIATE phase transition to its run, never the inbox', () => {
    // phase.started, deliberately. Gate arrival and run completion are in
    // ATTENTION_KINDS already, so a routing test written on either would pass
    // with phase routing entirely absent — which is the hole this file's house
    // rule exists for. MEASURED: re-gating `dirtyRuns.add` on ATTENTION_KINDS
    // fails three of the four tests in THIS block (the run_id guard below
    // survives it) and none of the ones above, every one of which is written
    // on a lifecycle kind.
    //
    // This test replaces `ignores kinds outside the attention set`, whose
    // assertion was `t.count === 0`: an ignored kind must not even ARM a
    // window. That guard is retired deliberately — routing by `run_id` means
    // reading every frame, so every frame arms one. What replaces it is the
    // last assertion: an ignored kind reaches the run, and nothing else.
    // `['approvals']` is absent from an exact `toEqual`, which is the same
    // protection stated as an outcome rather than as a timer count.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const { t, b } = bridgeOn(qc)
    b.onFrame(frame(1, 'phase.started'))
    expect(invalidated(spy)).toEqual([]) // nothing leaves before the window closes
    expect(t.count).toBe(1)
    t.runOnly(WINDOW)
    expect(invalidated(spy)).toEqual([['run', 'r1']])
  })

  it('invalidates only the runs the window itself saw', () => {
    // TWO windows and TWO DIFFERENT RUNS, and both halves are load-bearing.
    // MEASURED: delete `dirtyRuns.clear()` and r1 comes out again in the
    // second window here while every other test in this file stays green — the
    // only other two-window tests replay the SAME run, so a set that is never
    // cleared hands them exactly the keys they already expect.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const { t, b } = bridgeOn(qc)
    b.onFrame(frame(1, 'phase.started', 'r1'))
    t.runOnly(WINDOW)
    expect(invalidated(spy)).toEqual([['run', 'r1']])
    b.onFrame(frame(2, 'phase.started', 'r2'))
    t.runOnly(WINDOW)
    expect(invalidated(spy)).toEqual([
      ['run', 'r1'],
      ['run', 'r2'], // r1 is not here: the closed window took its runs with it
    ])
  })

  it('coalesces a burst across runs into ONE window and loses neither run', () => {
    // The debounce is load-bearing now rather than an optimization: a run in
    // flight emits far more phase transitions than gate events. What must not
    // happen is the window keeping one run and dropping the rest — MEASURED: a
    // `let lastRun` in place of the Set fails this test alone and passes the
    // others, which is what makes the failure invisible under anything but
    // concurrency.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const { t, b } = bridgeOn(qc)
    b.onFrame(frame(1, 'phase.started', 'r1'))
    b.onFrame(frame(2, 'phase.started', 'r2'))
    b.onFrame(frame(3, 'phase.succeeded', 'r1'))
    expect(t.count).toBe(1)
    t.runOnly(WINDOW)
    expect(invalidated(spy)).toContainEqual(['run', 'r1'])
    expect(invalidated(spy)).toContainEqual(['run', 'r2'])
    // And the hold carries BOTH into the trailing refresh, not just the last.
    t.runOnly(HOLD)
    expect(invalidated(spy)).toContainEqual(['run', 'r1', 'events'])
    expect(invalidated(spy)).toContainEqual(['run', 'r2', 'events'])
  })

  it('drops a frame whose run_id is unusable rather than routing it', () => {
    // The contract types `run_id` as a string, so this is not defensive
    // padding about a case the server produces — it is about what the bridge
    // does with a frame it cannot route. A `not.toThrow()` would prove nothing
    // here — MEASURED: with the guard deleted, `dirtyRuns.add(undefined)` is
    // legal and react-query hashes the key happily, so the window fires and
    // emits `['run', undefined]` and `['run', '']` without an error anywhere.
    // The exact `toEqual([])` is what sees it. Both halves of the guard are
    // separately proved: dropping only `runId === ''` leaves `['run', '']`
    // behind.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const { t, b } = bridgeOn(qc)
    const missing = frame(1, 'phase.started')
    // @ts-expect-error deliberately violating the contract
    missing.frame.run_id = undefined
    b.onFrame(missing)
    b.onFrame(frame(2, 'phase.started', ''))
    t.run()
    expect(invalidated(spy)).toEqual([])
  })
})
