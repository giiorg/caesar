import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { RunSummary } from '../../runs/runs-query'
import { renderApp, scriptedStream } from '../../test/render-app'

const AT = '2026-08-20T00:00:00Z'

const runRow = (over: Partial<RunSummary> & Pick<RunSummary, 'id'>): RunSummary => ({
  task_id: 't1',
  task_title: 'a run',
  project_id: 'p1',
  project_name: 'caesar',
  repo_slug: 'acme/app',
  workflow_name: 'default',
  status: 'running',
  attempt: 1,
  generation: 1,
  requester_id: 'u1',
  created_at: AT,
  started_at: AT,
  finished_at: null,
  phase_summary: [
    { node_id: 'build', node_type: 'implement', status: 'succeeded', attempt: 1, generation: 1 },
  ],
  ...over,
})

/**
 * `GET /runs`, ANSWERING THE `status` QUERY the way the server does.
 *
 * A handler that ignored the parameter would let the filter tests pass against
 * a client that never sent one — the exact defect they exist to catch. The
 * filtering happens here rather than in a fixture list so that a request
 * carrying no `status` is visibly a different request from one carrying
 * `status=running`.
 */
const runs = (rows: RunSummary[] | (() => RunSummary[])) =>
  http.get('/api/v1/runs', ({ request }) => {
    const status = new URL(request.url).searchParams.get('status')
    const all = typeof rows === 'function' ? rows() : rows
    return HttpResponse.json({
      runs: status === null ? all : all.filter((r) => r.status === status),
      next_cursor: null,
    })
  })

const twoStatuses = (): RunSummary[] => [
  runRow({ id: 'run-1', task_title: 'still going', status: 'running' }),
  runRow({ id: 'run-2', task_title: 'broke', status: 'failed', finished_at: AT }),
]

