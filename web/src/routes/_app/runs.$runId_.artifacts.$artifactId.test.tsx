import { screen } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import { renderApp } from '../../test/render-app'

const AT = '2026-08-26T10:00:00Z'

const PATCH_B64 = btoa('--- a/ROME.md\n+++ b/ROME.md\n@@ -1 +1 @@\n-old\n+new\n')

const artifact = (over: Record<string, unknown> = {}) =>
  http.get('/api/v1/runs/:id/artifacts/:artifactId', ({ params }) =>
    HttpResponse.json({
      id: params.artifactId,
      run_id: params.id,
      node_id: 'impl',
      type: 'patch',
      attempt: 2,
      generation: 3,
      parent_ids: [],
      created_at: AT,
      payload: { diff: PATCH_B64 },
      ...over,
    }),
  )

describe('the artifact view', () => {
  it('renders an artifact reached by deep link, through its kind renderer', async () => {
    // A URL AND NOT A DISCLOSURE: this is the screen a person is sent to when
    // they ask "what did that node actually produce", and it has to work with
    // nothing of this run in the cache. The route fetches the payload itself —
    // `run.artifacts` rows are `ApiArtifactResponse` and carry NO payload
    // (`api/schema.d.ts:745-756`), so the row cannot render itself.
    renderApp('/runs/run-1/artifacts/a7', [artifact()])
    expect(await screen.findByText('ROME.md')).toBeInTheDocument()
  })

  it('heads the screen with what the artifact IS, and links back to its phase', async () => {
    renderApp('/runs/run-1/artifacts/a7', [artifact()])
    const header = await screen.findByTestId('artifact-header')
    // The kind, and the node/attempt/generation that place it in the run. An
    // artifact id alone is not a thing a person can situate.
    expect(header).toHaveTextContent('patch')
    expect(header).toHaveTextContent('impl')
    expect(header).toHaveTextContent(/attempt 2/)
    expect(header).toHaveTextContent(/generation 3/)
    // BACK TO THE PHASE, not to the top of the timeline: `?phase=` is run
    // detail's URL state, so the link returns the reader to the row they left.
    expect(screen.getByRole('link', { name: /back to/i })).toHaveAttribute(
      'href',
      '/runs/run-1?phase=impl',
    )
  })

  it('renders a kind it has no renderer for by name', async () => {
    renderApp('/runs/run-1/artifacts/a7', [artifact({ type: 'sbom', payload: { spdx: 1 } })])
    expect(await screen.findByText(/no renderer/i)).toBeInTheDocument()
  })
})
