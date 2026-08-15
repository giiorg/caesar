import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/errors'
import { openAttentionStream } from './sse-connection'

/**
 * A fetch fake with REAL fetch's abort semantics.
 *
 * The plan's helper handed back a bare `new Response(stream)`, which ignores
 * the AbortSignal entirely — MEASURED: aborting the controller leaves the
 * pending `reader.read()` pending forever, so every abort-driven path
 * (watchdog, close) looked inert and the tests could only ever assert the
 * emitted state, never the teardown it is supposed to cause. Real fetch ERRORS
 * the body stream on abort and the pending read REJECTS; this fake does the
 * same, which is what lets the watchdog test assert that a stale connection is
 * actually torn down and reopened.
 */
type FakeConn = {
  push(s: string): void
  close(): void
  readonly aborted: boolean
}

/**
 * The transport's own fetch parameter type, reached through the function rather
 * than written as `typeof fetch`.
 *
 * Not a stylistic dodge: biome.json's `noRestrictedGlobals` exemption names
 * `src/api/client.ts`, `src/realtime/sse-connection.ts` and `src/test/**`, and a
 * test file beside the transport is in none of them — MEASURED, `typeof fetch`
 * here fails `biome ci` with "Do not use the global variable fetch". Widening
 * the exemption to cover this file would switch the bare-`fetch` ban off for it
 * permanently, which is the ban filling with holes as the app grows. Deriving
 * the type keeps the fake honest to the signature it is standing in for and
 * leaves the ban intact.
 */
type FetchLike = NonNullable<Parameters<typeof openAttentionStream>[0]['fetchImpl']>

function fakeTransport(status: (n: number) => number = () => 200) {
  const conns: FakeConn[] = []
  const resumeHeaders: (string | null)[] = []
  const fetchImpl = (async (_url: string, init?: RequestInit) => {
    resumeHeaders.push(new Headers(init?.headers).get('Last-Event-ID'))
    const code = status(conns.length + 1)
    if (code !== 200) {
      // The error response reports its REAL signal too. An error body is still
      // a body the browser holds the connection open for until it is drained or
      // cancelled, so "was this attempt cancelled" is a question worth being
      // able to ask on this branch — hard-coding `aborted: false` here made the
      // one test that asks it unable to fail.
      const errSignal = init?.signal
      conns.push({
        push: () => {},
        close: () => {},
        get aborted() {
          return errSignal?.aborted === true
        },
      })
      return new Response(JSON.stringify({ status: code }), {
        status: code,
        headers: { 'content-type': 'application/problem+json' },
      })
    }
    const enc = new TextEncoder()
    let ctl!: ReadableStreamDefaultController<Uint8Array>
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        ctl = c
      },
    })
    let done = false
    const signal = init?.signal
    signal?.addEventListener('abort', () => {
      if (done) return
      done = true
      ctl.error(new DOMException('The operation was aborted.', 'AbortError'))
    })
    conns.push({
      push: (s) => {
        if (!done) ctl.enqueue(enc.encode(s))
      },
      close: () => {
        if (done) return
        done = true
        ctl.close()
      },
      get aborted() {
        return signal?.aborted === true
      },
    })
    return new Response(body, {
      status: 200,
      headers: { 'content-type': 'text/event-stream' },
    })
  }) as unknown as FetchLike
  return { conns, resumeHeaders, fetchImpl }
}

/**
 * The injected clock and BOTH timer seams.
 *
 * The plan injected `now` and `setTimeout` but left the watchdog on the real
 * `setInterval`, which cost two things: the watchdog tests had to wait out real
 * 1s ticks (`vi.waitFor(…, {timeout: 2000})` and a literal 1.1s sleep in the
 * plan's terminal test), and nothing could assert that the interval was
 * CLEARED rather than merely quiet. Injecting the interval makes both
 * deterministic and instant: `tick()` runs the watchdog exactly when the test
 * says so, and `tickIgnoringClear()` runs it even after teardown, which is the
 * only way to tell "cleared" apart from "guarded".
 */
function fakeTimers() {
  let t = 0
  let nextId = 1
  const delays: number[] = []
  const intervals: { id: number; ms: number; fn: () => void }[] = []
  const cleared: number[] = []
  return {
    now: () => t,
    at(ms: number) {
      t = ms
    },
    delays,
    intervals,
    cleared,
    setTimeoutImpl: ((fn: () => void, ms: number) => {
      delays.push(ms)
      return setTimeout(fn, 0)
    }) as unknown as typeof setTimeout,
    setIntervalImpl: ((fn: () => void, ms: number) => {
      const id = nextId++
      intervals.push({ id, ms, fn })
      return id
    }) as unknown as typeof setInterval,
    clearIntervalImpl: ((id: number) => {
      cleared.push(id)
    }) as unknown as typeof clearInterval,
    /** Runs every interval the transport has not cleared. */
    tick() {
      for (const i of intervals) if (!cleared.includes(i.id)) i.fn()
    },
    /** Runs every interval, cleared or not: proves the callback's own guard. */
    tickIgnoringClear() {
      for (const i of intervals) i.fn()
    },
  }
}

