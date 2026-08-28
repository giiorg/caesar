import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import type { components } from '../../api/schema'
import { renderApp } from '../../test/render-app'

type ProjectRow = components['schemas']['ApiProjectResponse']
type RepoRow = components['schemas']['ApiRepoResponse']
type CredentialRow = components['schemas']['ApiCredentialResponse']

const project: ProjectRow = {
  id: 'p1',
  name: 'Caesar',
  description: 'the conveyor itself',
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
}

/**
 * THE TWO REPOS DISAGREE ON BOTH RENDERED PROPERTIES. Their forges differ and
 * their default branches differ, so a projection that printed one row's forge
 * on every row — or hard-coded `main` — is visible here. A fixture where the
 * two coincide cannot tell a correct table from either mistake.
 */
const primary: RepoRow = {
  id: 'r1',
  project_id: 'p1',
  forge: 'github',
  slug: 'giiorg/caesar',
  default_branch: 'main',
  credential_id: 'c-forge',
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
}

const secondary: RepoRow = {
  id: 'r2',
  project_id: 'p1',
  forge: 'gitlab',
  slug: 'group/sub/proj',
  default_branch: 'develop',
  credential_id: 'c-forge',
  created_at: '2026-08-02T00:00:00Z',
  updated_at: '2026-08-02T00:00:00Z',
}

const credential = (id: string, kind: string, name: string): CredentialRow => ({
  id,
  kind,
  name,
  key_id: 'k1',
  fingerprint: null,
  secret_version: 1,
  provided_at: '2026-08-01T00:00:00Z',
  last_used_at: null,
  last_auth_expired_at: null,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
})

/**
 * BOTH KINDS ON THE WIRE, which is what every real deployment holds: the
 * milestone walk takes two credentials — a repo binds a `forge_token`, a
 * harness binds a `model_key` — so a picker that forgot to filter has a wrong
 * option to offer in production and not only in a fixture.
 */
const bothKinds = () =>
  http.get('/api/v1/credentials', () =>
    HttpResponse.json({
      credentials: [
        credential('c-forge', 'forge_token', 'github pat'),
        credential('c-model', 'model_key', 'anthropic key'),
      ],
    }),
  )

const theProject = () => http.get('/api/v1/projects/:id', () => HttpResponse.json(project))

const repos = (...rows: RepoRow[]) =>
  http.get('/api/v1/projects/:id/repos', () => HttpResponse.json({ repos: rows }))

const inUse = () =>
  HttpResponse.json(
    {
      title: 'Conflict',
      status: 409,
      detail: 'this repository is used by existing tasks',
      correlation_id: 'cid-restrict',
    },
    { status: 409, headers: { 'content-type': 'application/problem+json' } },
  )

/** The one sentence `ErrorSurface` shows when there is no status to report. */
const OUTAGE = /the request could not be completed/i

