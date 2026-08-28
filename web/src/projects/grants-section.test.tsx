import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import type { components } from '../api/schema'
import { renderApp } from '../test/render-app'

type ProjectRow = components['schemas']['ApiProjectResponse']
type GrantRow = components['schemas']['ApiProjectGrantResponse']
type UserRow = components['schemas']['ApiUserResponse']

/**
 * Annotated with the GENERATED row types, so a renamed field on
 * `ProjectGrantResponse` reddens `tsc --noEmit` here rather than arriving at
 * the surface as a missing property.
 *
 * `ApiProjectGrantResponse` is `{email, level, user_id}` — NOT
 * `ApiGrantResponse`, which is `{level, project_id}` and is the shape that
 * rides on `/auth/me`. The two are one word apart and answer opposite
 * questions: this one is "who can reach this project", that one is "which
 * projects can I reach". The plan named a third spelling that exists nowhere.
 */
const project: ProjectRow = {
  id: 'p1',
  name: 'Caesar',
  description: 'the conveyor itself',
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
}

/**
 * THE TWO GRANTS DISAGREE ON BOTH RENDERED PROPERTIES. Their emails differ and
 * their levels differ, so a list that printed one row's level on every row is
 * visible here; a fixture where the two coincide cannot tell a correct list
 * from that mistake.
 */
const viewer: GrantRow = { user_id: 'u2', email: 'member@example.com', level: 'view' }
const approver: GrantRow = { user_id: 'u3', email: 'lead@example.com', level: 'approve' }

const theProject = () => http.get('/api/v1/projects/:id', () => HttpResponse.json(project))
const noRepos = () => http.get('/api/v1/projects/:id/repos', () => HttpResponse.json({ repos: [] }))

const grants = (...rows: GrantRow[]) =>
  http.get('/api/v1/projects/:id/grants', () => HttpResponse.json({ grants: rows }))

const user = (id: string, email: string): UserRow => ({
  id,
  email,
  role: 'member',
  active: true,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
})

const users = (...rows: UserRow[]) =>
  http.get('/api/v1/users', () => HttpResponse.json({ users: rows }))

/**
 * A session that is NOT a global admin, holding one grant on `p1`.
 *
 * The LEVEL is the whole subject of two tests below, so it is a parameter
 * rather than a constant: `manage` is the only level that can read this
 * section, and `approve` is the rank directly beneath it.
 */
const memberWith = (level: string) =>
  http.get('/api/v1/auth/me', () =>
    HttpResponse.json({
      user: {
        id: 'u9',
        email: 'lead@example.com',
        role: 'member',
        active: true,
        created_at: '',
        updated_at: '',
      },
      grants: [{ project_id: 'p1', level }],
    }),
  )

const forbidden = (detail: string) =>
  HttpResponse.json(
    { title: 'Forbidden', status: 403, detail, correlation_id: 'cid-403' },
    { status: 403, headers: { 'content-type': 'application/problem+json' } },
  )

