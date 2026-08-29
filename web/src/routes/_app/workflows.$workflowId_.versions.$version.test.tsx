import { screen } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { renderApp } from '../../test/render-app'

const HASH = 'abc123abc123deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef0002'

/** A schema-2 document, in the shape `workflows.Document` marshals to. */
const document = {
  schema: 2,
  nodes: [
    { id: 'plan', type: 'plan', harness: 'claude', instructions: 'draft it' },
    { id: 'implement', type: 'implement', harness: 'claude', instructions: 'do it' },
    { id: 'gate', type: 'gate' },
  ],
  edges: [{ from: 'implement', to: 'gate', artifact: 'patch' }],
}

const version = (over: Record<string, unknown> = {}) =>
  http.get('/api/v1/workflows/:id/versions/:version', ({ params }) =>
    HttpResponse.json({
      workflow_id: 'w1',
      version: Number(params.version),
      content_hash: HASH,
      document,
      created_at: '2026-08-21T09:00:00Z',
      ...over,
    }),
  )

describe('/workflows/$workflowId/versions/$version', () => {
  it('names the version, shows the whole hash, and holds both projections', async () => {
    renderApp('/workflows/w1/versions/2', [version()])

    expect(await screen.findByRole('heading', { name: 'v2' })).toBeInTheDocument()
    // THE WHOLE HASH HERE. The history list truncates for scanning; this is the
    // page a person opens to check one against another, so a prefix would
    // defeat the reason they came.
    expect(screen.getByText(HASH)).toBeInTheDocument()
    expect(screen.getByTestId('dag-preview-slot')).toBeInTheDocument()
    // The outline is Task 27's and says out loud that it is not built. A
    // bordered empty box reads as an outline that failed to draw.
    expect(screen.getByTestId('outline-slot')).toHaveTextContent(/not built yet/i)
  })

  /**
   * THE ASSERTION THAT THE DOCUMENT ARRIVED, and it is the only one on this
   * page that can make it. The heading and the hash come off the version's
   * metadata and the outline slot renders identically for every version — a
   * test built on those alone would pass against a page that fetched the
   * metadata and dropped the definition on the floor.
   *
   * It reads the DRAWING, node by node: `data-node-type` is what a pill's
   * colour and shape mean, so this is the same assertion the preview's own
   * suite makes, arrived at through the route, the query and the reader.
   */
  it('draws the fetched document’s steps in the preview', async () => {
    renderApp('/workflows/w1/versions/2', [version()])

    const slot = await screen.findByTestId('dag-preview-slot')
    const pills = [...slot.querySelectorAll('[data-node-type]')]
    expect(pills.map((p) => p.getAttribute('data-node-type'))).toEqual([
      'plan',
      'implement',
      'gate',
    ])
    expect(screen.getByRole('img', { name: '3 steps: plan, implement, gate' })).toBeInTheDocument()
  })

  /**
   * THE VERSION IN THE PATH IS THE VERSION FETCHED — asserted on the REQUEST,
   * because the fixture above echoes `params.version` back and a page that
   * asked for the wrong one would still render a heading matching its own URL.
   */
  it('asks for the version named in the address', async () => {
    const asked = vi.fn()
    renderApp('/workflows/w1/versions/7', [
      http.get('/api/v1/workflows/:id/versions/:version', ({ params }) => {
        asked(params)
        return HttpResponse.json({
          workflow_id: 'w1',
          version: 7,
          content_hash: HASH,
          document,
          created_at: '2026-08-21T09:00:00Z',
        })
      }),
    ])

    expect(await screen.findByRole('heading', { name: 'v7' })).toBeInTheDocument()
    expect(asked).toHaveBeenCalledWith(expect.objectContaining({ id: 'w1', version: '7' }))
    /**
     * ONCE, and the count is what makes the LOADER falsifiable at all.
     * `toHaveBeenCalledWith` passes if ANY call matched, so a loader that
     * prefetched a different version would leave this test green while firing a
     * second request — MEASURED: the mutant pinning the loader to `'1'` survives
     * the line above and dies on this one. The count also pins the thing the
     * prefetch exists for: loader and component must land on ONE cache entry,
     * and a key that disagreed between them would fetch twice.
     */
    expect(asked).toHaveBeenCalledTimes(1)
  })

  /**
   * UNREADABLE IS NOT EMPTY, and the two must not render the same way.
   *
   * `Validate` refuses a document with no nodes, so an empty definition cannot
   * come off the wire; a page that drew an empty picture here would describe a
   * state the system does not have and send an operator looking for a workflow
   * problem instead of a client one. The second and third assertions are what
   * make it bite — the sentence alone would pass beside an empty frame, and the
   * outline stub would go on claiming a version this page could not read.
   *
   * ONE FIXTURE, NOT THE TABLE. Which shapes are unreadable is `readDocument`'s
   * question and `derive.test.ts` asks it fifteen ways against the function
   * itself; what is left for this page to prove is that a `null` answer reaches
   * the screen as this sentence and displaces both projections. Re-running the
   * shape table through a router, a query and a mock server would assert the
   * same thing five times more slowly.
   */
  it('says a document it cannot read is unreadable, and draws nothing', async () => {
    renderApp('/workflows/w1/versions/2', [version({ document: { schema: 99, steps: [] } })])

    expect(
      await screen.findByText(/could not be read by this version of the app/i),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('dag-preview-slot')).not.toBeInTheDocument()
    expect(screen.queryByTestId('outline-slot')).not.toBeInTheDocument()
  })
})
