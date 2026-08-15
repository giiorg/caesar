import { ApiError } from '../api/errors'
import type { ConnectionState } from './connection-state'
import { createSseParser, type ParsedFrame } from './sse-parser'

export type EventFrame = Extract<ParsedFrame, { kind: 'event' }>

export type ConnectionEvent =
  | { type: 'frame'; frame: EventFrame }
  | { type: 'state'; state: ConnectionState }
  | { type: 'scope-lost' }
  | { type: 'session-lost' }

const WATCHDOG_MS = 45_000 // the server heartbeats an idle stream at 20s
const WATCHDOG_TICK_MS = 1_000
const BACKOFF_BASE_MS = 500
const BACKOFF_MAX_MS = 30_000
// How long the OPENING fetch gets to produce response headers. Measured from
// the attempt, not from lastActivity: a connection that has not opened has no
// activity to be stale relative to, which is why the live watchdog cannot cover
// this window (it would misfire through every backoff wait instead). One full
// heartbeat interval is already generous — the handler authenticates, resolves
// scope and writes the stream headers before it does any work
// (backend/internal/realtime/stream.go), so a server that has said nothing in
// 20s is not answering, it is wedged.
const OPEN_TIMEOUT_MS = 20_000
// How long GET /auth/me gets to answer the forbidden probe. Expiry routes into
// the RECONNECT arm, never the terminal one: a probe that did not answer proves
// nothing about authorization, and REST still enforces it on every request.
const PROBE_TIMEOUT_MS = 10_000
// How long a connection must survive before it counts as having worked. Above
// the server's 20s heartbeat, so a stream that stayed open across at least one
// idle beat qualifies; see reconnect() for why elapsed time is the signal.
const STABLE_MS = 25_000

/**
 * The shape `probeMe` must satisfy.
 *
 * `grants` is nullable because THE CONTRACT SAYS SO: `ApiMeResponse.grants` is
 * generated as `ApiGrantResponse[] | null` (src/api/schema.d.ts), which is what
 * a Go nil slice marshals to. The handler happens to build a non-nil
 * `make([]GrantResponse, 0, len(grants))` today
 * (backend/internal/identity/api/api.go:215), so the wire currently carries
 * `[]` — but a client that reads `.length` off the value the contract permits
 * would throw a TypeError on exactly the empty-inbox case, and onForbidden's
 * catch would then read the crash as a failed probe and reconnect forever. The
 * one arm that must stop would be the one that never stops.
 */
export type MeProbe = { grants: readonly unknown[] | null }

/**
 * fetch + ReadableStream, NOT EventSource.
 *
 * EventSource reconnects on a fixed interval the page cannot control, surfaces
 * errors opaquely, and hides the HTTP status — so it can deliver neither
 * exponential backoff nor a heartbeat watchdog that surfaces staleness
 * honestly, and it cannot distinguish a 401 from a dropped tunnel. The cost is
 * re-implementing Last-Event-ID resume by hand, which this design wants
 * explicit anyway.
 *
 * This layer owns RECONNECTION and never CORRECTNESS. It decides when to
 * reopen and what to say about staleness; what a frame means is the bridge's
 * question, and what the data is is REST's.
 *
 * EVERY CLOCK AND TIMER IS INJECTABLE, including the watchdog's interval. That
 * is not symmetry for its own sake: with the interval left on the real
 * `setInterval`, a suite whose subject is a 45-second threshold has to wait out
 * real 1-second ticks to observe anything, and it can never distinguish a
 * watchdog that was CLEARED from one that merely had nothing to say. Both of
 * those cost the suite a real test.
 */
