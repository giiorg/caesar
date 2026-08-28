import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import type { components } from '../../api/schema'
import { renderApp } from '../../test/render-app'

type ProjectRow = components['schemas']['ApiProjectResponse']

const made: ProjectRow = {
  id: 'p9',
  name: 'Ledger',
  description: 'the second one',
  created_at: '2026-08-03T00:00:00Z',
  updated_at: '2026-08-03T00:00:00Z',
}

/**
 * The list AS IT CHANGES: empty on the first read, holding the new project on
 * every read after it.
 *
 * A handler that answered the same body twice could not tell an invalidation
 * from a component that simply re-rendered its existing data — the assertion
 * would pass with `invalidateQueries` deleted. Making the SECOND read differ is
 * what puts the invalidation on the only path to the new row.
 */
const listThatGrows = () => {
  let served = 0
  return http.get('/api/v1/projects', () => {
    served += 1
    return HttpResponse.json({ projects: served === 1 ? [] : [made] })
  })
}

const nameTaken = () =>
  HttpResponse.json(
    {
      title: 'Conflict',
      status: 409,
      detail: 'project name already taken',
      correlation_id: 'cid-dup',
    },
    { status: 409, headers: { 'content-type': 'application/problem+json' } },
  )

const openNewProject = async () => {
  await userEvent.click(await screen.findByRole('button', { name: /new project/i }))
  await userEvent.type(await screen.findByLabelText(/^name$/i), 'Ledger')
  await userEvent.type(screen.getByLabelText(/^description$/i), 'the second one')
}

describe('/repos', () => {
  it('creates a project through the sheet, then shows it in the list', async () => {
    const created = vi.fn()
    renderApp('/repos', [
      listThatGrows(),
      http.post('/api/v1/projects', async ({ request }) => {
        created(await request.json())
        return HttpResponse.json(made, { status: 201 })
      }),
    ])

    expect(await screen.findByText(/no projects yet/i)).toBeInTheDocument()
    await openNewProject()
    await userEvent.click(screen.getByRole('button', { name: /^create project$/i }))

    // THE BODY, not just that a request happened: a form that dropped
    // `description` would still POST and still refresh the list.
    expect(created).toHaveBeenCalledWith({ name: 'Ledger', description: 'the second one' })
    const rows = within(await screen.findByTestId('project-cards')).getAllByRole('listitem')
    expect(rows).toHaveLength(1)
    expect(rows[0]).toHaveTextContent('Ledger')
  })

  it('puts a duplicate-name conflict in the form banner, with its correlation id', async () => {
    renderApp('/repos', [
      http.get('/api/v1/projects', () => HttpResponse.json({ projects: [] })),
      http.post('/api/v1/projects', nameTaken),
    ])

    await openNewProject()
    await userEvent.click(screen.getByRole('button', { name: /^create project$/i }))

    /**
     * THE BANNER IS THE ONLY PLACE THIS CAN LAND, and that is the server's
     * shape rather than a client choice: `CreateProject` raises
     * `xerrors.E(ClassConflict, "project name already taken")` with NO
     * `WithFields` (`assets/projects.go`), so the problem document carries no
     * `errors[]` and `applyProblemToForm` takes its no-fields arm. A client
     * that routed this onto `name` would be inventing an attribution the
     * server never made.
     */
    const banner = await screen.findByRole('alert')
    expect(banner).toHaveTextContent('project name already taken')
    expect(banner).toHaveTextContent('cid-dup')
    // The field is untouched: no message under the input, and the input is not
    // marked invalid.
    expect(screen.getByLabelText(/^name$/i)).toHaveAttribute('aria-invalid', 'false')
  })
})