/**
 * Drain the pending microtask and macrotask queues.
 *
 * NOT a sleep-until-it-passes: every assertion that follows a `settle()` is
 * written so that an under-drained queue FAILS the test rather than passing it
 * (see the watchdog-heartbeat test, where an unread heartbeat leaves the
 * cursor at 0 and the watchdog fires). The stream pipeline is microtask-driven,
 * so a single 0ms macrotask turn is behind all of it.
 */
const settle = async () => {
  await new Promise((r) => setTimeout(r, 0))
  await new Promise((r) => setTimeout(r, 0))
}

const evt = (id: number, kind = 'gate.waiting') =>
  `id: ${id}\nevent: ${kind}\ndata: ${JSON.stringify({ id, run_id: 'r1', kind, payload: {}, occurred_at: '' })}\n\n`

const grantsProbe = () => async () => ({ grants: [{ project_id: 'p1' }] })

/**
 * The probe's deadline, which every forbidden test sees in `delays`.
 *
 * `onForbidden` races `probeMe()` against a timer, and the fake records EVERY
 * timer the transport schedules — so the losing side of that race is visible
 * here even when the probe answers instantly. That is deliberate: it means the
 * deadline cannot be deleted or retuned without a test saying so. The terminal
 * claims below are still carried by the ABSENCE of a BACKOFF_BASE_MS entry
 * beside it, not by an empty list.
 */
const PROBE_DEADLINE = 10_000

