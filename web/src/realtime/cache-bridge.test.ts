import { QueryClient } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { createCacheBridge } from './cache-bridge'
import type { EventFrame } from './sse-connection'

const frame = (id: number, kind: string): EventFrame => ({
  kind: 'event',
  id,
  event: kind,
  frame: { id, run_id: 'r1', kind, payload: { node_id: 'gate' }, occurred_at: '' },
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

afterEach(() => {
  vi.useRealTimers()
})

describe('createCacheBridge', () => {
  it('invalidates the approvals query on an attention frame', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const t = fakeTimer()
    const b = createCacheBridge(qc, { debounceMs: 10, setTimeoutImpl: t.impl })
    b.onFrame(frame(1, 'gate.waiting'))
    expect(spy).not.toHaveBeenCalled() // not until the window closes
    t.run()
    expect(spy).toHaveBeenCalledWith({ queryKey: ['approvals'] })
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

  it('coalesces a burst into one window and one refetch', () => {
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
    expect(spy).toHaveBeenCalledTimes(1)
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
    expect(spy).toHaveBeenCalledTimes(2)
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
    expect(spy).toHaveBeenCalledTimes(2) // two invalidations, one truth
  })

  it('ignores kinds outside the attention set', () => {
    const qc = new QueryClient()
    const spy = vi.spyOn(qc, 'invalidateQueries')
    const t = fakeTimer()
    const b = createCacheBridge(qc, { debounceMs: 10, setTimeoutImpl: t.impl })
    b.onFrame(frame(1, 'phase.materialized'))
    // No timer either: an ignored kind must not even arm a window, or a run
    // emitting nothing but phase.* traffic pays for a refetch every 250ms.
    expect(t.count).toBe(0)
    t.run()
    expect(spy).not.toHaveBeenCalled()
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
    expect(spy).toHaveBeenCalledTimes(1)
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
    expect(spy).toHaveBeenCalledTimes(1)
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