describe('/repos/$projectId', () => {
  it('names the project and lists its repos in both projections', async () => {
    renderApp('/repos/p1', [theProject(), repos(primary, secondary)])

    expect(await screen.findByRole('heading', { name: 'Caesar' })).toBeInTheDocument()
    expect(screen.getByText('the conveyor itself')).toBeInTheDocument()

    /**
     * BOTH STRUCTURES, ASSERTED SEPARATELY, and the scoping is not stylistic.
     * jsdom loads no stylesheet, so `md:hidden` and `hidden md:table` are inert
     * there and every slug is in the document TWICE. A bare `getByText` throws
     * on that; a bare `getAllByText(...)[0]` would silently read whichever
     * projection came first. Task 32's Playwright project is where the widths
     * themselves become falsifiable — what is falsifiable here is that both
     * renderings exist and carry the same rows.
     */
    const cards = within(screen.getByTestId('repo-cards')).getAllByRole('listitem')
    expect(cards).toHaveLength(2)
    expect(cards[0]).toHaveTextContent('giiorg/caesar')
    expect(cards[0]).toHaveTextContent('github')
    expect(cards[0]).toHaveTextContent('main')
    expect(cards[1]).toHaveTextContent('group/sub/proj')
    expect(cards[1]).toHaveTextContent('gitlab')
    expect(cards[1]).toHaveTextContent('develop')

    const body = within(screen.getByTestId('repo-table')).getAllByRole('row')
    // Three rows: the header and the two repos.
    expect(body).toHaveLength(3)
    expect(
      within(body[1] as HTMLElement)
        .getAllByRole('cell')
        .slice(0, 3)
        .map(cellText),
    ).toEqual(['giiorg/caesar', 'github', 'main'])
    expect(
      within(body[2] as HTMLElement)
        .getAllByRole('cell')
        .slice(0, 3)
        .map(cellText),
    ).toEqual(['group/sub/proj', 'gitlab', 'develop'])
  })

  it('has its own empty state, which is not either of the project list’s', async () => {
    /**
     * NO REPO OVERRIDE: the harness default answers `{repos: null}`, the body a
     * Go nil slice really marshals to, so this case is what puts
     * `projectReposQueryOptions`'s `?? []` on a live path. MEASURED — with the
     * coalesce removed this test dies on `null.length` and the ones that supply
     * an array do not.
     */
    renderApp('/repos/p1', [theProject()])

    expect(
      await screen.findByText(/no repos are registered on this project yet/i),
    ).toBeInTheDocument()
    // The two sentences the PROJECT list owns must not appear on a screen whose
    // reader can already see the project.
    expect(screen.queryByText(/create the first/i)).not.toBeInTheDocument()
    expect(screen.queryByText(/shared with you yet/i)).not.toBeInTheDocument()
  })

  it('offers only forge_token credentials when registering a repo, and sends the forge', async () => {
    const posted = vi.fn()
    renderApp('/repos/p1', [
      theProject(),
      repos(),
      bothKinds(),
      http.post('/api/v1/projects/:id/repos', async ({ request, params }) => {
        posted({ projectId: params.id, body: await request.json() })
        return HttpResponse.json(secondary, { status: 201 })
      }),
    ])

    await userEvent.click(await screen.findByRole('button', { name: /new repo/i }))
    const sheet = within(await screen.findByRole('dialog'))

    const picker = await screen.findByLabelText(/^credential$/i)
    // NAMED, NOT COUNTED. Asserting "one option besides the placeholder" would
    // pass for a picker that filtered on the wrong predicate and happened to
    // keep one row; naming both the survivor and the casualty cannot.
    expect(within(picker).getByRole('option', { name: 'github pat' })).toBeInTheDocument()
    expect(within(picker).queryByRole('option', { name: 'anthropic key' })).not.toBeInTheDocument()

    await userEvent.selectOptions(await screen.findByLabelText(/^forge$/i), 'gitlab')
    await userEvent.type(screen.getByLabelText(/^slug$/i), 'group/sub/proj')
    await userEvent.clear(screen.getByLabelText(/default branch/i))
    await userEvent.type(screen.getByLabelText(/default branch/i), 'develop')
    await userEvent.selectOptions(picker, 'c-forge')
    await userEvent.click(sheet.getByRole('button', { name: /^register repo$/i }))

    expect(posted).toHaveBeenCalledWith({
      projectId: 'p1',
      body: {
        forge: 'gitlab',
        slug: 'group/sub/proj',
        default_branch: 'develop',
        credential_id: 'c-forge',
      },
    })
  })

  it('pre-fills the default branch, and says when there is no forge credential to pick', async () => {
    // NO CREDENTIAL OVERRIDE: the default answers `{credentials: null}`, which
    // exercises that query's own `?? []` — with the coalesce removed this dies
    // on `null.filter` — and leaves the picker genuinely empty, which is the
    // other sentence `CredentialPickerNote` owns.
    renderApp('/repos/p1', [theProject(), repos()])

    await userEvent.click(await screen.findByRole('button', { name: /new repo/i }))
    // `CreateRepo` substitutes `main` for an empty branch (`assets/repos.go`),
    // so a blank field would silently become a value the user never typed.
    expect(await screen.findByLabelText(/default branch/i)).toHaveValue('main')
    // "No forge credentials", not "no credentials": the list may be full of
    // model keys, none of which a repo can use. A bare empty select says
    // neither.
    expect(screen.getByText(/no forge credentials are stored yet/i)).toBeInTheDocument()
  })

  it('says why the credential picker is empty for a non-admin instead of showing a bare select', async () => {
    renderApp('/repos/p1', [
      theProject(),
      repos(),
      http.get('/api/v1/credentials', () =>
        HttpResponse.json(
          {
            title: 'Forbidden',
            status: 403,
            detail: 'admin role required',
            correlation_id: 'cid-403',
          },
          { status: 403, headers: { 'content-type': 'application/problem+json' } },
        ),
      ),
    ])

    await userEvent.click(await screen.findByRole('button', { name: /new repo/i }))

    /**
     * `GET /credentials` IS ADMIN-ONLY (`assets/api/api.go`, `adminOnly`) while
     * registering a repo needs only `manage` on the project — so this is a real
     * person's real state, not a contrived one. Their picker is empty for a
     * reason that has nothing to do with the deployment having no credentials,
     * and a bare empty select tells them to keep waiting for a control that
     * will never fill in.
     */
    expect(
      await screen.findByText(
        /credentials are listed by admins only — ask an admin to attach one/i,
      ),
    ).toBeInTheDocument()
    // NOT the transport sentence: a 403 is an answer, and reading it as a
    // failure to read would send the person looking for an outage.
    expect(screen.queryByText(/could not be read/i)).not.toBeInTheDocument()
  })

  it('shows a registered repo’s forge as text and never sends it on an edit', async () => {
    const patched = vi.fn()
    renderApp('/repos/p1', [
      theProject(),
      repos(primary),
      bothKinds(),
      http.patch('/api/v1/repos/:id', async ({ request, params }) => {
        patched({ id: params.id, body: await request.json() })
        return HttpResponse.json({ ...primary, slug: 'giiorg/caesar-2' })
      }),
    ])

    const cards = within(await screen.findByTestId('repo-cards'))
    await userEvent.click(cards.getByRole('button', { name: /^edit giiorg\/caesar$/i }))
    const sheet = within(await screen.findByRole('dialog'))

    /**
     * NO CONTROL AT ALL, not a disabled one. `ContractsIdAndUpdateRepo` carries
     * slug, default branch and credential and nothing else, so there is no
     * write path that could change a repo's forge — and a disabled select reads
     * as something a permission might unlock.
     */
    expect(sheet.queryByLabelText(/^forge$/i)).not.toBeInTheDocument()
    expect(sheet.getByText('Forge')).toBeInTheDocument()
    expect(sheet.getByText('github')).toBeInTheDocument()

    await userEvent.clear(sheet.getByLabelText(/^slug$/i))
    await userEvent.type(sheet.getByLabelText(/^slug$/i), 'giiorg/caesar-2')
    await userEvent.click(sheet.getByRole('button', { name: /^save repo$/i }))

    expect(patched).toHaveBeenCalledWith({
      id: 'r1',
      body: { slug: 'giiorg/caesar-2', default_branch: 'main', credential_id: 'c-forge' },
    })
  })

  it('reads a refused delete back to the operator, sentence and reference', async () => {
    renderApp('/repos/p1', [theProject(), repos(primary), http.delete('/api/v1/repos/:id', inUse)])

    const cards = within(await screen.findByTestId('repo-cards'))
    await userEvent.click(cards.getByRole('button', { name: /^delete giiorg\/caesar$/i }))

    /**
     * THE SERVER'S OWN SENTENCE, which is the whole point of Task 16's
     * classification: before it, an in-use repo answered 500 "internal error"
     * and a UI could only render an outage. The 409's detail names what is
     * holding the repo, so the operator knows the delete is refused rather than
     * broken — and the correlation id is here too, because a refusal is still
     * the thread between a complaint and a log line.
     */
    const panel = await screen.findByRole('alert')
    expect(panel).toHaveTextContent('this repository is used by existing tasks')
    expect(panel).toHaveTextContent('cid-restrict')
    expect(panel).not.toHaveTextContent(OUTAGE)
    // The repo is still there — a refused delete removed nothing.
    expect(within(screen.getByTestId('repo-cards')).getAllByRole('listitem')).toHaveLength(1)
  })

  it('says something different when the request never reached the server', async () => {
    renderApp('/repos/p1', [
      theProject(),
      repos(primary),
      http.delete('/api/v1/repos/:id', () => HttpResponse.error()),
    ])

    const cards = within(await screen.findByTestId('repo-cards'))
    await userEvent.click(cards.getByRole('button', { name: /^delete giiorg\/caesar$/i }))

    // No status, no problem document, so no correlation id to show — and the
    // wording names no cause, because a dropped connection and an unparseable
    // 200 both arrive here.
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(OUTAGE)
    expect(alert).not.toHaveTextContent(/used by existing tasks/i)
  })

  it('removes a repo from the list once the server accepts the delete', async () => {
    let deleted = false
    renderApp('/repos/p1', [
      theProject(),
      http.get('/api/v1/projects/:id/repos', () =>
        HttpResponse.json({ repos: deleted ? [] : [primary] }),
      ),
      http.delete('/api/v1/repos/:id', () => {
        deleted = true
        return new HttpResponse(null, { status: 204 })
      }),
    ])

    const cards = within(await screen.findByTestId('repo-cards'))
    await userEvent.click(cards.getByRole('button', { name: /^delete giiorg\/caesar$/i }))

    // The refetch is what makes the row go, so this dies with the
    // invalidation deleted — the component holds no list state of its own.
    expect(
      await screen.findByText(/no repos are registered on this project yet/i),
    ).toBeInTheDocument()
  })

  it('refuses to delete a project that still has repos, and stays on the screen', async () => {
    renderApp('/repos/p1', [
      theProject(),
      repos(primary),
      http.delete('/api/v1/projects/:id', () =>
        HttpResponse.json(
          {
            title: 'Conflict',
            status: 409,
            detail: 'project still has repos',
            correlation_id: 'cid-project',
          },
          { status: 409, headers: { 'content-type': 'application/problem+json' } },
        ),
      ),
    ])

    await userEvent.click(await screen.findByRole('button', { name: /delete project/i }))

    const panel = await screen.findByRole('alert')
    expect(panel).toHaveTextContent('project still has repos')
    // Still here: the refusal must not navigate, because the list the operator
    // has to empty first is on this screen.
    expect(screen.getByRole('heading', { name: 'Caesar' })).toBeInTheDocument()
  })
})

/** A cell's text, trimmed — the table's padding is markup, not content. */
const cellText = (cell: HTMLElement): string => cell.textContent?.trim() ?? ''
