import { screen, waitFor } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import { queryClient } from '../api/query-client'
import { onTheWire } from '../runs/on-the-wire.testkit'
import type { Run } from '../runs/run-query'
import { runsQueryOptions } from '../runs/runs-query'
import { tasksQueryOptions } from '../tasks/tasks-query'
import { renderApp } from '../test/render-app'
import { createCacheBridge } from './cache-bridge'
import { fakeTimer } from './fake-timer.testkit'
import type { EventFrame } from './sse-connection'

/**
 * WHAT A BURST COSTS IN HTTP REQUESTS, counted at the resolver.
 *
 * The bridge's own suite asserts which keys it invalidates, and that would pass
 * against the OLD bridge for most of what matters here: an invalidation is not
 * a refetch, and a refetch is what the operator's bandwidth and the server's
 * load are made of. So these two scenarios count MSW resolver calls per
 * endpoint, through the real router, the real query client and the real
 * queries — with per-endpoint EQUALITIES, never ceilings. An upper bound alone
 * is satisfied by zero, and zero is exactly what an unobserved query produces.
 *
 * THE BRIDGE IS CONSTRUCTED HERE rather than driven through `scriptedStream`,
 * and that is forced. `useAttentionStream` builds its own bridge with no options
 * (`use-attention-stream.ts`), so a frame written on a scripted stream lands in
 * an instance whose 250ms window and 5s throttle are REAL — and worse, that
 * instance would then fire into this same `queryClient` at an uncontrolled
 * moment and add requests nobody counted. Instead the default `silentStream`
 * keeps the production bridge asleep, this file's bridge invalidates the same
 * singleton cache the router is mounted on, and both of its timers are
 * injected. What that gives up — the wiring from transport to bridge — is held
 * end to end elsewhere, by tests that drive a real frame down a real stream to a
 * rendered DOM change (`runs.$runId.test.tsx`, `index.test.tsx`,
 * `tasks.test.tsx`, `approvals.test.tsx`).
 */

const AT = '2026-08-20T00:00:00Z'
const WINDOW = 10
const HOLD = 1_000

const baseRun = (): Run => ({
  id: 'r1',
  task_id: 't1',
  task_title: 'Add revocation',
  repo_slug: 'caesar/e2e',
  workflow_name: 'e2e',
  status: 'running',
  definition_hash: 'h1',
  base_sha: 'abc123',
  policy_snapshot: {},
  generation: 1,
  execution_used_ms: 62_000,
  wall_deadline: '2099-01-01T00:00:00Z',
  created_at: AT,
  started_at: AT,
  finished_at: null,
  artifacts: null,
  publications: null,
  phases: [
    {
      node_id: 'impl',
      attempt: 1,
      max_attempts: 3,
      status: 'running',
      generation: 1,
      created_at: AT,
      finished_at: null,
    },
  ],
})

const frame = (id: number, kind: string, runId = 'r1'): EventFrame => ({
  kind: 'event',
  id,
  event: kind,
  frame: { id, run_id: runId, kind, payload: {}, occurred_at: '' },
})

type Counts = {
  run: number
  events: number
  runs: number
  tasks: number
  approvals: number
  me: number
}

/**
 * The six endpoints a burst could possibly touch, each counting its own
 * resolver. `me` is here because pre-flight 1 measured the `/auth/me` drumbeat
 * to be the 60s refresh floor and NOTHING else — a stream burst added zero — and
 * a zero that is asserted is the only kind that stays zero.
 */
function counted(): { n: Counts; handlers: ReturnType<typeof http.get>[]; zero: () => void } {
  const n: Counts = { run: 0, events: 0, runs: 0, tasks: 0, approvals: 0, me: 0 }
  return {
    n,
    zero: () => {
      n.run = 0
      n.events = 0
      n.runs = 0
      n.tasks = 0
      n.approvals = 0
      n.me = 0
    },
    handlers: [
      http.get('/api/v1/runs/r1', () => {
        n.run += 1
        return HttpResponse.json(onTheWire(baseRun()))
      }),
      http.get('/api/v1/runs/:id/events', () => {
        n.events += 1
        return HttpResponse.json({ events: null })
      }),
      http.get('/api/v1/runs', () => {
        n.runs += 1
        return HttpResponse.json({ runs: null, next_cursor: null })
      }),
      http.get('/api/v1/tasks', () => {
        n.tasks += 1
        return HttpResponse.json({ tasks: null })
      }),
      http.get('/api/v1/approvals', () => {
        n.approvals += 1
        return HttpResponse.json({ approvals: [] })
      }),
      http.get('/api/v1/auth/me', () => {
        n.me += 1
        return HttpResponse.json({
          user: {
            id: 'u1',
            email: 'user@example.com',
            role: 'admin',
            active: true,
            created_at: '',
            updated_at: '',
          },
          grants: [{ project_id: 'p1', level: 'run' }],
        })
      }),
    ],
  }
}

