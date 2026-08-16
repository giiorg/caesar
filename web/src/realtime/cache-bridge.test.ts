import { QueryClient } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createCacheBridge } from './cache-bridge'
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

/**
 * A CONTROLLABLE `setTimeout`, and it is the only seam these tests use.
 *
 * The plan gave the bridge a `flush()` and had every test call it. MEASURED:
 * that made the whole suite vacuous for the code production actually runs —
 * delete the scheduling arm entirely and all five plan tests still pass, while
 * the shipped bridge never invalidates anything, because nothing in
 * `useAttentionStream` calls `flush`. A seam with no production caller is not a
 * seam, it is a second implementation that only the tests exercise. So `flush`
 * is gone and the window is driven here instead: `run()` is the timer firing,
 * which is exactly what happens in the browser 250ms later.
 */
function fakeTimer() {
  const scheduled: Array<{ fn: () => void; ms: number }> = []
  const impl = ((fn: () => void, ms?: number) => {
    scheduled.push({ fn, ms: ms ?? 0 })
    return 0 as unknown as ReturnType<typeof setTimeout>
  }) as unknown as typeof setTimeout
  return {
    impl,
    get count() {
      return scheduled.length
    },
    delays: () => scheduled.map((s) => s.ms),
    run() {
      // Drain, so a callback that schedules another window is observable
      // rather than re-run.
      const due = scheduled.splice(0, scheduled.length)
      for (const s of due) s.fn()
    },
  }
}

/**
 * Every query key handed to `invalidateQueries`, in call order.
 *
 * A READER over the spy this file already uses, not a second recording
 * mechanism: the bridge now invalidates three different keys per window, and
 * `toHaveBeenCalledTimes(n)` cannot say WHICH. Keeping one spy means the count
 * assertions and the key assertions are reading the same source.
 */
const invalidated = (spy: { mock: { calls: unknown[][] } }): unknown[] =>
  spy.mock.calls.map((c) => (c[0] as { queryKey?: unknown } | undefined)?.queryKey)

afterEach(() => {
  vi.useRealTimers()
})

