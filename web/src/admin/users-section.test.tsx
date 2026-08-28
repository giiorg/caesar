import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import type { components } from '../api/schema'
import { renderApp } from '../test/render-app'
import { asAdminSection } from './admin-nav'

type UserRow = components['schemas']['ApiUserResponse']

/**
 * Annotated with the GENERATED row type, so a renamed field on `UserResponse`
 * reddens `tsc --noEmit` here rather than arriving at the surface as a missing
 * property.
 */
const user = (over: Partial<UserRow> & { id: string; email: string }): UserRow => ({
  role: 'member',
  active: true,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
  ...over,
})

/**
 * THE THREE ROWS DISAGREE ON EVERY RENDERED PROPERTY. Their emails differ,
 * their roles differ AND their active flags differ, so a list that printed one
 * row's role or one row's flag on every row is visible here. A fixture whose
 * rows coincide on a property cannot tell a correct list from that mistake.
 */
const ada: UserRow = user({ id: 'u1', email: 'ada@example.com', role: 'admin' })
const grace: UserRow = user({ id: 'u2', email: 'grace@example.com', role: 'member' })
const alan: UserRow = user({ id: 'u3', email: 'alan@example.com', role: 'member', active: false })

const users = (...rows: UserRow[]) =>
  http.get('/api/v1/users', () => HttpResponse.json({ users: rows }))

const problem = (status: number, title: string, detail: string) =>
  HttpResponse.json(
    { title, status, detail, correlation_id: 'cid-admin' },
    { status, headers: { 'content-type': 'application/problem+json' } },
  )

/** A signed-in MEMBER — the session every admin endpoint refuses. */
const asMember = () =>
  http.get('/api/v1/auth/me', () =>
    HttpResponse.json({
      user: {
        id: 'u9',
        email: 'member@example.com',
        role: 'member',
        active: true,
        created_at: '',
        updated_at: '',
      },
      grants: [],
    }),
  )

const rows = async () => within(await screen.findByTestId('user-rows'))