describe('the runs list', () => {
  it('lists every run as a card', async () => {
    renderApp('/runs', [runs(twoStatuses())])
    const list = await screen.findByTestId('run-list')
    // BY TESTID, NOT BY ROLE. Each card nests its own <ul> of phase dots, so
    // `getAllByRole('listitem')` inside this list counts dots as rows — it
    // reported 4 for these two runs.
    expect(within(list).getAllByTestId('run-card')).toHaveLength(2)
    expect(within(list).getByText('still going')).toBeInTheDocument()
    expect(within(list).getByText('broke')).toBeInTheDocument()
  })

  it('reads its filter from the URL, and asks the SERVER for it', async () => {
    // URL-AS-STATE, and the half that a React-state filter would fake: the
    // parameter has to reach `runsQueryOptions`, which puts it on the wire and
    // into the query key. The handler filters by what it was sent, so a client
    // that dropped the parameter would render both rows here.
    renderApp('/runs?status=failed', [runs(twoStatuses())])
    const list = await screen.findByTestId('run-list')
    expect(within(list).getAllByTestId('run-card')).toHaveLength(1)
    expect(within(list).getByText('broke')).toBeInTheDocument()
  })

  it('writes a picked filter into the URL, so the view can be sent to somebody else', async () => {
    const router = renderApp('/runs', [runs(twoStatuses())])
    await screen.findByTestId('run-list')
    await userEvent.click(screen.getByRole('link', { name: /^running$/i }))
    await waitFor(() => expect(router.state.location.search).toMatchObject({ status: 'running' }))
    await waitFor(() =>
      expect(within(screen.getByTestId('run-list')).getAllByTestId('run-card')).toHaveLength(1),
    )
  })

  it('marks which filter is in force — exactly one of them', async () => {
    renderApp('/runs?status=failed', [runs(twoStatuses())])
    const filters = await screen.findByTestId('status-filters')
    const current = within(filters)
      .getAllByRole('listitem')
      .filter((li) => li.getAttribute('data-current') === 'true')
    expect(current.map((li) => li.getAttribute('data-status'))).toEqual(['failed'])
    // AND THE ACCESSIBILITY TREE AGREES, which is the assertion that pins
    // `activeOptions={{ exact: true }}` on the "All" chip. Without it the
    // router's `includeSearch` compares a link's search as a SUBSET of the
    // address bar's, and `{}` is a subset of every filter — so "All" would
    // carry `aria-current="page"` on every filtered URL while the visible
    // highlight sat on the right chip. Two answers to one question, and only
    // a screen reader would ever see the wrong one.
    const marked = within(filters)
      .getAllByRole('link')
      .filter((a) => a.getAttribute('aria-current') === 'page')
    expect(marked.map((a) => a.textContent)).toEqual(['failed'])
  })

  it('reads a search value that is not even a string as no filter at all', async () => {
    // TanStack Router's default search parser runs JSON.parse over each value
    // and keeps the raw string only when that throws — so `?status=failed`
    // reaches `validateSearch` as a string while THIS one reaches it as the
    // NUMBER 42, which `z.string()` rejects. `parse` would throw there and take
    // the whole screen down over a mistyped link; `safeParse` reads it as no
    // filter, which is this list unfiltered.
    renderApp('/runs?status=42', [runs(twoStatuses())])
    expect(within(await screen.findByTestId('run-list')).getAllByTestId('run-card')).toHaveLength(2)
    expect(
      within(screen.getByTestId('status-filters'))
        .getAllByRole('listitem')
        .filter((li) => li.getAttribute('data-current') === 'true')
        .map((li) => li.getAttribute('data-status')),
    ).toEqual(['all'])
  })

  it("shows a status the server rejects as the server's own refusal", async () => {
    // A status this endpoint does not know is NOT clamped to "no filter" here.
    // `runlist.go:151` answers with a 400 naming the field, and showing every
    // run to somebody who asked for one kind is the failure that looks like
    // success. The correlation id is the constitution's requirement on every
    // error surface, and this is the one this route can produce.
    renderApp('/runs?status=nonsense', [
      http.get('/api/v1/runs', () =>
        HttpResponse.json(
          {
            title: 'validation failed',
            status: 400,
            correlation_id: 'cid-status',
            errors: [{ field: 'status', message: 'not a run status' }],
          },
          { status: 400, headers: { 'content-type': 'application/problem+json' } },
        ),
      ),
    ])
    expect(await screen.findByText('cid-status')).toBeInTheDocument()
  })

  // THE TWO EMPTY STATES, AS TWO TESTS. `renderApp` mounts a fresh tree without
  // unmounting the last one — RTL cleans up between TESTS (`src/test/setup.ts:29`)
  // and not between renders — so asserting the second state's absence in the
  // same `it` as the first reads the first tree still on screen. Measured: the
  // `queryByText(/no runs yet/i)` line failed against a correct component.
  it('says the system has run nothing, when nothing is filtered', async () => {
    renderApp('/runs', [runs([])])
    expect(await screen.findByText(/no runs yet/i)).toBeInTheDocument()
  })

  it('names the FILTER when a filter is what emptied the list', async () => {
    // "No runs yet" told to somebody who arrived on `?status=failed` is false —
    // it says the system has never run anything when what happened is that
    // nothing matched, and it hides a filter they may not have chosen.
    renderApp('/runs?status=failed', [runs([])])
    expect(await screen.findByText(/no runs have status "failed"/i)).toBeInTheDocument()
    expect(screen.queryByText(/no runs yet/i)).toBeNull()
  })

  it('refreshes a FILTERED list when a frame arrives', async () => {
    // THE PREFIX MATCH AT ITS LOAD-BEARING WIDTH. `cache-bridge.ts` invalidates
    // the bare `['runs']` and this screen mounts `['runs', {status:'running'}]`
    // — a key that is only reached because the filters live in the SECOND key
    // element. Home's version of this test covers `['runs', {}]`, which a
    // respelling to `['runs:waiting']` would break too; this one is the case
    // the comment in `runs-query.ts` actually names.
    //
    // A LIFECYCLE KIND, since Task 12, and the change is deliberate. This test's
    // subject is the prefix WIDTH, which the frame's kind has nothing to do
    // with; `run.started` reaches the list on the immediate tier and keeps the
    // test at a few hundred milliseconds. The throttled tier's own real-clock
    // proof is Home's version of this test, which pays 5.3 seconds for it once
    // rather than twice.
    const stream = scriptedStream()
    let rows = [runRow({ id: 'run-1', task_title: 'still going', status: 'running' })]
    renderApp('/runs?status=running', [runs(() => rows), stream.handler])
    expect(within(await screen.findByTestId('run-list')).getAllByTestId('run-card')).toHaveLength(1)
    rows = [...rows, runRow({ id: 'run-9', task_title: 'just started', status: 'running' })]
    await stream.emit({ kind: 'run.started', run_id: 'run-9' })
    await waitFor(
      () =>
        expect(within(screen.getByTestId('run-list')).getAllByTestId('run-card')).toHaveLength(2),
      { timeout: 5_000 },
    )
  })
})
