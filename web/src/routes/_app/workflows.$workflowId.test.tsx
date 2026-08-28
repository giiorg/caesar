import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import type { components } from '../../api/schema'
import { renderApp } from '../../test/render-app'

type WorkflowRow = components['schemas']['ApiWorkflowResponse']
type VersionMeta = components['schemas']['ApiWorkflowVersionMetaResponse']

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