describe('the admin frame', () => {
  it('offers the four sections in reading order, with Users selected by default', async () => {
    renderApp('/admin', [users(ada)])

    const tabs = await screen.findAllByRole('tab')
    expect(tabs.map((t) => t.textContent)).toEqual(['Users', 'Harnesses', 'Credentials', 'Audit'])
    /**
     * THE ORDER IS ASSERTED AS A LIST, not by finding each tab by name. Four
     * `getByRole('tab', {name})` calls all pass against any permutation, and
     * the tab strip's order is the reading order of the screen.
     */
    expect(tabs[0]).toHaveAttribute('aria-selected', 'true')
    expect(tabs.slice(1).every((t) => t.getAttribute('aria-selected') === 'false')).toBe(true)
  })

  it('names row 09 rather than leaving the missing guardrails silent', async () => {
    renderApp('/admin', [users(ada)])

    expect(await screen.findByText(/policy baselines and the kill switch/i)).toBeInTheDocument()
    expect(screen.getByText(/row 09/i)).toBeInTheDocument()
  })

  it('keeps the row-09 card on the screen but outside the open section', async () => {
    /**
     * BOTH HALVES, and the second one is the one with teeth. Presence alone
     * does not pin the card's placement: MEASURED by moving it inside the
     * `tabpanel`, where it stays on screen for all four sections — the panel
     * always renders something — and every presence assertion stays green.
     *
     * What the containment assertion catches is the false CLAIM. The panel is
     * `aria-labelledby` the selected tab, so a card inside it announces itself
     * as part of the Audit section; this card belongs to the screen, and says
     * so by sitting beside the panel.
     */
    renderApp('/admin?section=audit')

    const card = await screen.findByText(/policy baselines and the kill switch/i)
    expect(card).toBeInTheDocument()
    expect(screen.getByRole('tabpanel')).not.toContainElement(card)
    expect(screen.queryByTestId('user-rows')).not.toBeInTheDocument()
  })

  it('selects the section named in the URL', async () => {
    renderApp('/admin?section=credentials')

    const credentials = await screen.findByRole('tab', { name: 'Credentials' })
    expect(credentials).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('tab', { name: 'Users' })).toHaveAttribute('aria-selected', 'false')
  })

  it('does not mark the default tab current while another section is open', async () => {
    /**
     * THIS IS THE ONLY ASSERTION THAT CAN FAIL WHEN `activeOptions={{ exact:
     * true }}` IS REMOVED, and without it that prop is unfalsifiable here.
     * `aria-selected` above is this component's own answer — it compares two
     * strings and would go on being right — while `aria-current` is the
     * ROUTER'S, and the router's active test is a SUBSET comparison: Users'
     * search is `{}`, a subset of `{section: 'credentials'}`, so without the
     * prop the router marks Users current at the same time as Credentials and
     * a screen reader is told two of four tabs are the current page.
     *
     * The same lesson `runs.index.tsx`'s filter chips and `approvals.index.tsx`
     * both had to learn, now on a third screen.
     */
    renderApp('/admin?section=credentials')

    expect(await screen.findByRole('tab', { name: 'Credentials' })).toHaveAttribute(
      'aria-current',
      'page',
    )
    expect(screen.getByRole('tab', { name: 'Users' })).not.toHaveAttribute('aria-current')
  })

  it('falls back to Users when the URL carries a section that is not one', async () => {
    /**
     * `?section=42` ARRIVES AS THE NUMBER 42. TanStack Router's default parser
     * runs `JSON.parse` per value, and because nothing between `/` and here
     * declares `validateSearch`, this route's `safeParse` rejecting the value
     * does NOT remove it from the search the component reads — the read site's
     * `asAdminSection` is the only thing standing between that number and a
     * lookup in the label map.
     */
    renderApp('/admin?section=42', [users(ada)])

    expect(await screen.findByRole('tab', { name: 'Users' })).toHaveAttribute(
      'aria-selected',
      'true',
    )
    expect(await screen.findByTestId('user-rows')).toBeInTheDocument()
  })

  it('narrows anything that is not a section to users', () => {
    // The unit behind the route test above: the four members pass through, and
    // every shape the URL can really deliver — a number, a bare string, absent
    // — lands on the default rather than escaping as a label-map key.
    expect(asAdminSection('audit')).toBe('audit')
    expect(asAdminSection('harnesses')).toBe('harnesses')
    expect(asAdminSection('credentials')).toBe('credentials')
    expect(asAdminSection('users')).toBe('users')
    expect(asAdminSection(42)).toBe('users')
    expect(asAdminSection('policies')).toBe('users')
    expect(asAdminSection(undefined)).toBe('users')
  })
})

