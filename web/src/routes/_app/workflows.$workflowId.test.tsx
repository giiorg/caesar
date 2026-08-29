import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import type { components } from '../../api/schema'
import { renderApp } from '../../test/render-app'

type WorkflowRow = components['schemas']['ApiWorkflowResponse']
type VersionMeta = components['schemas']['ApiWorkflowVersionMetaResponse']
type RunRow = components['schemas']['ApiRunSummaryResponse']

const conveyor: WorkflowRow = {
  id: 'w1',
  name: 'conveyor',
  description: 'plan → implement → gate',
  latest_version: 2,
  created_at: '2026-08-20T00:00:00Z',
  updated_at: '2026-08-21T00:00:00Z',
}

/**
 * TWO HASHES THAT SHARE THEIR FIRST TWELVE CHARACTERS.
 *
 * The truncation is there so a person can compare two rows at a glance, and a
 * fixture whose hashes differ in their first byte cannot tell a screen that
 * truncates from one that does not — every assertion would pass either way.
 * These force the `title` attribute to be the thing that distinguishes them,
 * which is exactly the claim the rendering makes.
 */
const HASH_2 = 'abc123abc123deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef0002'
const HASH_1 = 'abc123abc123cafebabecafebabecafebabecafebabecafebabecafebabe0001'

const v2: VersionMeta = { version: 2, content_hash: HASH_2, created_at: '2026-08-21T09:00:00Z' }
const v1: VersionMeta = { version: 1, content_hash: HASH_1, created_at: '2026-08-20T09:00:00Z' }

const detail = (over: Partial<WorkflowRow> = {}) =>
  http.get('/api/v1/workflows/:id', () => HttpResponse.json({ ...conveyor, ...over }))

/** Newest first, as `ListVersions` orders it in SQL. */
const versions = (...rows: VersionMeta[]) =>
  http.get('/api/v1/workflows/:id/versions', () => HttpResponse.json({ versions: rows }))

/**
 * A run of this workflow. `id` and `task_title` are what the assertions read;
 * everything else is the projection's required shape.
 *
 * The two fixtures below arrive NEWEST FIRST, as `ListRunsPage` sends them
 * (`ORDER BY r.created_at DESC, r.id DESC`), and their ids sort in the SAME
 * direction as their timestamps — which is what uuidv7 ids really do, coming
 * off a millisecond clock. So the asserted order is descending in both columns
 * and any client re-derivation that is not also descending — an ascending
 * `.sort()` on either, or a bare `.reverse()` — lands in the opposite order.
 */
const run = (id: string, title: string, createdAt: string): RunRow => ({
  id,
  attempt: 1,
  generation: 1,
  created_at: createdAt,
  started_at: null,
  finished_at: null,
  phase_summary: [
    { node_id: 'plan', node_type: 'agent', status: 'succeeded', attempt: 1, generation: 1 },
  ],
  project_id: 'p1',
  project_name: 'Caesar',
  repo_slug: 'giiorg/caesar',
  requester_id: 'u1',
  status: 'running',
  task_id: 't1',
  task_title: title,
  workflow_name: 'conveyor',
})

const newer = run('run-b', 'the newer run', '2026-08-22T00:00:00Z')
const older = run('run-a', 'the older run', '2026-08-20T00:00:00Z')

/**
 * A runs endpoint that ANSWERS PER `workflow_id` and records every filter it
 * was asked for — `repo-activity.test.tsx`'s `runsByRepo`, on the other filter.
 *
 * Keyed on the parameter rather than returning one fixed list, because the
 * property under test is that this section asks for THIS workflow's runs: a
 * handler answering the same rows regardless would pass against a page that
 * sent the version number, another workflow's id, or no filter at all — and
 * "no filter at all" renders the whole deployment's runs under one workflow's
 * heading, which is the failure that looks like success.
 */
const runsByWorkflow = (seen: (id: string | null) => void, rows: Record<string, RunRow[]>) =>
  http.get('/api/v1/runs', ({ request }) => {
    const workflowId = new URL(request.url).searchParams.get('workflow_id')
    seen(workflowId)
    return HttpResponse.json({
      runs: workflowId === null ? [] : (rows[workflowId] ?? []),
      next_cursor: null,
    })
  })