describe('the grants section', () => {
  it('lists each grant as its email and its level', async () => {
    renderApp('/repos/p1', [theProject(), noRepos(), grants(viewer, approver)])

    const list = within(await screen.findByTestId('grant-rows'))
    expect(within(list.getByTestId('grant-u2')).getByText('member@example.com')).toBeInTheDocument()
    expect(within(list.getByTestId('grant-u2')).getByText('view')).toBeInTheDocument()
    expect(within(list.getByTestId('grant-u3')).getByText('lead@example.com')).toBeInTheDocument()
    expect(within(list.getByTestId('grant-u3')).getByText('approve')).toBeInTheDocument()
  })

  it('says the project is unshared when it has no grants', async () => {
    /**
     * NO GRANTS OVERRIDE. `renderApp`'s default answers `{grants: null}` —
     * `GrantListResponse.grants` is required AND nullable because a Go nil
     * slice marshals to null — so this test is what puts that query's `?? []`
     * on a live path. Task 19 added a default every one of its own tests then
     * overrode, and the coalesce it was supposed to exercise sat on no path at
     * all while a comment claimed otherwise.
     */
    renderApp('/repos/p1', [theProject(), noRepos()])

    expect(
      await screen.findByText(/nobody else has been granted access to this project yet/i),
    ).toBeInTheDocument()
  })

  it('offers the four levels of the ladder and PUTs the picked user and level', async () => {
    const put = vi.fn()
    let listed = 0
    renderApp('/repos/p1', [
      theProject(),
      noRepos(),
      users(user('u2', 'member@example.com'), user('u3', 'lead@example.com')),
      http.get('/api/v1/projects/:id/grants', () => {
        listed += 1
        return HttpResponse.json({ grants: listed === 1 ? [] : [approver] })
      }),
      http.put('/api/v1/projects/:id/grants', async ({ request, params }) => {
        put({ projectId: params.id, body: await request.json() })
        return new HttpResponse(null, { status: 204 })
      }),
    ])

    await userEvent.click(await screen.findByRole('button', { name: /^grant access$/i }))
    const sheet = within(await screen.findByRole('dialog'))

    /**
     * THE VOCABULARY IS THE BACKEND'S, and it could not be read where the plan
     * said to read it: `ContractsIdAndSetGrant.level` is a bare `string` in the
     * generated schema, carrying no enum at all. The four values and their
     * ORDER come from `backend/internal/platform/authn/authn.go` — an inclusive
     * ladder ranked view 1 · run 2 · approve 3 · manage 4 — and they are named
     * here rather than counted, because "four options" passes for any four.
     */
    const level = await screen.findByLabelText(/^level$/i)
    expect(
      within(level)
        .getAllByRole('option')
        .map((o) => o.textContent),
    ).toEqual(['Pick a level', 'view', 'run', 'approve', 'manage'])

    await userEvent.selectOptions(await screen.findByLabelText(/^user$/i), 'u3')
    /**
     * `approve`, WHICH IS NOT THE FORM'S DEFAULT. The field starts at `view` —
     * the bottom of the ladder, so an unattended submit grants the least — and
     * selecting that value would leave the assertion below true for a select
     * wired to nothing at all.
     */
    await userEvent.selectOptions(level, 'approve')
    await userEvent.click(sheet.getByRole('button', { name: /^save grant$/i }))

    expect(put).toHaveBeenCalledWith({ projectId: 'p1', body: { user_id: 'u3', level: 'approve' } })
    // The list is REREAD, not patched locally: the row appears only because the
    // second GET answered with it.
    const list = within(await screen.findByTestId('grant-rows'))
    expect(list.getByText('lead@example.com')).toBeInTheDocument()
  })

  it('removes a grant by DELETEing that row’s user, not the first one', async () => {
    const deleted = vi.fn()
    renderApp('/repos/p1', [
      theProject(),
      noRepos(),
      grants(viewer, approver),
      http.delete('/api/v1/projects/:id/grants/:userId', ({ params }) => {
        deleted({ projectId: params.id, userId: params.userId })
        return new HttpResponse(null, { status: 204 })
      }),
    ])

    // THE SECOND ROW, named with its own email. A control named only "Remove"
    // is reachable in a list only by counting siblings, and a test that counts
    // passes against a handler wired to the wrong row.
    await userEvent.click(
      await screen.findByRole('button', { name: /^remove lead@example\.com$/i }),
    )

    expect(deleted).toHaveBeenCalledWith({ projectId: 'p1', userId: 'u3' })
  })

  it('is absent entirely for a member one rank below manage', async () => {
    /**
     * `approve`, NOT `view`, AND THAT IS THE POINT. All three grant endpoints —
     * including the READ — enforce `GrantManage` in `grantScoped`
     * (`backend/internal/identity/api/api.go`), and `manage` is the TOP of the
     * ladder, so "at least manage" and "exactly manage" coincide today. A
     * fixture holding `view` would pass against a gate written one rank too
     * low; the rank directly beneath manage is the only one that cannot.
     *
     * The ORDER of the ladder is pinned separately, by the option list in the
     * PUT test above — `rankOf` indexes the same array the select renders, so
     * an array sorted alphabetically by somebody tidying it would move the
     * boundary to `run` and this fixture would not notice.
     *
     * ABSENT rather than a 403 panel: this member can legitimately see the
     * project, and an error surface on a screen they are allowed to be on
     * reads as a fault rather than as a boundary.
     */
    renderApp('/repos/p1', [theProject(), noRepos(), memberWith('approve')])

    expect(await screen.findByRole('heading', { name: 'Caesar' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: /^grants$/i })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /^grant access$/i })).not.toBeInTheDocument()
  })

  it('degrades the user picker to a user id for a manager who is not an admin', async () => {
    renderApp('/repos/p1', [
      theProject(),
      noRepos(),
      memberWith('manage'),
      grants(),
      http.get('/api/v1/users', () => forbidden('admin role required')),
    ])

    await userEvent.click(await screen.findByRole('button', { name: /^grant access$/i }))

    /**
     * A REAL PERSON'S REAL STATE, not a contrived one: `GET /users` is
     * admin-only (`identity/api/api.go`, `adminOnly`) while managing grants
     * needs only `manage` on the project. So a project manager who is not a
     * global admin can grant access and cannot list the people to grant it to.
     * The honest surface is the id they can still paste, with the reason
     * stated — a bare empty select tells them to wait for a control that will
     * never fill in.
     */
    expect(
      await screen.findByText(/users are listed by admins only — grant by user id instead/i),
    ).toBeInTheDocument()
    expect(screen.getByLabelText(/^user id$/i)).toBeInTheDocument()
    expect(screen.queryByLabelText(/^user$/i)).not.toBeInTheDocument()
  })
})