describe('the users section', () => {
  it('lists each account as its email, its role and its active flag', async () => {
    renderApp('/admin', [users(ada, grace, alan)])

    const list = await rows()
    expect(within(list.getByTestId('user-u1')).getByText('ada@example.com')).toBeInTheDocument()
    expect(within(list.getByTestId('user-u1')).getByText('active')).toBeInTheDocument()
    expect(within(list.getByTestId('user-u3')).getByText('alan@example.com')).toBeInTheDocument()
    /**
     * THE DEACTIVATED ROW IS THE POINT. `active: true` and `active: false` are
     * both rendered as words, so a list that showed a badge only for one value
     * is distinguishable from one whose flag never arrived at all.
     */
    expect(within(list.getByTestId('user-u3')).getByText('deactivated')).toBeInTheDocument()

    // The role select carries the row's OWN role, not the first option.
    expect(list.getByLabelText('Role for ada@example.com')).toHaveValue('admin')
    expect(list.getByLabelText('Role for grace@example.com')).toHaveValue('member')
  })

  it('shows a role this client has not learned rather than silently reading it as member', async () => {
    /**
     * A ROLE THE SERVER GREW AND THIS CLIENT HAS NOT. `role` is a bare string
     * on the wire — the generated types carry no enum, because the Go field's
     * underlying type is `string` — so this is a body the contract permits
     * today, not a hypothetical.
     *
     * Without the extra option a `<select>` whose value matches none of its
     * options selects NOTHING, and the control renders as its first option:
     * the row would read `member` for an account that is not one, on the screen
     * whose entire job is saying who holds what. That is the silent-wrong
     * direction, and it is invisible to every other test here because every
     * other fixture uses a role the client knows.
     */
    renderApp('/admin', [users(user({ id: 'u5', email: 'auditor@example.com', role: 'auditor' }))])

    const list = await rows()
    expect(list.getByLabelText('Role for auditor@example.com')).toHaveValue('auditor')
  })

  it('renders the frame against the harness default list', async () => {
    /**
     * NO USERS OVERRIDE. `renderApp`'s default answers `{users: null}` —
     * `UserListResponse.users` is required AND nullable because a Go nil slice
     * marshals to null — so this test is what puts `usersQueryOptions`'s
     * `?? []` on a live path. With that coalesce deleted this dies on
     * `null.map`, and without this test it would sit on no live path at all
     * while a comment claimed otherwise (Task 19's failure, twice recorded).
     *
     * It asserts the LIST IS EMPTY rather than asserting an empty-state
     * sentence, because there is none: the reader of an admin-only list of all
     * accounts is always in it, so a genuinely empty answer is not a state the
     * server can produce.
     */
    renderApp('/admin')

    expect(await screen.findByRole('tab', { name: 'Users' })).toBeInTheDocument()
    expect(await rows()).toBeTruthy()
    expect(screen.queryByRole('listitem')).not.toBeInTheDocument()
  })

  it('creates a user and refreshes the list', async () => {
    const posted = vi.fn()
    let listed = 0
    renderApp('/admin', [
      http.post('/api/v1/users', async ({ request }) => {
        posted(await request.json())
        return HttpResponse.json(user({ id: 'u4', email: 'new@example.com' }), { status: 201 })
      }),
      http.get('/api/v1/users', () => {
        listed += 1
        return HttpResponse.json({ users: listed === 1 ? [ada] : [ada, grace] })
      }),
    ])

    await userEvent.click(await screen.findByRole('button', { name: 'Add user' }))
    /**
     * THE DEFAULT IS THE LEAST PRIVILEGE, asserted before anything is picked.
     * An unattended submit makes a member; a default of `admin` would pass
     * every other assertion in this test, because the next line chooses a role
     * explicitly.
     */
    expect(await screen.findByLabelText('Role')).toHaveValue('member')
    await userEvent.type(await screen.findByLabelText('Email'), 'grace@example.com')
    await userEvent.type(screen.getByLabelText('Starting password'), 'a-long-enough-secret')
    await userEvent.selectOptions(screen.getByLabelText('Role'), 'admin')
    await userEvent.click(screen.getByRole('button', { name: 'Create user' }))

    await expect
      .poll(() => posted.mock.calls[0]?.[0])
      .toEqual({ email: 'grace@example.com', password: 'a-long-enough-secret', role: 'admin' })
    // THE INVALIDATION, asserted through the rendered list rather than through a
    // request count: the second row only appears if `['users']` was refetched.
    expect(
      await within(await screen.findByTestId('user-rows')).findByText('grace@example.com'),
    ).toBeInTheDocument()
  })

  it('refuses a password the server would refuse, without asking it', async () => {
    /**
     * The client's floor is the server's (`MinPasswordLen` = 12,
     * `identity/password.go`). A short one never leaves the browser, so the
     * assertion is that NO request was made — a form that posted anyway and
     * rendered the server's field error would look identical without this.
     */
    const posted = vi.fn()
    renderApp('/admin', [
      users(ada),
      http.post('/api/v1/users', () => {
        posted()
        return HttpResponse.json(ada, { status: 201 })
      }),
    ])

    await userEvent.click(await screen.findByRole('button', { name: 'Add user' }))
    await userEvent.type(await screen.findByLabelText('Email'), 'grace@example.com')
    await userEvent.type(screen.getByLabelText('Starting password'), 'short')
    await userEvent.click(screen.getByRole('button', { name: 'Create user' }))

    expect(await screen.findByText(/use at least 12 characters/i)).toBeInTheDocument()
    expect(posted).not.toHaveBeenCalled()
  })

  it('routes a duplicate-email conflict onto the form', async () => {
    renderApp('/admin', [
      users(ada),
      http.post('/api/v1/users', () => problem(409, 'Conflict', 'email already registered')),
    ])

    await userEvent.click(await screen.findByRole('button', { name: 'Add user' }))
    await userEvent.type(await screen.findByLabelText('Email'), 'ada@example.com')
    await userEvent.type(screen.getByLabelText('Starting password'), 'a-long-enough-secret')
    await userEvent.click(screen.getByRole('button', { name: 'Create user' }))

    /**
     * THE BANNER, not a field error. `CreateUser`'s conflict carries no
     * `WithFields` (`identity/service.go`), so `applyProblemToForm` can only
     * take its no-fields arm and the detail becomes the form-level message —
     * the same shape the project form's duplicate-name 409 has.
     */
    const banner = await screen.findByRole('alert')
    expect(banner).toHaveTextContent(/email already registered/i)
    /**
     * `toHaveTextContent` rather than `getByText('cid-admin')`, because
     * `FormError` renders the id as part of a sentence — `reference: {id}` —
     * so the id is two text nodes inside one `<p>` and an exact text query
     * matches no single element. `ApiErrorPanel` puts it in a `<code>` of its
     * own, which is why the same assertion IS exact on the refusal surfaces
     * below. Worth stating: the difference is in the surfaces, not in whether
     * the id survived.
     */
    expect(banner).toHaveTextContent('cid-admin')
  })

  it('changes a role through PATCH and refreshes the list', async () => {
    const patched = vi.fn()
    let listed = 0
    renderApp('/admin', [
      http.patch('/api/v1/users/:id', async ({ request, params }) => {
        patched({ id: params.id, body: await request.json() })
        return HttpResponse.json({ ...grace, role: 'admin' })
      }),
      http.get('/api/v1/users', () => {
        listed += 1
        return HttpResponse.json({ users: [listed === 1 ? grace : { ...grace, role: 'admin' }] })
      }),
    ])

    const list = await rows()
    await userEvent.selectOptions(list.getByLabelText('Role for grace@example.com'), 'admin')

    await expect
      .poll(() => patched.mock.calls[0]?.[0])
      .toEqual({ id: 'u2', body: { role: 'admin' } })
    /**
     * ONLY `role` IS SENT. `UpdateUserRequest` is two independent pointers and
     * the server reads `active` as "leave it alone" when absent, so a body that
     * also carried `active` would work today and would silently re-assert a
     * stale flag the moment two admins edit the same row.
     */
    await expect.poll(() => list.getByLabelText('Role for grace@example.com')).toHaveValue('admin')
  })

  it('deactivates through PATCH and offers the way back', async () => {
    const patched = vi.fn()
    renderApp('/admin', [
      users(grace),
      http.patch('/api/v1/users/:id', async ({ request }) => {
        patched(await request.json())
        return HttpResponse.json({ ...grace, active: false })
      }),
    ])

    const list = await rows()
    await userEvent.click(list.getByRole('button', { name: 'Deactivate grace@example.com' }))

    await expect.poll(() => patched.mock.calls[0]?.[0]).toEqual({ active: false })
  })

  it('offers Reactivate on a deactivated row', async () => {
    /**
     * The control is the ROW'S flag inverted, not a fixed "Deactivate". A
     * screen that could only ever set `active: false` would show `alan` as
     * deactivated beside a button that cannot bring him back, and the only
     * other door is SQL.
     */
    const patched = vi.fn()
    renderApp('/admin', [
      users(alan),
      http.patch('/api/v1/users/:id', async ({ request }) => {
        patched(await request.json())
        return HttpResponse.json({ ...alan, active: true })
      }),
    ])

    const list = await rows()
    await userEvent.click(list.getByRole('button', { name: 'Reactivate alan@example.com' }))

    await expect.poll(() => patched.mock.calls[0]?.[0]).toEqual({ active: true })
  })
})