describe('sse-connection', () => {
  // Only the no-seams test below installs fake timers; every other test in this
  // file drives real 0ms macrotasks through `settle()` and would hang under
  // them. `restoreMocks`/`unstubGlobals` do not undo `useFakeTimers`, so the
  // restore has to be explicit.
  afterEach(() => {
    vi.useRealTimers()
  })

  it('sends Last-Event-ID on resume, advanced only by frames that carry an id', async () => {
    const { conns, resumeHeaders, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const conn = openAttentionStream({
      onEvent: () => {},
      fetchImpl,
      probeMe: grantsProbe(),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    expect(conns).toHaveLength(1)
    conns[0]?.push(evt(41))
    conns[0]?.push(': hb\n\n') // a heartbeat carries no id and must NOT advance it
    await settle()
    conns[0]?.close()
    await vi.waitFor(() => expect(resumeHeaders).toHaveLength(2))
    expect(resumeHeaders[0]).toBeNull()
    expect(resumeHeaders[1]).toBe('41')
    conn.close()
  })

  it('a comment heartbeat resets the watchdog; 45s of silence trips it', async () => {
    // The plan's version pushed the heartbeat at t=30s and then jumped to
    // t=90s, which is past the 45s threshold measured from EITHER the open or
    // the heartbeat — so it fired identically with the heartbeat ignored and
    // proved nothing about the reset. Here the first tick is at t=50s: with the
    // heartbeat counted the elapsed time is 0, without it 50s, and the two
    // outcomes differ.
    const { conns, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const states: string[] = []
    const conn = openAttentionStream({
      onEvent: (e) => {
        if (e.type === 'state') states.push(e.state)
      },
      fetchImpl,
      probeMe: grantsProbe(),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    await vi.waitFor(() => expect(states).toContain('live'))
    expect(timers.intervals[0]?.ms).toBe(1_000)

    timers.at(50_000)
    conns[0]?.push(': hb\n\n')
    await settle()
    timers.tick()
    expect(states.at(-1)).toBe('live')

    timers.at(96_000) // 46s past the heartbeat
    timers.tick()
    expect(states.at(-1)).toBe('reconnecting')
    conn.close()
  })

  it('the watchdog tears the stale connection down once and reopens it under backoff', async () => {
    // Emitting `reconnecting` without aborting would leave the tab attached to
    // a dead socket forever, honestly labelled and permanently useless. The
    // second tick is the other half: the teardown is asynchronous — the read
    // rejects on a microtask — so a watchdog that did not notice the connection
    // it had already aborted would report `reconnecting` once a second for as
    // long as the catch took to run.
    const { conns, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const states: string[] = []
    const conn = openAttentionStream({
      onEvent: (e) => {
        if (e.type === 'state') states.push(e.state)
      },
      fetchImpl,
      probeMe: grantsProbe(),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    await settle()
    timers.at(50_000)
    timers.tick()
    timers.tick()
    expect(states.filter((s) => s === 'reconnecting')).toHaveLength(1)
    expect(conns[0]?.aborted).toBe(true)
    await vi.waitFor(() => expect(conns).toHaveLength(2))
    // 50s alive is past STABLE_MS, so this attempt starts the backoff over.
    expect(timers.delays).toEqual([500])
    conn.close()
  })

  it('the watchdog stays quiet while a reconnect is pending', async () => {
    // There is no connection to watch during a backoff wait, and the wait can
    // be 30s. A watchdog that only compared clocks would emit `reconnecting`
    // every second for the whole wait — and then abort each fresh attempt about
    // a second after it started, so a server slow to answer could never finish
    // opening. `lastActivity` is genuinely stale here; being stale is not the
    // question.
    const { conns, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const pending: (() => void)[] = []
    const states: string[] = []
    const conn = openAttentionStream({
      onEvent: (e) => {
        if (e.type === 'state') states.push(e.state)
      },
      fetchImpl,
      probeMe: grantsProbe(),
      now: timers.now,
      // Holds the backoff callback instead of running it, which is the only way
      // to observe the window between "reconnect scheduled" and "reopened".
      setTimeoutImpl: ((fn: () => void, ms: number) => {
        timers.delays.push(ms)
        pending.push(fn)
        return 0
      }) as unknown as typeof setTimeout,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    await vi.waitFor(() => expect(states).toContain('live'))
    conns[0]?.close()
    await vi.waitFor(() => expect(pending).toHaveLength(1))
    const during = states.length

    timers.at(300_000)
    timers.tick()
    timers.tick()
    expect(states).toHaveLength(during)
    // And the OPENING deadline is disarmed for the wait too. This is the one
    // path that reaches reconnect() with a controller nobody aborted — the
    // server closed the stream cleanly — so a stale `attemptStartedAt` would
    // read as an opening that overran by four minutes and tear down a
    // connection that already finished.
    expect(conns[0]?.aborted).toBe(false)

    pending[0]?.()
    await vi.waitFor(() => expect(conns).toHaveLength(2))
    conn.close()
  })

  it('an ordinary close reconnects, through the backoff timer', async () => {
    const { conns, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const conn = openAttentionStream({
      onEvent: () => {},
      fetchImpl,
      probeMe: grantsProbe(),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    conns[0]?.close()
    await vi.waitFor(() => expect(conns).toHaveLength(2))
    expect(timers.delays).toEqual([500])
    conn.close()
  })

  it('forbidden + 200-with-grants is a RESOLVER OUTAGE: evict nothing, reconnect UNDER BACKOFF', async () => {
    // The backend says so outright: after three consecutive resolver failures
    // "the client gets the same frame a revocation would send". Treating the
    // frame as proof of lost authorization would leave the tab permanently
    // realtime-dead, falsely claiming a permission problem, while REST worked
    // fine the whole time.
    //
    // The plan asserted this arm with a 50ms real sleep and `opened.length`,
    // which cannot tell "reconnected immediately" from "reconnected after the
    // 500ms backoff" — it passed for a timing reason. The delay list is the
    // assertion that carries the claim: a grants store that is down stays down,
    // and reopening with no delay would drive it straight back through the
    // same three-failure cycle that produced the frame.
    const { conns, resumeHeaders, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const events: string[] = []
    const conn = openAttentionStream({
      onEvent: (e) => events.push(e.type === 'state' ? `state:${e.state}` : e.type),
      fetchImpl,
      probeMe: grantsProbe(),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    conns[0]?.push(evt(7))
    conns[0]?.push('event: forbidden\ndata: {"error":"forbidden"}\n\n')
    await vi.waitFor(() => expect(conns).toHaveLength(2))
    expect(timers.delays).toEqual([PROBE_DEADLINE, 500])
    expect(events).not.toContain('scope-lost')
    expect(events).not.toContain('session-lost')
    // forbidden carries no id, so the resume point is still the last real frame.
    expect(resumeHeaders[1]).toBe('7')
    // The forbidden frame ENDS its connection. Abandoning the read loop without
    // aborting would leave the old stream open on the server for the life of
    // the page, one leaked socket per resolver outage, while a second one is
    // opened alongside it.
    expect(conns[0]?.aborted).toBe(true)
    conn.close()
  })

  it('forbidden + 200-with-NO-grants is an empty inbox, not an authorization error', async () => {
    // An active principal who legitimately sees nothing. Rendering the
    // authorization panel here would be the UI lying on the server's behalf.
    const { conns, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const events: string[] = []
    const conn = openAttentionStream({
      onEvent: (e) => events.push(e.type === 'state' ? `state:${e.state}` : e.type),
      fetchImpl,
      probeMe: async () => ({ grants: [] }),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    conns[0]?.push('event: forbidden\ndata: {"error":"forbidden"}\n\n')
    await vi.waitFor(() => expect(events).toContain('scope-lost'))
    expect(events).toContain('state:stopped')
    // Terminal means terminal: nothing scheduled, nothing reopened.
    await settle()
    expect(timers.delays).toEqual([PROBE_DEADLINE])
    expect(conns).toHaveLength(1)
    conn.close()
  })

  it('treats a null grants array as an empty one, because the contract says it can be null', async () => {
    // MEASURED against the generated types: `ApiMeResponse.grants` is
    // `ApiGrantResponse[] | null` (src/api/schema.d.ts). The Go handler builds
    // `make([]GrantResponse, 0, len(grants))` today (internal/identity/api/api.go:215)
    // so the wire currently carries `[]` — but the CONTRACT permits null, and
    // null is exactly the empty-inbox case this branch exists for. `me.grants.length`
    // on null throws a TypeError, which onForbidden's catch would read as a
    // failed probe and answer with an endless reconnect loop: the one arm that
    // must stop would be the one that never stops.
    const { conns, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const events: string[] = []
    const conn = openAttentionStream({
      onEvent: (e) => events.push(e.type === 'state' ? `state:${e.state}` : e.type),
      fetchImpl,
      probeMe: async () => ({ grants: null }),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    conns[0]?.push('event: forbidden\ndata: {"error":"forbidden"}\n\n')
    await vi.waitFor(() => expect(events).toContain('scope-lost'))
    expect(timers.delays).toEqual([PROBE_DEADLINE])
    conn.close()
  })

  it('forbidden + 401 is a dead session', async () => {
    const { conns, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const events: string[] = []
    const conn = openAttentionStream({
      onEvent: (e) => events.push(e.type),
      fetchImpl,
      probeMe: async () => {
        throw new ApiError({
          type: '',
          title: '',
          status: 401,
          detail: '',
          correlationId: '',
          fields: [],
        })
      },
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    conns[0]?.push('event: forbidden\ndata: {"error":"forbidden"}\n\n')
    await vi.waitFor(() => expect(events).toContain('session-lost'))
    expect(events).not.toContain('scope-lost')
    await settle()
    expect(timers.delays).toEqual([PROBE_DEADLINE])
    // The teardown, asserted BEFORE conn.close() does it by hand. MEASURED:
    // without this line, deleting `stop()` from this arm leaves the suite fully
    // green, because the trailing close() clears the same interval — and in a
    // real tab the 1Hz watchdog would then run for the life of the page after a
    // session was declared lost.
    expect(timers.cleared).toEqual([timers.intervals[0]?.id])
    conn.close()
  })

  it('forbidden + a probe that fails for any other reason reconnects rather than evicting', async () => {
    // A 500 or a dropped connection on /auth/me says nothing about
    // authorization, and REST still enforces it on every request. Concluding
    // scope-lost here would blank the UI because a health check hiccuped.
    const { conns, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const events: string[] = []
    const conn = openAttentionStream({
      onEvent: (e) => events.push(e.type),
      fetchImpl,
      probeMe: async () => {
        throw new ApiError({
          type: '',
          title: '',
          status: 503,
          detail: '',
          correlationId: '',
          fields: [],
        })
      },
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    conns[0]?.push('event: forbidden\ndata: {"error":"forbidden"}\n\n')
    await vi.waitFor(() => expect(conns).toHaveLength(2))
    expect(events).not.toContain('scope-lost')
    expect(events).not.toContain('session-lost')
    expect(timers.delays).toEqual([PROBE_DEADLINE, 500])
    conn.close()
  })

  it('backs off exponentially between reconnects', async () => {
    const { conns, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const conn = openAttentionStream({
      onEvent: (e) => {
        // Every connection is accepted and closes at once: the shape a proxy
        // in front of a restarting backend produces.
        if (e.type === 'state' && e.state === 'live') conns.at(-1)?.close()
      },
      fetchImpl,
      probeMe: grantsProbe(),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    await vi.waitFor(() => expect(timers.delays.length).toBeGreaterThanOrEqual(4))
    expect(timers.delays.slice(0, 4)).toEqual([500, 1_000, 2_000, 4_000])
    conn.close()
  })

  it('caps the backoff rather than growing without bound', async () => {
    // `2 ** attempt` is unbounded: without the cap a tab left open against a
    // dead server reaches hours between attempts within a minute, and Infinity
    // not long after — a client that never comes back once the server does.
    const { conns, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const conn = openAttentionStream({
      onEvent: (e) => {
        if (e.type === 'state' && e.state === 'live') conns.at(-1)?.close()
      },
      fetchImpl,
      probeMe: grantsProbe(),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    await vi.waitFor(() => expect(timers.delays.length).toBeGreaterThanOrEqual(9))
    expect(timers.delays.slice(0, 9)).toEqual([
      500, 1_000, 2_000, 4_000, 8_000, 16_000, 30_000, 30_000, 30_000,
    ])
    conn.close()
  })

  it('does NOT reset backoff for a connection that opened and even delivered a frame', async () => {
    // The proxy-in-front-of-a-restarting-backend shape again, one step later:
    // the opening snapshot arrives and THEN the stream dies. A transport that
    // reset `attempt` on `res.ok`, or on the first byte, would emit 500, 500,
    // 500 forever and turn the retry loop into a hot loop against a server that
    // is already unwell. Only ELAPSED TIME separates "this connection worked"
    // from "this connection was accepted".
    //
    // The clock steps 24_999 per connection — one millisecond SHORT of
    // STABLE_MS — rather than staying frozen at 0. Frozen, `now() - openedAt`
    // is always 0, which satisfies "below the threshold" for ANY positive
    // value, and the constant was then pinned only to (0, 30_000] by the
    // sibling test's 30_000 step. MEASURED at the frozen version: STABLE_MS of
    // 1, 500, 24_999 and 30_000 all kept 25/25 green, and `= 1` reintroduces
    // verbatim the hot loop this constant exists to prevent, since a proxy that
    // accepts and closes after 2ms would reset `attempt` every cycle. Stepping
    // to just beneath the threshold brackets it from below; with the sibling
    // test's step above it, the pair pins STABLE_MS to (24_999, 30_000].
    const { conns, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    let t = 0
    const conn = openAttentionStream({
      onEvent: (e) => {
        if (e.type === 'state' && e.state === 'live') {
          t += 24_999
          timers.at(t)
          const c = conns.at(-1)
          c?.push(evt(1))
          c?.close()
        }
      },
      fetchImpl,
      probeMe: grantsProbe(),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    await vi.waitFor(() => expect(timers.delays.length).toBeGreaterThanOrEqual(4))
    expect(new Set(timers.delays.slice(0, 4)).size).toBe(4)
    conn.close()
  })

  it('resets backoff after a connection that lasted', async () => {
    // The other half: without it, `attempt` would be write-only and the delay
    // would climb to 30s and stay there for the rest of the session, however
    // healthy the server became. One line differs from the test above — the
    // clock advances past STABLE_MS before the stream dies — and it is the line
    // the rule is about.
    const { conns, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    let t = 0
    const conn = openAttentionStream({
      onEvent: (e) => {
        if (e.type === 'state' && e.state === 'live') {
          t += 30_000
          timers.at(t)
          conns.at(-1)?.close()
        }
      },
      fetchImpl,
      probeMe: grantsProbe(),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    await vi.waitFor(() => expect(timers.delays.length).toBeGreaterThanOrEqual(3))
    expect(timers.delays.slice(0, 3)).toEqual([500, 500, 500])
    conn.close()
  })

  it('stops the watchdog on a terminal outcome, and the callback refuses to fire anyway', async () => {
    // Deliberately does NOT call close(). Without this the suite proves nothing
    // about termination: every other test calls conn.close() right after the
    // terminal event, which clears the interval by hand and hides a timer that
    // would otherwise keep firing `reconnecting` at a connection that was
    // declared stopped.
    //
    // Two independent guards, so a mutant can only be caught by the one that
    // covers it: the interval must be CLEARED (by its own handle), and the
    // callback must refuse to act when run anyway.
    const { conns, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const events: string[] = []
    openAttentionStream({
      onEvent: (e) => events.push(e.type === 'state' ? `state:${e.state}` : e.type),
      fetchImpl,
      probeMe: async () => ({ grants: [] }),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    conns[0]?.push('event: forbidden\ndata: {"error":"forbidden"}\n\n')
    await vi.waitFor(() => expect(events).toContain('scope-lost'))
    const after = events.length
    expect(timers.cleared).toEqual([timers.intervals[0]?.id])

    timers.at(240_000)
    timers.tickIgnoringClear()
    await settle()
    expect(events).toHaveLength(after)
    expect(conns).toHaveLength(1)
    expect(timers.delays).toEqual([PROBE_DEADLINE])
  })

  it('treats an opening 401 as a dead session, not a retryable fault', async () => {
    // realtime/api authenticates BEFORE the handler so this is a real
    // problem+json 401 rather than a stream that opens and says nothing. A
    // transport that folds it into `throw new Error(...)` retries a dead
    // session until the tab closes.
    const { conns, fetchImpl } = fakeTransport(() => 401)
    const timers = fakeTimers()
    const events: string[] = []
    const conn = openAttentionStream({
      onEvent: (e) => events.push(e.type),
      fetchImpl,
      probeMe: async () => ({ grants: [] }),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    await vi.waitFor(() => expect(events).toContain('session-lost'))
    expect(timers.delays).toEqual([])
    await settle()
    expect(conns).toHaveLength(1)
    // Same masking as the forbidden+401 arm: without this, deleting `stop()`
    // here survives the whole suite on the strength of the trailing close().
    expect(timers.cleared).toEqual([timers.intervals[0]?.id])
    conn.close()
  })

  it('retries a 503 open, which is the transient half of the same branch', async () => {
    const { conns, fetchImpl } = fakeTransport((n) => (n === 1 ? 503 : 200))
    const timers = fakeTimers()
    const events: string[] = []
    const conn = openAttentionStream({
      onEvent: (e) => events.push(e.type === 'state' ? `state:${e.state}` : e.type),
      fetchImpl,
      probeMe: grantsProbe(),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    await vi.waitFor(() => expect(events).toContain('state:live'))
    expect(events).not.toContain('session-lost')
    expect(timers.delays).toEqual([500])
    expect(conns).toHaveLength(2)
    // The 503 arrives as a Response with a body that the transport throws past
    // without ever reading. A browser holds that connection open until the body
    // is drained or cancelled, so abandoning the attempt has to cancel it —
    // otherwise every failed open leaks a socket, forever, against a server
    // that is by definition already in trouble.
    expect(conns[0]?.aborted).toBe(true)
    conn.close()
  })

  it('an opening fetch that never answers is torn down and retried', async () => {
    // The server accepts the connection and never sends response headers: a
    // proxy that held the socket, a backend wedged before its first write.
    // MEASURED against a watchdog gated on `openedAt !== null`, this shape gave
    // `opens=1 events=[] delays=[]` after 120 simulated seconds — one attempt,
    // no retry, and on a FIRST connect not even a `reconnecting` for the UI, so
    // the indicator sits at its initial value for the life of the page. The
    // deadline is measured from the ATTEMPT, which is what keeps it from
    // misfiring through a backoff wait the way an unconditional check does.
    const timers = fakeTimers()
    const states: string[] = []
    let opens = 0
    const conn = openAttentionStream({
      onEvent: (e) => {
        if (e.type === 'state') states.push(e.state)
      },
      // Real fetch semantics: pending until the signal fires, then rejected.
      fetchImpl: ((_u: string, init?: RequestInit) => {
        opens += 1
        return new Promise<Response>((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () =>
            reject(new DOMException('The operation was aborted.', 'AbortError')),
          )
        })
      }) as unknown as FetchLike,
      probeMe: grantsProbe(),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    await settle()

    // At the deadline exactly, the attempt is still the server's to answer.
    timers.at(20_000)
    timers.tick()
    await settle()
    expect(opens).toBe(1)
    expect(timers.delays).toEqual([])

    timers.at(20_001)
    timers.tick()
    await vi.waitFor(() => expect(opens).toBe(2))
    expect(timers.delays).toEqual([500])
    // The abort is only half of it: the UI has to be told, and reconnect() is
    // what tells it — the first thing this stream ever says about itself.
    expect(states).toEqual(['reconnecting'])
    conn.close()
  })

  it('a probe that never answers reconnects, and its late answer cannot evict', async () => {
    // A hung GET /auth/me is terminal BY ACCIDENT without a deadline: MEASURED,
    // `events=["state:live"] conns=1 delays=[] cleared=[]` after 200 simulated
    // minutes — the forbidden frame ended the connection and nothing ever
    // reopened it. Expiry has to route into the reconnect arm, because a probe
    // that did not answer proves nothing about authorization.
    //
    // The second half is why the deadline is a RACE and not a flag: the probe
    // eventually answers, and by then its answer belongs to a connection that
    // no longer exists. `grants: []` arriving late must not evict the stream
    // that replaced it.
    const { conns, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const events: string[] = []
    let answer!: (m: { grants: unknown[] }) => void
    const conn = openAttentionStream({
      onEvent: (e) => events.push(e.type === 'state' ? `state:${e.state}` : e.type),
      fetchImpl,
      probeMe: () =>
        new Promise<{ grants: unknown[] }>((r) => {
          answer = r
        }),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    await settle()
    conns[0]?.push('event: forbidden\ndata: {"error":"forbidden"}\n\n')
    await vi.waitFor(() => expect(conns).toHaveLength(2))
    expect(timers.delays).toEqual([PROBE_DEADLINE, 500])
    expect(events).not.toContain('scope-lost')
    expect(events).not.toContain('session-lost')

    answer({ grants: [] })
    await settle()
    expect(events).not.toContain('scope-lost')
    expect(timers.cleared).toEqual([])
    expect(conns).toHaveLength(2)
    conn.close()
  })

  it('a consumer that throws does not take the reconnection machine with it', async () => {
    // `onEvent` runs synchronously on the transport's control path. MEASURED
    // without isolation: a consumer throwing on every event left `conns = 1,
    // delays = []` after twenty simulated minutes, with vitest reporting
    // `Unhandled Rejection: consumer blew up ❯ emit ❯ reconnect` — the throw
    // escaped `emit({state:'reconnecting'})` before `later(connect, delay)`
    // ran, so the stream was permanently dead because someone else's render
    // threw once.
    const { conns, resumeHeaders, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const seen: string[] = []
    const conn = openAttentionStream({
      onEvent: (e) => {
        seen.push(e.type === 'state' ? `state:${e.state}` : e.type)
        throw new Error('consumer blew up')
      },
      fetchImpl,
      probeMe: grantsProbe(),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    await settle()
    conns[0]?.push(evt(7))
    await settle()
    conns[0]?.close()
    await vi.waitFor(() => expect(conns).toHaveLength(2))
    expect(timers.delays).toEqual([500])
    // The throw does not end the read loop either, and the machine keeps going
    // through every event: the frame was delivered and rejected, the stream
    // then closed, and the replacement went live — all four handed to a
    // consumer that threw on every one of them.
    expect(seen).toEqual(['state:live', 'frame', 'state:reconnecting', 'state:live'])
    // AT MOST ONCE, asserted. The cursor advances even though the consumer
    // refused the frame, so the resume asks for events above 7 rather than
    // asking the server to redeliver the frame that just broke the consumer on
    // every reconnect for the rest of the session.
    expect(resumeHeaders[1]).toBe('7')
    conn.close()
  })

  it('close() during the opening fetch reports nothing and opens nothing', async () => {
    // The tab navigating away mid-connect. `connect()` checks `closed` on the
    // way in, but the response arrives long after that check has passed, so
    // every emit downstream of the await is reachable after close — starting
    // with the one that would announce a connection nobody is listening to as
    // `live`.
    const timers = fakeTimers()
    const events: string[] = []
    let release!: (r: Response) => void
    const gate = new Promise<Response>((r) => {
      release = r
    })
    const conn = openAttentionStream({
      onEvent: (e) => events.push(e.type === 'state' ? `state:${e.state}` : e.type),
      fetchImpl: (() => gate) as unknown as FetchLike,
      probeMe: grantsProbe(),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    conn.close()
    release(
      new Response(new ReadableStream<Uint8Array>({ start() {} }), {
        status: 200,
        headers: { 'content-type': 'text/event-stream' },
      }),
    )
    await settle()
    expect(events).toEqual([])
    expect(timers.delays).toEqual([])
  })

  it('a stream that ends in the same turn as close() schedules no reconnect', async () => {
    // The read loop's `done` continuation is a microtask, so a server close and
    // an app close can land in that order within one turn. Reaching the bottom
    // of the loop and reconnecting anyway would leave a timer — and eventually
    // a connection — belonging to a stream the caller has already closed.
    const { conns, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const conn = openAttentionStream({
      onEvent: () => {},
      fetchImpl,
      probeMe: grantsProbe(),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    await settle()
    conns[0]?.close()
    conn.close()
    await settle()
    expect(timers.delays).toEqual([])
    expect(conns).toHaveLength(1)
  })

  it('a close during the backoff wait cancels the pending reopen', async () => {
    const { conns, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const pending: (() => void)[] = []
    const conn = openAttentionStream({
      onEvent: () => {},
      fetchImpl,
      probeMe: grantsProbe(),
      now: timers.now,
      setTimeoutImpl: ((fn: () => void, ms: number) => {
        timers.delays.push(ms)
        pending.push(fn)
        return 0
      }) as unknown as typeof setTimeout,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    await settle()
    conns[0]?.close()
    await vi.waitFor(() => expect(pending).toHaveLength(1))
    conn.close()
    pending[0]?.()
    await settle()
    expect(conns).toHaveLength(1)
  })

  it('close() aborts the stream, clears the watchdog and silences every later event', async () => {
    const { conns, fetchImpl } = fakeTransport()
    const timers = fakeTimers()
    const events: string[] = []
    const conn = openAttentionStream({
      onEvent: (e) => events.push(e.type),
      fetchImpl,
      probeMe: grantsProbe(),
      now: timers.now,
      setTimeoutImpl: timers.setTimeoutImpl,
      setIntervalImpl: timers.setIntervalImpl,
      clearIntervalImpl: timers.clearIntervalImpl,
    })
    await vi.waitFor(() => expect(events).toContain('state'))
    conn.close()
    expect(conns[0]?.aborted).toBe(true)
    expect(timers.cleared).toEqual([timers.intervals[0]?.id])
    const after = events.length
    // The abort rejects the in-flight read; a transport that reconnected out of
    // its own catch would reopen here.
    await settle()
    timers.at(240_000)
    timers.tickIgnoringClear()
    await settle()
    expect(events).toHaveLength(after)
    expect(conns).toHaveLength(1)
  })

  it('with NO seams injected, a hung opening fetch is torn down and retried', async () => {
    // THE DEFAULTS ARE WHAT SHIPS, and every other test in this file replaces
    // all five seams. There is no consumer of openAttentionStream in the tree
    // yet (`grep -rn openAttentionStream src --include='*.ts' --include='*.tsx'
    // | grep -v sse-connection` -> exit 1), so the `??` right-hand sides are
    // exactly the wiring Task 11 will get, and until this test existed not one
    // of them was ever executed.
    //
    // MEASURED, each mutant alone against `tsc --noEmit` + the full file:
    // `now ?? (() => 0)`, `setIntervalImpl ?? setTimeout`,
    // `setTimeoutImpl ?? setInterval`, `clearIntervalImpl ?? (() => {})`,
    // `(input, _init) => fetch(input)` and `fetchImpl ?? fetch` ALL passed tsc
    // and all 24 tests. The first two and the fifth reproduce the verbatim
    // symptom the opening-deadline test above exists to prevent — one attempt,
    // nothing emitted, nothing scheduled; the third turns the one-shot backoff
    // into a 2Hz reconnect storm; the fourth leaves a 1Hz interval running for
    // the life of the page after close().
    //
    // `vi.useFakeTimers()` is what makes the untouched defaults observable: it
    // replaces Date, setTimeout AND setInterval together, so the module picks
    // up fakes through its own `??` fallbacks rather than through an injected
    // seam. That is the point — the fallback expression itself is under test.
    // (`clearIntervalImpl ?? clearTimeout` is NOT a defect and no assertion
    // here chases it: clearTimeout and clearInterval are interchangeable by
    // spec, and MEASURED, that mutant leaves all 25 tests green.)
    vi.useFakeTimers()
    const states: string[] = []
    // Two counters rather than one, because WHICH global answered is itself an
    // assertion further down. Named fields rather than a tuple: under
    // `noUncheckedIndexedAccess` an array index is `number | undefined` and
    // `opens[slot] += 1` does not typecheck — MEASURED, `error TS2532: Object is
    // possibly 'undefined'`, which vitest happily ran straight past.
    const opens = { before: 0, after: 0 }
    // A fetch that hangs until its signal fires, then rejects the way real fetch
    // does. `init?.signal` is the load-bearing read: the default wrapper has to
    // forward `init` for the abort to be able to reach anything at all.
    const hung = (slot: 'before' | 'after') => (_u: string, init?: RequestInit) => {
      opens[slot] += 1
      return new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () =>
          reject(new DOMException('The operation was aborted.', 'AbortError')),
        )
      })
    }
    // NOT `fetchImpl:` — stubbed on the global instead, so the fifth default,
    // the `(input, init) => fetch(input, init)` wrapper, is exercised too rather
    // than substituted away. `unstubGlobals` restores it, and msw's interceptor
    // is simply replaced for the duration rather than consulted.
    vi.stubGlobal('fetch', hung('before'))
    const conn = openAttentionStream({
      onEvent: (e) => {
        if (e.type === 'state') states.push(e.state)
      },
      probeMe: async () => ({ grants: [] }),
    })

    // At the deadline exactly the attempt is still the server's to answer, so
    // the default clock has to be advancing AND the default interval has to be
    // repeating for anything at all to happen one tick later.
    await vi.advanceTimersByTimeAsync(20_000)
    expect(opens).toEqual({ before: 1, after: 0 })

    await vi.advanceTimersByTimeAsync(1_001)
    expect(states).toEqual(['reconnecting'])

    // Swapped MID-CONNECTION, which is the property the wrapper exists for and
    // the only thing that separates it from a plain `opts.fetchImpl ?? fetch`:
    // that form resolves the global once, when openAttentionStream is called,
    // and every later attempt keeps talking to the fetch that was installed
    // then. MEASURED — with the wrapper replaced by `?? fetch`, tsc and all 25
    // tests passed until this swap existed.
    vi.stubGlobal('fetch', hung('after'))
    await vi.advanceTimersByTimeAsync(500)
    expect(opens).toEqual({ before: 1, after: 1 })

    // The backoff is a ONE-SHOT. `setTimeoutImpl ?? setInterval` reopens every
    // 500ms forever, which is a hot loop against a server already in trouble.
    await vi.advanceTimersByTimeAsync(2_000)
    expect(opens).toEqual({ before: 1, after: 1 })

    // And the default clear actually clears: without it the 1Hz watchdog
    // outlives the connection for the life of the page.
    //
    // Asserted as a DELTA, not as `toBe(0)`. vi.getTimerCount() is global, so
    // an absolute count couples this test to every test that ran before it:
    // any earlier failure that skips its own conn.close() leaves a 0ms
    // reconnect loop re-arming against the fake clock installed here, and this
    // test then fails a second time pointing at a timer-clearing default that
    // is perfectly fine. MEASURED — with `toBe(0)`, three unrelated source
    // mutants (STABLE_MS 45_000, STABLE_MS 30_001, BACKOFF_MAX_MS 3_600_000)
    // each produced two failures instead of one, and the phantom vanished when
    // this test was run in isolation. The delta cancels foreign timers out
    // while keeping the full kill power: the mutant that drops the clear
    // (`clearIntervalImpl ?? (() => {})`) still dies here.
    const armed = vi.getTimerCount()
    conn.close()
    expect(vi.getTimerCount()).toBe(armed - 1)
  })
})