const blockedByRuns = () =>
  HttpResponse.json(
    {
      title: 'Conflict',
      status: 409,
      detail: 'workflow has runs; delete is blocked',
      correlation_id: 'cid-blocked',
    },
    { status: 409, headers: { 'content-type': 'application/problem+json' } },
  )

describe('/workflows/$workflowId', () => {
  it('names the workflow and lists its versions newest first', async () => {
    renderApp('/workflows/w1', [detail(), versions(v2, v1)])

    expect(await screen.findByRole('heading', { name: 'conveyor' })).toBeInTheDocument()
    expect(screen.getByText('plan → implement → gate')).toBeInTheDocument()

    const rows = within(await screen.findByTestId('version-history')).getAllByRole('listitem')
    expect(rows).toHaveLength(2)
    // THE SERVER'S ORDER, KEPT. A client re-sort would be a second opinion
    // about an ordering the server already holds.
    expect(rows[0]).toHaveTextContent('v2')
    expect(rows[1]).toHaveTextContent('v1')
    expect(rows[0]).toHaveTextContent('2026-08-21T09:00:00Z')
    expect(within(rows[0] as HTMLElement).getByRole('link')).toHaveAttribute(
      'href',
      '/workflows/w1/versions/2',
    )
  })

  /**
   * TRUNCATED FOR READING, WHOLE FOR COMPARING — and the two halves have to be
   * asserted separately, because each passes on its own against a screen that
   * gets the other wrong.
   *
   * `toHaveTextContent` with the full hash would pass against an untruncated
   * render; the exact-text query below cannot. The `title` assertion is what
   * stops the whole value being lost rather than merely hidden.
   */
  it('shows twelve characters of the hash and keeps all sixty-four on the title', async () => {
    renderApp('/workflows/w1', [detail(), versions(v2, v1)])

    const rows = within(await screen.findByTestId('version-history')).getAllByRole('listitem')
    const shown = within(rows[0] as HTMLElement).getByText('abc123abc123')
    expect(shown).toHaveAttribute('title', HASH_2)
    // The full hash is nowhere in the visible text of the row.
    expect(rows[0]).not.toHaveTextContent(HASH_2)
  })

  /**
   * THE HALF-CREATED WORKFLOW REACHED BY ADDRESS rather than by its card, which
   * goes straight to the editor. The sentence is distinct from the LIST's two
   * because it is about one workflow rather than about the deployment.
   */
  it('says a workflow with no definition has none, in its own words', async () => {
    // `versions: null`, NOT `[]`. Required and nullable are independent and a Go
    // nil slice marshals to null, so this is a body the server may really send —
    // and it is the ONLY fixture in this file that puts the `?? []` in
    // `workflowVersionsQueryOptions` on a live path. With `[]` here that
    // coalesce is deletable with nothing going red.
    renderApp('/workflows/w1', [
      detail({ latest_version: 0 }),
      http.get('/api/v1/workflows/:id/versions', () => HttpResponse.json({ versions: null })),
    ])

    expect(
      await screen.findByText(/this workflow has a name but no definition/i),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('version-history')).not.toBeInTheDocument()
    // NOT the list's sentence: the two must not converge.
    expect(screen.queryByText(/create the first/i)).not.toBeInTheDocument()
  })

  it('renames through PATCH and shows the new name without a reload', async () => {
    const patched = vi.fn()
    let served = 0
    renderApp('/workflows/w1', [
      // THE DETAIL AS IT CHANGES. A handler answering the same body twice could
      // not tell an invalidation from a component that merely re-rendered — the
      // assertion would pass with `invalidateQueries` deleted.
      http.get('/api/v1/workflows/:id', () => {
        served += 1
        return HttpResponse.json(served === 1 ? conveyor : { ...conveyor, name: 'belt' })
      }),
      versions(v2, v1),
      http.patch('/api/v1/workflows/:id', async ({ request }) => {
        patched(await request.json())
        return HttpResponse.json({ ...conveyor, name: 'belt' })
      }),
    ])

    await userEvent.click(await screen.findByRole('button', { name: /edit workflow/i }))
    const name = await screen.findByLabelText(/^name$/i)
    await userEvent.clear(name)
    await userEvent.type(name, 'belt')
    await userEvent.click(screen.getByRole('button', { name: /^save workflow$/i }))

    expect(patched).toHaveBeenCalledWith({ name: 'belt', description: 'plan → implement → gate' })
    expect(await screen.findByRole('heading', { name: 'belt' })).toBeInTheDocument()
  })

  /**
   * THE REFUSAL IS THE SERVER'S SENTENCE, VERBATIM, and the screen stays put.
   *
   * `DeleteWorkflow` classifies the task→workflow_version RESTRICT as
   * `ClassConflict` "workflow has runs; delete is blocked". A client sentence
   * would state a cause the server did not find — Task 16's rule — and
   * navigating away would take the operator off the version list, which is the
   * next thing they need to look at.
   */
  it('renders a blocked delete verbatim, with its correlation id, and stays on the page', async () => {
    const router = renderApp('/workflows/w1', [
      detail(),
      versions(v2, v1),
      http.delete('/api/v1/workflows/:id', blockedByRuns),
    ])

    await userEvent.click(await screen.findByRole('button', { name: /delete workflow/i }))

    const refusal = await screen.findByRole('alert')
    expect(refusal).toHaveTextContent('workflow has runs; delete is blocked')
    expect(refusal).toHaveTextContent('cid-blocked')
    expect(router.state.location.pathname).toBe('/workflows/w1')
    expect(screen.getByTestId('version-history')).toBeInTheDocument()
  })

  it('leaves for the list when the delete succeeds', async () => {
    const router = renderApp('/workflows/w1', [
      detail(),
      versions(v2, v1),
      http.delete('/api/v1/workflows/:id', () => new HttpResponse(null, { status: 204 })),
    ])

    await userEvent.click(await screen.findByRole('button', { name: /delete workflow/i }))

    await waitFor(() => expect(router.state.location.pathname).toBe('/workflows'))
  })

  /**
   * "NEW VERSION", NOT "EDIT DEFINITION". Versions are immutable — the editor
   * registers the NEXT number rather than changing this one — so a label
   * promising an in-place edit would describe a write the door does not
   * perform. The href is asserted beside it because the wording alone would
   * pass against a link that went nowhere useful.
   */
  it('offers a new version, at the editor address', async () => {
    renderApp('/workflows/w1', [detail(), versions(v2, v1)])

    const link = await screen.findByRole('link', { name: /new version/i })
    expect(link).toHaveAttribute('href', '/workflows/w1/edit')
  })
})