describe('createCacheBridge', () => {
  it('invalidates the inbox, the list and the run on an attention frame', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const t = fakeTimer()
    const b = createCacheBridge(qc, { debounceMs: 10, setTimeoutImpl: t.impl })
    b.onFrame(frame(1, 'gate.waiting'))
    expect(spy).not.toHaveBeenCalled() // not until the window closes
    t.run()
    expect(invalidated(spy)).toEqual([['approvals'], ['runs'], ['run', 'r1']])
  })

  it('NEVER writes cache data from an event payload', () => {
    // setQueryData from an event payload is wrong under BOTH of the server's
    // stated behaviours: the opening frames are the most recent 500 events,
    // not a projection of open gates, so a gate still waiting from before that
    // window is not on the stream at all; and resume deliberately re-sends a
    // backlog. Invalidation is idempotent under both. Reconstruction is not.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'setQueryData')
    const t = fakeTimer()
    const b = createCacheBridge(qc, { debounceMs: 10, setTimeoutImpl: t.impl })
    b.onFrame(frame(1, 'gate.waiting'))
    t.run()
    expect(spy).not.toHaveBeenCalled()
  })

  it('coalesces a burst into one window and one invalidation per key', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const t = fakeTimer()
    const b = createCacheBridge(qc, { debounceMs: 10, setTimeoutImpl: t.impl })
    for (let i = 0; i < 20; i++) b.onFrame(frame(i, 'run.started'))
    // ONE window, not twenty. Asserting only the invalidation count would not
    // say this: twenty windows all firing produce one invalidation too, because
    // the first clears `pending` and the other nineteen no-op. Then a run
    // emitting twenty events would arm twenty timers, which is the cost this
    // debounce exists to avoid.
    expect(t.count).toBe(1)
    t.run()
    // Three, not twenty-two: the inbox, the list, and the one run these twenty
    // frames all named. MEASURED: with `dirtyRuns` an array rather than a Set,
    // this assertion reports 22 — the window coalesces the timers and not the
    // invalidations, which is half the saving and none of the point.
    expect(spy).toHaveBeenCalledTimes(3)
  })

  it('opens a NEW window for a frame that arrives after the last one closed', () => {
    // The bug this catches is a bridge that goes permanently deaf: leave
    // `scheduled` set after firing and the first burst is the only one that
    // ever reaches the cache. Nothing about the first window's behaviour
    // distinguishes the two, so it takes a second burst to see it.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const t = fakeTimer()
    const b = createCacheBridge(qc, { debounceMs: 10, setTimeoutImpl: t.impl })
    b.onFrame(frame(1, 'gate.waiting'))
    t.run()
    b.onFrame(frame(2, 'gate.decided'))
    expect(t.count).toBe(1) // a second window was armed
    t.run()
    expect(spy).toHaveBeenCalledTimes(6) // two windows, three keys each
  })

  it('is idempotent under a replayed backlog', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const t = fakeTimer()
    const b = createCacheBridge(qc, { debounceMs: 10, setTimeoutImpl: t.impl })
    b.onFrame(frame(5, 'gate.waiting'))
    t.run()
    b.onFrame(frame(5, 'gate.waiting')) // the same event, re-delivered on resume
    t.run()
    expect(spy).toHaveBeenCalledTimes(6) // two windows of three, one truth
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
    expect(spy).toHaveBeenCalledTimes(3)
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
    const t = fakeTimer()
    const b = createCacheBridge(qc, { debounceMs: 10, setTimeoutImpl: t.impl })
    b.onFrame({ ...frame(1, 'gate.waiting'), event: '' })
    t.run()
    // The INBOX by name, because the inbox is the only key the kind decides.
    // MEASURED: with `f.event` in place of `f.frame.kind`, this window carries
    // exactly `['runs']` and `['run','r1']` — the run and the list are routed
    // from `run_id` and would go out under either reading.
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

describe('routing a frame to its run', () => {
  it('routes an INTERMEDIATE phase transition to its run and the list, never the inbox', () => {
    // phase.started, deliberately. Gate arrival and run completion are in
    // ATTENTION_KINDS already, so a routing test written on either would pass
    // with phase routing entirely absent — which is the hole this file's house
    // rule exists for. MEASURED: re-gating `dirtyRuns.add` on ATTENTION_KINDS
    // fails the three tests in THIS block and none of the eight above, every
    // one of which is written on an attention kind.
    //
    // This test replaces `ignores kinds outside the attention set`, whose
    // assertion was `t.count === 0`: an ignored kind must not even ARM a
    // window. That guard is retired deliberately — routing by `run_id` means
    // reading every frame, so every frame arms one. What replaces it is the
    // last assertion: an ignored kind reaches the run and the list, and
    // nothing else. `['approvals']` is absent from an exact `toEqual`, which
    // is the same protection stated as an outcome rather than as a timer count.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const t = fakeTimer()
    const b = createCacheBridge(qc, { debounceMs: 10, setTimeoutImpl: t.impl })
    b.onFrame(frame(1, 'phase.started'))
    expect(invalidated(spy)).toEqual([]) // nothing leaves before the window closes
    expect(t.count).toBe(1)
    t.run()
    expect(invalidated(spy)).toEqual([['runs'], ['run', 'r1']])
  })

  it('invalidates only the runs the window itself saw', () => {
    // TWO windows and TWO DIFFERENT RUNS, and both halves are load-bearing.
    // MEASURED: delete `dirtyRuns.clear()` and r1 comes out again in the
    // second window here while every other test in this file stays green — the
    // only other two-window tests replay the SAME run, so a set that is never
    // cleared hands them exactly the keys they already expect.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const t = fakeTimer()
    const b = createCacheBridge(qc, { debounceMs: 10, setTimeoutImpl: t.impl })
    b.onFrame(frame(1, 'phase.started', 'r1'))
    t.run()
    expect(invalidated(spy)).toEqual([['runs'], ['run', 'r1']])
    b.onFrame(frame(2, 'phase.started', 'r2'))
    t.run()
    expect(invalidated(spy)).toEqual([
      ['runs'],
      ['run', 'r1'],
      ['runs'],
      ['run', 'r2'], // r1 is not here: the closed window took its runs with it
    ])
  })

  it('coalesces a burst across runs into ONE window and loses neither run', () => {
    // The debounce is load-bearing now rather than an optimization: a run in
    // flight emits far more phase transitions than gate events. What must not
    // happen is the window keeping one run and dropping the rest — MEASURED: a
    // `let lastRun` in place of the Set fails this test alone and passes the
    // other eleven, which is what makes the failure invisible under anything
    // but concurrency.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const t = fakeTimer()
    const b = createCacheBridge(qc, { debounceMs: 10, setTimeoutImpl: t.impl })
    b.onFrame(frame(1, 'phase.started', 'r1'))
    b.onFrame(frame(2, 'phase.started', 'r2'))
    b.onFrame(frame(3, 'phase.succeeded', 'r1'))
    expect(t.count).toBe(1)
    t.run()
    expect(invalidated(spy)).toContainEqual(['run', 'r1'])
    expect(invalidated(spy)).toContainEqual(['run', 'r2'])
  })

  it('drops a frame whose run_id is unusable rather than routing it', () => {
    // The contract types `run_id` as a string, so this is not defensive
    // padding about a case the server produces — it is about what the bridge
    // does with a frame it cannot route. A `not.toThrow()` would prove nothing
    // here — MEASURED: with the guard deleted, `dirtyRuns.add(undefined)` is
    // legal and react-query hashes the key happily, so the window fires and
    // emits `['runs']`, `['run', undefined]` and `['run', '']` without an
    // error anywhere. The exact `toEqual([])` is what sees it. Both halves of
    // the guard are separately proved: dropping only `runId === ''` leaves
    // `['run', '']` behind.
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const t = fakeTimer()
    const b = createCacheBridge(qc, { debounceMs: 10, setTimeoutImpl: t.impl })
    const missing = frame(1, 'phase.started')
    // @ts-expect-error deliberately violating the contract
    missing.frame.run_id = undefined
    b.onFrame(missing)
    b.onFrame(frame(2, 'phase.started', ''))
    t.run()
    expect(invalidated(spy)).toEqual([])
  })
})