describe('the refusals this screen can really produce', () => {
  it('says a member lacks the admin ROLE, and never mentions a project grant', async () => {
    /**
     * THE WHOLE POINT OF THIS TEST IS THE SECOND ASSERTION. `/admin` is in the
     * nav for every session, so a member reaching it is routine rather than an
     * attack — and `ApiErrorPanel`, the tree's other 403 surface, answers every
     * 403 with "You do not have access to this project. Ask an admin for a
     * grant." That sentence is true on the project screens it was written for
     * and false here: what this reader lacks is the admin role, and no project
     * grant will ever fix it. Rendering the section through `ErrorSurface`
     * passes the first assertion and fails the second.
     */
    renderApp('/admin', [
      asMember(),
      http.get('/api/v1/users', () => problem(403, 'Forbidden', 'admin role required')),
    ])

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/admin role required/i)
    expect(alert).not.toHaveTextContent(/grant/i)
    // The thread back to a log line, on the surface a confused user complains from.
    expect(within(alert).getByText('cid-admin')).toBeInTheDocument()
    // The frame still renders — the refusal is one section's answer, not a blank screen.
    expect(screen.getByRole('tab', { name: 'Users' })).toBeInTheDocument()
  })

  it("surfaces the own-row guard in the server's words", async () => {
    renderApp('/admin', [
      users(ada),
      http.patch('/api/v1/users/:id', () =>
        problem(403, 'Forbidden', 'cannot change own role or active flag'),
      ),
    ])

    const list = await rows()
    await userEvent.click(list.getByRole('button', { name: 'Deactivate ada@example.com' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/cannot change own role or active flag/i)
    expect(alert).not.toHaveTextContent(/grant/i)
  })

  it('surfaces the last-active-admin conflict, which the plan did not name', async () => {
    /**
     * THE SECOND GUARD. `UpdateUser` refuses two things and the brief named one:
     * demoting or deactivating the last active admin answers 409 "cannot remove
     * the last active admin", decided inside a transaction holding an advisory
     * lock over `CountActiveAdmins` (`identity/service.go`). It is the refusal
     * this client CANNOT predict — the count is not on this page and two admins
     * pressing Deactivate at once is the race that lock exists for — which is
     * the argument for surfacing the server's answer rather than disabling
     * controls the screen thinks it can rule out.
     */
    renderApp('/admin', [
      users(ada),
      http.patch('/api/v1/users/:id', () =>
        problem(409, 'Conflict', 'cannot remove the last active admin'),
      ),
    ])

    const list = await rows()
    await userEvent.selectOptions(list.getByLabelText('Role for ada@example.com'), 'member')

    expect(await screen.findByText(/cannot remove the last active admin/i)).toBeInTheDocument()
  })

  it('offers no way to reset somebody elses password', async () => {
    /**
     * ASSERTED ON THE ROWS, not on the screen. The create form legitimately has
     * a password field, so a document-wide `queryByText(/password/i)` would be
     * green only while the sheet happened to be shut — and would go red the day
     * somebody opened it, for no defect. The backend shape behind this:
     * `PUT /auth/password` is `authenticated`, not `adminOnly`, and takes the
     * CURRENT password, so no request exists that resets another account's.
     */
    renderApp('/admin', [users(ada, grace)])

    const list = await rows()
    expect(within(list.getByTestId('user-u2')).queryByText(/password/i)).not.toBeInTheDocument()
    expect(list.queryByRole('button', { name: /password/i })).not.toBeInTheDocument()
  })
})