export function openAttentionStream(opts: {
  onEvent(e: ConnectionEvent): void
  probeMe(): Promise<MeProbe>
  fetchImpl?: typeof fetch
  now?: () => number
  setTimeoutImpl?: typeof setTimeout
  setIntervalImpl?: typeof setInterval
  clearIntervalImpl?: typeof clearInterval
}): { close(): void } {
  // Spelled BARE and wrapped, for two separate reasons. `globalThis.fetch` is
  // banned by tools/lint/no-qualified-fetch.grit (the qualified spellings are
  // the hole noRestrictedGlobals cannot see), and the wrapper keeps the call
  // late-bound so a test that installs a fetch after this module is imported is
  // still the one that answers.
  const doFetch: typeof fetch = opts.fetchImpl ?? ((input, init) => fetch(input, init))
  const now = opts.now ?? (() => Date.now())
  const later = opts.setTimeoutImpl ?? setTimeout
  const every = opts.setIntervalImpl ?? setInterval
  const stopEvery = opts.clearIntervalImpl ?? clearInterval

  let closed = false
  let attempt = 0
  let lastEventId: string | null = null
  let lastActivity = now()
  // When the current connection went live, or null if none is. reconnect()
  // reads it to decide whether the attempt counter has earned a reset, and the
  // watchdog reads it to know whether there is anything to watch.
  let openedAt: number | null = null
  // When the in-flight attempt issued its fetch, or null when no attempt is in
  // flight (between reconnect() and the reopen it schedules). Non-null WITH a
  // null openedAt is exactly the opening window, which is the window the
  // watchdog could not otherwise see.
  let attemptStartedAt: number | null = null
  let controller: AbortController | null = null

  /**
   * A CONSUMER FAULT IS NOT A TRANSPORT FAULT.
   *
   * `onEvent` runs synchronously on the transport's own control path, so
   * without this catch a throwing consumer is indistinguishable from a dead
   * socket and takes the reconnection machine down with it. MEASURED against
   * the unwrapped version: a consumer that threw on every event left
   * `conns = 1, delays = []` after twenty simulated minutes, with vitest
   * reporting `Unhandled Rejection: consumer blew up ❯ emit ❯ reconnect` — the
   * throw escaped `emit({state:'reconnecting'})` BEFORE `later(connect, delay)`
   * ran, so the machine was permanently dead and the tab never came back.
   *
   * Swallowed rather than logged: this module has no logging seam, and the one
   * thing it must not do is convert someone else's bug into a stream that stops
   * reconnecting.
   */
  const emit = (e: ConnectionEvent) => {
    if (closed) return
    try {
      opts.onEvent(e)
    } catch {
      // Deliberately empty; see above.
    }
  }

  const watchdog = every(() => {
    // No `if (closed)` here on purpose. stop() clears this interval AND aborts
    // the controller, so after a close every path below already returns — a
    // `closed` check would be a branch no mutant can kill, which on this project
    // is the definition of a rule nobody has tested. The two guards that make it
    // unnecessary are each pinned by a test of their own ('close() aborts the
    // stream, clears the watchdog…' and the terminal-outcome test).
    //
    // `signal.aborted` keeps a connection already being torn down from being
    // torn down again, and it is also what silences every tick during a backoff
    // wait on the paths that abort.
    if (controller === null || controller.signal.aborted) return
    // THE OPENING WINDOW GETS ITS OWN DEADLINE, measured from the attempt.
    //
    // The plan ran the staleness check unconditionally, which misfires in both
    // directions once a reconnect is in flight: through a backoff of up to 30s
    // `lastActivity` is already older than the threshold, so it emits
    // `reconnecting` every second for the whole wait and then aborts each fresh
    // attempt about a second after it started — a server slow to answer could
    // never finish opening. Gating on `openedAt !== null` fixed that and cost
    // the opening window all coverage: MEASURED, a server that accepts the
    // connection and never sends response headers gave `opens=1 events=[]
    // delays=[]` after 120 simulated seconds of ticks — one attempt, no retry,
    // and on a first connect not even a `reconnecting` for the UI to show.
    // Measuring from `attemptStartedAt` rather than from `lastActivity` is what
    // makes this arm immune to the misfire: it is armed only while a fetch is
    // genuinely outstanding.
    //
    // No `emit` here. The abort rejects the pending fetch, the catch in
    // connect() routes it into reconnect(), and reconnect() announces
    // `reconnecting` one microtask later — announcing it here too would claim a
    // reconnect even from a fetch that ignores its signal and therefore never
    // gets one.
    if (openedAt === null) {
      if (attemptStartedAt !== null && now() - attemptStartedAt > OPEN_TIMEOUT_MS)
        controller.abort()
      return
    }
    // The UI never renders stale data as live. A frozen badge that looks
    // authoritative is worse than an honest one that admits it lost the stream.
    if (now() - lastActivity > WATCHDOG_MS) {
      emit({ type: 'state', state: 'reconnecting' })
      // The abort is what makes this more than a label: it errors the body
      // stream, the pending read rejects, and the catch in connect() reopens.
      // Without it the tab stays attached to a dead socket, correctly labelled
      // and permanently useless.
      controller.abort()
    }
  }, WATCHDOG_TICK_MS)

  /**
   * A TERMINAL outcome, and the reason it is a named function rather than a
   * bare `return`.
   *
   * session-lost and scope-lost end the stream for good: there is nothing left
   * to reconnect to and no caller action that would change the answer. But the
   * watchdog is an interval, and an early `return` out of connect() leaves it
   * running — so 45 seconds after the connection is declared stopped it starts
   * emitting `reconnecting`, contradicting the terminal state it just reported
   * and keeping a timer alive for the life of the page. Every test that calls
   * conn.close() right after observing the terminal event hides this completely,
   * which is why one test deliberately does not.
   */
  function stop(): void {
    closed = true
    stopEvery(watchdog)
    controller?.abort()
  }

  /**
   * THE PROBE. `forbidden` does not say what happened, and three different
   * conditions send those identical bytes: a real revocation; an empty scope
   * at the 30s re-check (which is also what a DEACTIVATED principal and an
   * active member with ZERO grants both resolve to); and a grants-store
   * outage after three consecutive resolver failures.
   *
   * So the client probes rather than concludes. One GET /auth/me is the only
   * thing on the wire that can decide this, and it costs one request on a rare
   * path. Roadmap row 10a makes it unnecessary.
   */
  async function onForbidden(): Promise<void> {
    try {
      // RACED, because the probe is the last thing holding this connection.
      // MEASURED against the unraced version: a /auth/me that never answers
      // left `events=["state:live"] conns=1 delays=[] cleared=[]` after 200
      // simulated minutes — terminal by accident, with no terminal event to
      // show for it. Promise.race is what makes the deadline safe as well as
      // present: the losing side can never settle this function afterwards, so
      // a probe that answers `grants: []` an hour late cannot evict a stream
      // that has long since reconnected.
      const me = await Promise.race([
        opts.probeMe(),
        new Promise<never>((_, reject) => {
          later(() => reject(new Error('probe: timeout')), PROBE_TIMEOUT_MS)
        }),
      ])
      if ((me.grants ?? []).length > 0) {
        // Not about authorization at all — a resolver outage. Evict nothing.
        // Through reconnect(), NOT connect(): a grants store that is down
        // stays down for a while, and reopening with no delay would hammer it
        // through the same three-failure cycle that produced this frame. The
        // outage path is exactly where backoff earns its keep.
        reconnect()
        return
      }
      // An active principal who legitimately sees nothing.
      emit({ type: 'scope-lost' })
      emit({ type: 'state', state: 'stopped' })
      stop()
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        emit({ type: 'session-lost' })
        stop()
        return
      }
      // The probe itself failed for some other reason. Reconnecting is the
      // safe direction: REST still enforces authorization on every request —
      // and under backoff, since a failing probe means the server is unwell.
      reconnect()
    }
  }

  function connect(): void {
    if (closed) return
    const ctl = new AbortController()
    controller = ctl
    attemptStartedAt = now()
    const headers: Record<string, string> = { Accept: 'text/event-stream' }
    if (lastEventId !== null) headers['Last-Event-ID'] = lastEventId

    void (async () => {
      try {
        const res = await doFetch('/api/v1/stream', { headers, signal: ctl.signal })

        // CLASSIFY BEFORE RETRYING. realtime/api authenticates ahead of the
        // handler precisely so an unauthenticated client gets a real 401
        // problem+json rather than a stream that opens and says nothing
        // (realtime/api/api.go). Collapsing that into a generic Error throws
        // away the one distinction the backend went out of its way to make,
        // and reconnecting forever against a dead session is the result.
        if (res.status === 401) {
          emit({ type: 'session-lost' })
          stop()
          return
        }
        if (!res.ok || res.body === null) throw new Error(`stream: ${res.status}`)

        lastActivity = now()
        openedAt = now()
        emit({ type: 'state', state: 'live' })

        const parser = createSseParser()
        const reader = res.body.pipeThrough(new TextDecoderStream()).getReader()
        for (;;) {
          const { done, value } = await reader.read()
          if (done) break
          // Chunk-level, deliberately: the watchdog asks "is this connection
          // still being spoken to", and a `: hb` comment is the server saying
          // yes. Resetting only on frames that decode to events would trip the
          // watchdog on a stream doing exactly what the protocol asks of it.
          lastActivity = now()
          for (const f of parser.push(value)) {
            if (f.kind === 'heartbeat') continue
            if (f.kind === 'forbidden') {
              // Always ends the connection, and carries no id, so the cursor
              // is untouched and a resumed stream still resumes above the last
              // real frame.
              ctl.abort()
              await onForbidden()
              return
            }
            // ONLY frames that carry an id advance the resume point, and the
            // cursor advances whether or not the consumer accepted the frame:
            // delivery here is AT MOST ONCE by choice. Holding the cursor back
            // on a consumer exception would ask the server to redeliver the
            // frame that just broke the consumer, on every reconnect, forever —
            // a poison pill is worse than a dropped event, and this layer owns
            // reconnection and never correctness.
            lastEventId = String(f.id)
            emit({ type: 'frame', frame: f })
          }
        }
        reconnect()
      } catch {
        // Abandoning this attempt means aborting it. Idempotent on the paths
        // that arrived here already aborted (watchdog, close), and load-bearing
        // on the one that did not: the `!res.ok` throw above leaves a response
        // whose body was never read, and a browser holds that connection open
        // until the body is drained or cancelled — one leaked socket per failed
        // open, forever, against a server already in trouble.
        ctl.abort()
        // Includes the abort that the watchdog and close() raise — they are the
        // same event here, and telling "torn down on purpose" apart from "torn
        // down because it went quiet" is reconnect()'s single `closed` check,
        // not a second copy of it at every call site.
        reconnect()
      }
    })()
  }

  /**
   * Backoff resets on a connection that LASTED, never on one that merely
   * opened.
   *
   * The distinction is the whole point. A server that accepts and immediately
   * closes — a proxy in front of a restarting backend does exactly this —
   * answers 200 every time, so resetting on `res.ok` would hold the delay at
   * BACKOFF_BASE_MS forever and turn the retry loop into a hot loop against a
   * server already in trouble. Resetting on first byte has the same failure
   * one step later, because the opening snapshot arrives before the close.
   * Elapsed time is the only signal that separates "this connection worked"
   * from "this connection was accepted".
   */
  function reconnect(): void {
    if (closed) return
    if (openedAt !== null && now() - openedAt >= STABLE_MS) attempt = 0
    openedAt = null
    // No attempt is in flight until the scheduled connect() issues the next
    // fetch, so the opening deadline must be disarmed for the wait. Without
    // this, the one path that reaches reconnect() with a live controller — the
    // server closing the stream cleanly, where nothing aborted — leaves a stale
    // `attemptStartedAt` that the watchdog reads as an opening that overran,
    // and it aborts the finished connection during the backoff wait.
    attemptStartedAt = null
    emit({ type: 'state', state: 'reconnecting' })
    const delay = Math.min(BACKOFF_BASE_MS * 2 ** attempt, BACKOFF_MAX_MS)
    attempt += 1
    later(connect, delay)
  }

  connect()

  return { close: stop }
}
