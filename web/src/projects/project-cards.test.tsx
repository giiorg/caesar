import { screen, within } from '@testing-library/react'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { components } from '../api/schema'
import { authenticatedMe, renderApp } from '../test/render-app'

type ProjectRow = components['schemas']['ApiProjectResponse']

/**
 * Annotated `ProjectRow` so a field added to or renamed on `ProjectResponse`
 * reddens `tsc --noEmit` here rather than arriving at the surface as a missing
 * property — the discipline `approvals.test.tsx` applies to `Run` and
 * `decision-history.test.tsx` to `DecisionRow`.
 *
 * The second project's description is EMPTY, which is the common case the wire
 * really carries: `CreateProject` accepts an empty description and stores it.
 * A fixture where both rows are described cannot tell the conditional
 * paragraph from an unconditional one.
 */
const described: ProjectRow = {
  id: 'p1',
  name: 'Caesar',
  description: 'the conveyor itself',
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
}

const bare: ProjectRow = {
  id: 'p2',
  name: 'Scratch',
  description: '',
  created_at: '2026-08-02T00:00:00Z',
  updated_at: '2026-08-02T00:00:00Z',
}

const projects = (...rows: ProjectRow[]) =>
  http.get('/api/v1/projects', () => HttpResponse.json({ projects: rows }))

/** A signed-in session that is NOT an admin — the other half of the empty pair. */
const memberSession = () =>
  http.get('/api/v1/auth/me', () =>
    HttpResponse.json({
      user: {
        id: 'u2',
        email: 'member@example.com',
        role: 'member',
        active: true,
        created_at: '',
        updated_at: '',
      },
      grants: [{ project_id: 'p1', level: 'view' }],
    }),
  )

describe('the project cards', () => {
  it('renders a card per project, and only describes the ones with a description', async () => {
    renderApp('/repos', [projects(described, bare)])

    const rows = within(await screen.findByTestId('project-cards')).getAllByRole('listitem')
    expect(rows).toHaveLength(2)
    expect(rows[0]).toHaveTextContent('Caesar')
    expect(rows[0]).toHaveTextContent('the conveyor itself')
    expect(rows[1]).toHaveTextContent('Scratch')
    // COUNTED, NOT MATCHED. Deleting the `description !== ''` guard renders an
    // empty <p> under "Scratch", which every getByText in this file is blind
    // to — the element is there and carries no text. The paragraph count is
    // what sees it: three with the guard, four without.
    expect(within(rows[1] as HTMLElement).queryAllByRole('paragraph')).toHaveLength(1)
    expect(within(rows[0] as HTMLElement).queryAllByRole('paragraph')).toHaveLength(2)
  })

  it('makes the whole card a link to that project', async () => {
    renderApp('/repos', [projects(described)])

    const card = within(await screen.findByTestId('project-cards')).getByRole('link')
    // The ID IS IN THE HREF, and asserting the whole string is what catches a
    // link pointed at the list it is already on. `/repos` is a prefix of
    // `/repos/p1`, so a containment check would pass for both.
    expect(card).toHaveAttribute('href', '/repos/p1')
    expect(card).toHaveTextContent('Caesar')
  })

  it('tells an admin to create the first project when there are none', async () => {
    /**
     * NO `/api/v1/projects` OVERRIDE, DELIBERATELY. The harness default answers
     * `{projects: null}` — the body a Go nil slice really marshals to — so this
     * case is what puts `projectsQueryOptions`'s `?? []` on a live path.
     * MEASURED: with the coalesce removed this test dies and the other three in
     * this file, which all supply their own array, do not. `authenticatedMe` is
     * named rather than left implicit because the admin half of the pair is the
     * subject here.
     */
    renderApp('/repos', [http.get('/api/v1/auth/me', authenticatedMe)])

    expect(await screen.findByText(/no projects yet — create the first/i)).toBeInTheDocument()
    expect(screen.queryByTestId('project-cards')).not.toBeInTheDocument()
  })

  it('tells a member with no grants that nothing is shared, and offers no create action', async () => {
    /**
     * THE TWO SENTENCES ARE NOT INTERCHANGEABLE and the role is the exact
     * discriminator, not an approximation: `handleListProjects` gives an admin
     * every project and everyone else exactly their granted ones, and
     * `POST /projects` is admin-only. So this member's empty list means nothing
     * has been shared — and telling them to "create the first" would send them
     * to a form the API answers 403 to.
     */
    renderApp('/repos', [projects(), memberSession()])

    expect(await screen.findByText(/no projects are shared with you yet/i)).toBeInTheDocument()
    expect(screen.queryByText(/create the first/i)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /new project/i })).not.toBeInTheDocument()
  })
})