/**
 * The locked page map's "Run history per definition" (`01-app-skeleton.html:75`),
 * deferred by 12b design departure 7 on a missing filter that Task 15 then built.
 */
describe('/workflows/$workflowId — run history', () => {
  it('lists this workflow’s runs, in the server’s order, as the same RunCard the lists use', async () => {
    renderApp('/workflows/w1', [
      detail(),
      versions(v2, v1),
      runsByWorkflow(vi.fn(), { w1: [newer, older] }),
    ])

    /**
     * `RunCard` ITSELF, found by the testid it already carries, rather than a
     * lookalike built here — the plan says `RunCard` rows, and the card is what
     * carries the phase dots and the run's own status. The href is the half a
     * wrapper rendering only the title would fail.
     *
     * NOT `getAllByRole('listitem')`, which the version list above can use and
     * this one cannot: `RunCard` nests `PhaseDots`, itself a `<ul>` of `<li>`,
     * so that query answers four rows for two runs. MEASURED, not guessed —
     * the first draft of this test asserted a length of 2 and got 4.
     */
    const rows = within(await screen.findByTestId('run-history')).getAllByTestId('run-card')
    expect(rows).toHaveLength(2)
    // THE SERVER'S ORDER, KEPT — `ListRunsPage` is "one page of the run list,
    // newest first". A client re-sort would be a second opinion about an
    // ordering the server already holds, and this is the same rule the version
    // list above states.
    expect(rows[0]).toHaveTextContent('the newer run')
    expect(rows[1]).toHaveTextContent('the older run')

    const first = rows[0] as HTMLElement
    expect(first).toHaveAttribute('href', '/runs/run-b')
    expect(within(first).getByText(/running/)).toBeInTheDocument()
  })

  /**
   * THE WORKFLOW, NOT A VERSION OF IT AND NOT NOTHING.
   *
   * `runsQueryOptions`'s `workflowId` resolves server-side through a
   * `workflow_versions` subquery (`queries.sql`, `ListRunsPage`), so a task
   * pinned to v1 is still a run of this workflow — which is why the id and not
   * `latest_version` is what goes on the wire. Both wrong answers are
   * observable here: `'2'` would be the version, `null` the unfiltered list.
   */
  it('asks for this workflow’s runs and not for the whole deployment’s', async () => {
    const seen = vi.fn()
    renderApp('/workflows/w1', [detail(), versions(v2, v1), runsByWorkflow(seen, { w1: [newer] })])

    expect(await screen.findByText('the newer run')).toBeInTheDocument()
    expect(seen).toHaveBeenCalledWith('w1')
    expect(seen).not.toHaveBeenCalledWith(null)
    expect(seen).not.toHaveBeenCalledWith('2')
  })

  it('says a workflow has never run rather than showing an empty space', async () => {
    renderApp('/workflows/w1', [detail(), versions(v2, v1), runsByWorkflow(vi.fn(), {})])

    expect(await screen.findByText(/no runs have used this workflow/i)).toBeInTheDocument()
    expect(screen.queryByTestId('run-history')).not.toBeInTheDocument()
    // NOT the runs list's sentence and NOT the repo activity's: "No runs yet."
    // is a claim about the whole deployment, and this screen does not know it.
    expect(screen.queryByText(/no runs yet/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/touched this repo/i)).not.toBeInTheDocument()
    // The versions are still there — an empty run history says nothing about
    // whether the workflow has a definition.
    expect(screen.getByTestId('version-history')).toBeInTheDocument()
  })

  /**
   * A HISTORY NOT YET READ IS NOT AN EMPTY ONE.
   *
   * FOUND BY MUTATION, not by design: collapsing the pending branch into the
   * empty one (`(runs.data ?? []).length === 0`) left every other test in this
   * file green, because they all wait for the answer. What it changes is the
   * moment before it arrives, where the screen would tell an operator "No runs
   * have used this workflow." without knowing — the same lie `index.tsx`
   * refuses to tell about the deployment when the run list fails.
   *
   * The handler is HELD OPEN by a promise this test resolves, rather than
   * delayed by a number of milliseconds: a timing race would make the assertion
   * pass or fail on machine speed. Resolving it afterwards is the second half —
   * without it the test could not tell a pending state from a stuck one.
   */
  it('does not claim a workflow has never run while it is still asking', async () => {
    let release!: () => void
    const held = new Promise<void>((resolve) => {
      release = resolve
    })

    renderApp('/workflows/w1', [
      detail(),
      versions(v2, v1),
      http.get('/api/v1/runs', async () => {
        await held
        return HttpResponse.json({ runs: [], next_cursor: null })
      }),
    ])

    expect(await screen.findByText(/reading the run history/i)).toBeInTheDocument()
    expect(screen.queryByText(/no runs have used this workflow/i)).not.toBeInTheDocument()

    release()

    expect(await screen.findByText(/no runs have used this workflow/i)).toBeInTheDocument()
    expect(screen.queryByText(/reading the run history/i)).not.toBeInTheDocument()
  })

  /**
   * A SECTION THAT FAILS MUST NOT TAKE THE PAGE WITH IT, which is the whole
   * reason this read is a `useQuery` beside the two the loader holds.
   *
   * The workflow and its versions are the page's IDENTITY — a 404 on either
   * means the address is wrong and the screen has nothing to say. The run
   * history is one section of it, and folding it into the loader's
   * `Promise.all` would let a 500 on `/runs` blank the workflow's name, its
   * version list, and the delete refusal an operator may be standing on. This
   * is the assertion that kills that mutant: with the read moved into the
   * loader, `ApiErrorPanel` replaces the whole route and the heading is gone.
   */
  it('keeps the workflow up when its run history cannot be read', async () => {
    renderApp('/workflows/w1', [
      detail(),
      versions(v2, v1),
      http.get('/api/v1/runs', () =>
        HttpResponse.json(
          { title: 'Internal error', status: 500, correlation_id: 'cid-runs' },
          { status: 500, headers: { 'content-type': 'application/problem+json' } },
        ),
      ),
    ])

    expect(await screen.findByText('cid-runs', {}, { timeout: 8_000 })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'conveyor' })).toBeInTheDocument()
    expect(screen.getByTestId('version-history')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /new version/i })).toBeInTheDocument()
    // Not the empty state: a history that could not be read is not a history
    // that is empty, and only one of the two is a fact about this workflow.
    expect(screen.queryByText(/no runs have used this workflow/i)).not.toBeInTheDocument()
  }, 15_000)
})