/**
 * `/runs/r1` with the two collections mounted alongside it.
 *
 * The route is run detail because it is the only one that observes BOTH the run
 * DTO and the event log, which are two of the five endpoints under measurement.
 * It observes neither `['runs']` nor `['tasks']` — MEASURED, `{runs:0, tasks:0}`
 * after first paint — so those get real observers through the harness seam, and
 * `['approvals']` needs none because `AppLayout`'s badge already watches it.
 *
 * Every counter is zeroed AFTER the first paint has settled: the baseline loads
 * are not what is being measured.
 */
async function mounted() {
  const c = counted()
  renderApp('/runs/r1', c.handlers, { observe: [runsQueryOptions(), tasksQueryOptions] })
  await screen.findByTestId('phase-timeline')
  await waitFor(() => {
    expect(c.n.run).toBeGreaterThan(0)
    expect(c.n.events).toBeGreaterThan(0)
    expect(c.n.runs).toBeGreaterThan(0)
    expect(c.n.tasks).toBeGreaterThan(0)
  })
  c.zero()
  const t = fakeTimer()
  const b = createCacheBridge(queryClient, {
    debounceMs: WINDOW,
    throttleMs: HOLD,
    setTimeoutImpl: t.impl,
  })
  return { n: c.n, t, b }
}

/** Ten windows of four `phase.*` frames, each awaited so the count is the window's. */
async function tenPhaseWindows(
  b: { onFrame: (f: EventFrame) => void },
  t: { runOnly: (ms: number) => void },
  n: Counts,
) {
  for (let w = 0; w < 10; w++) {
    for (let i = 0; i < 4; i++) b.onFrame(frame(w * 4 + i, 'phase.started'))
    t.runOnly(WINDOW)
    await waitFor(() => expect(n.run).toBe(w + 1))
  }
}

describe('what a phase burst costs in requests', () => {
  it('Scenario A — the drumbeat collapses, and the trailing refresh really lands', async () => {
    const { n, t, b } = await mounted()

    await tenPhaseWindows(b, t, n)
    // Forty frames in, and the collections have not been asked for ONCE. The old
    // bridge asked ten times each, plus ten event logs on the run key's prefix.
    expect({ runs: n.runs, tasks: n.tasks, events: n.events }).toEqual({
      runs: 0,
      tasks: 0,
      events: 0,
    })

    t.runOnly(HOLD)
    await waitFor(() => expect(n.runs).toBe(1))

    b.onFrame(frame(100, 'run.succeeded'))
    t.runOnly(WINDOW)
    await waitFor(() => expect(n.run).toBe(11))
    await waitFor(() => expect(n.runs).toBe(2))

    expect(n).toEqual({
      // TWO: the trailing throttle refresh, then the lifecycle window. The first
      // of those is the LIVENESS half and the reason this is an equality rather
      // than a `<=` — the runs list really was refetched inside the burst, which
      // is what keeps Home's phase dots advancing (decisions §7).
      runs: 2,
      tasks: 2,
      // ONE: the lifecycle window only. `run.succeeded` is an attention kind;
      // forty phase frames are not, and the inbox does not change when a phase
      // ticks.
      approvals: 1,
      // TWO, against ~10 under the old bridge: the log rode `['run', id]`'s
      // prefix and refetched once per window.
      events: 2,
      // ELEVEN — one per window. The run DTO is the tier that stays per-window,
      // because it is what the open screen is drawn from.
      run: 11,
      // ZERO. Pre-flight 1 measured the /auth/me drumbeat to be the 60s refresh
      // floor and nothing else; a burst must not add to it.
      me: 0,
    })
  })

  it('Scenario B — a lifecycle window absorbs the pending hold, and the orphan says nothing', async () => {
    const { n, t, b } = await mounted()

    await tenPhaseWindows(b, t, n)
    // The same ten windows as A, but the lifecycle event arrives BEFORE the
    // throttle fires. It drains the held set, so the collections are refetched
    // ONCE in total rather than once per tier.
    b.onFrame(frame(100, 'run.succeeded'))
    t.runOnly(WINDOW)
    await waitFor(() => expect(n.run).toBe(11))
    await waitFor(() => expect(n.runs).toBe(1))

    const settled = { ...n }
    expect(settled).toEqual({ runs: 1, tasks: 1, approvals: 1, events: 1, run: 11, me: 0 })

    // THE WHOLE POINT. The throttle timer is still pending and cannot be
    // cancelled through the `setTimeoutImpl` seam — a real browser would fire it
    // a few seconds after the run finished. The empty-hold return is the only
    // thing between that and a second, pointless collection refetch.
    t.runOnly(HOLD)
    await new Promise((r) => setTimeout(r, 50))
    expect(n).toEqual(settled)
  })
})
