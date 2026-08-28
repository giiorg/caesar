import { screen, within } from '@testing-library/react'
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
  it('names the version, shows the whole hash, and holds both slots', async () => {
    renderApp('/workflows/w1/versions/2', [version()])

    expect(await screen.findByRole('heading', { name: 'v2' })).toBeInTheDocument()
    // THE WHOLE HASH HERE. The history list truncates for scanning; this is the
    // page a person opens to check one against another, so a prefix would
    // defeat the reason they came.
    expect(screen.getByText(HASH)).toBeInTheDocument()
    expect(screen.getByTestId('dag-preview-slot')).toBeInTheDocument()
    expect(screen.getByTestId('outline-slot')).toBeInTheDocument()
    // The slots say out loud that they are not built. A bordered empty box
    // reads as a preview that failed to draw.
    expect(screen.getByTestId('dag-preview-slot')).toHaveTextContent(/not built yet/i)
  })

  /**
   * THE ASSERTION THAT THE DOCUMENT ARRIVED, and it is the only one on this
   * page that can make it. Both slots above render identically for every
   * version — a test built on them alone would pass against a page that never
   * fetched anything at all.
   */
  it('renders the node ids out of the fetched document', async () => {
    renderApp('/workflows/w1/versions/2', [version()])

    const ids = within(await screen.findByTestId('node-ids')).getAllByRole('listitem')
    expect(ids.map((li) => li.textContent)).toEqual(['plan', 'implement', 'gate'])
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
   * `Validate` refuses a document with no nodes, so an empty list cannot come
   * off the wire; a page that answered "no steps" here would describe a state
   * the system does not have and send the operator looking for a workflow
   * problem instead of a client one. The `not.toBeInTheDocument` half is what
   * kills a reader that returned `[]` on a miss: the sentence alone would pass
   * beside an empty list.
   */
  it('says a document it cannot read is unreadable, rather than showing no steps', async () => {
    renderApp('/workflows/w1/versions/2', [version({ document: { schema: 99, steps: [] } })])

    expect(
      await screen.findByText(/could not be read by this version of the app/i),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('node-ids')).not.toBeInTheDocument()
  })

  /**
   * A `nodes` THAT IS PRESENT AND NOT A LIST — the shape a presence check lets
   * through, and the fixture is an OBJECT rather than a string on purpose.
   *
   * MEASURED, both ways: with `nodes: 'plan'` the mutant that weakens the guard
   * to `doc.nodes === undefined` SURVIVES, because iterating a string yields
   * characters, `asObject('p')` answers null, and the reader returns null by a
   * different route — the two behaviours are indistinguishable on that input.
   * An object is not iterable at all, so the weakened guard throws inside the
   * render instead of answering, and the mutant dies. A keyed-by-id map is also
   * the plausible wrong shape: it is what someone would write if they had never
   * read `workflows.Document`.
   */
  it('refuses a document whose nodes are not a list', async () => {
    renderApp('/workflows/w1/versions/2', [
      version({ document: { schema: 2, nodes: { plan: { id: 'plan' } } } }),
    ])

    expect(
      await screen.findByText(/could not be read by this version of the app/i),
    ).toBeInTheDocument()
    expect(screen.queryByTestId('node-ids')).not.toBeInTheDocument()
  })

  /**
   * A NODE WITHOUT AN ID MAKES THE WHOLE DOCUMENT UNREADABLE rather than making
   * the graph one node shorter. Dropping it silently would show a person fewer
   * steps than the runner executes — `readPlan`'s call, on the same kind of
   * partially-understood payload.
   */
  it('refuses a document whose nodes are not all identifiable', async () => {
    renderApp('/workflows/w1/versions/2', [
      version({ document: { schema: 2, nodes: [{ id: 'plan' }, { type: 'gate' }] } }),
    ])

    expect(
      await screen.findByText(/could not be read by this version of the app/i),
    ).toBeInTheDocument()
    expect(screen.queryByText('plan')).not.toBeInTheDocument()
  })
})
