import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { delay, HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import { queryClient } from '../api/query-client'
import type { components } from '../api/schema'
import { renderApp } from '../test/render-app'

type AuditRow = components['schemas']['ApiAuditEntryResponse']
type UserRow = components['schemas']['ApiUserResponse']

/**
 * EVERY FIXTURE ROW BELOW IS A ROW SOME WRITER IN `backend/internal` ACTUALLY
 * PRODUCES, named in its comment — the discipline session 91 wrote down after a
 * reviewer proposed a phase row this control plane cannot write. The audit
 * table is written from 47 call sites with wildly different habits, and the
 * three that matter to this screen (no actor, no correlation id, no detail) are
 * exactly the ones a fixture invented from the DTO would smooth over.
 */
const entry = (over: Partial<AuditRow> & { id: number; action: string }): AuditRow => ({
  occurred_at: '2026-08-27T12:00:00Z',
  actor_user_id: 'u-ada',
  subject_kind: 'harness',
  subject_id: 'h1',
  correlation_id: 'cid-default',
  detail: {},
  ...over,
})

/** `assets/harnesses.go` — actor, correlation id, and NO Detail at all. */
const harnessDeleted = entry({
  id: 5,
  action: 'harness.deleted',
  occurred_at: '2026-08-27T12:05:00Z',
  correlation_id: 'cid-del',
})

/** `assets/harnesses.go` — the fully-populated row: actor, correlation, detail. */
const harnessRegistered = entry({
  id: 4,
  action: 'harness.registered',
  occurred_at: '2026-08-27T12:04:00Z',
  correlation_id: 'cid-reg',
  detail: { name: 'claude-main', kind: 'claude-code', image_ref: 'ghcr.io/x/cc:1' },
})

/**
 * `assets/projects.go`, acted on by an account the users list does not hold.
 *
 * REACHABLE RATHER THAN HYPOTHETICAL: this page is fetched fresh while
 * `['users']` may be up to a minute stale (`AUTH_SCOPED_QUERY_OPTIONS`'s
 * interval), so an account created since the last users fetch acts on rows this
 * screen cannot name yet.
 */
const projectCreated = entry({
  id: 3,
  action: 'project.created',
  occurred_at: '2026-08-27T12:03:00Z',
  actor_user_id: 'u-ghost',
  subject_kind: 'project',
  subject_id: 'p1',
  correlation_id: 'cid-proj',
  detail: { name: 'caesar' },
})

/**
 * `identity/login.go` — no actor because the login FAILED, and `subject_id` is
 * the lowercased email that was tried rather than any id. It is in the fixture
 * to keep two claims honest at once: that a null actor renders, and that
 * nothing on this row treats `subject_id` as an identifier.
 */
const loginFailed = entry({
  id: 2,
  action: 'auth.login_failed',
  occurred_at: '2026-08-27T12:02:00Z',
  actor_user_id: null,
  subject_kind: 'user',
  subject_id: 'intruder@example.com',
  correlation_id: 'cid-login',
  detail: { ip: '10.0.0.9' },
})

/**
 * `runs/orchestrator.go` — written inside the orchestrator's own transaction,
 * so it has NO actor and NO correlation id. The empty string is not a fixture
 * convenience: that writer passes no `CorrelationID` field at all, and
 * `httpx.CorrelationID` answers `""` off any context that never passed through
 * the HTTP middleware, which is every runner and background path.
 */
const runFinished = entry({
  id: 1,
  action: 'run.finished',
  occurred_at: '2026-08-27T12:01:00Z',
  actor_user_id: null,
  subject_kind: 'run',
  subject_id: 'run-1',
  correlation_id: '',
  detail: { status: 'failed', outcome: { reason: 'gate_rejected' } },
})

const ALL = [harnessDeleted, harnessRegistered, projectCreated, loginFailed, runFinished]

/** One page, no cursor: the whole log fits and there is nothing to load. */
const onePage = (...entries: AuditRow[]) =>
  http.get('/api/v1/audit', () => HttpResponse.json({ entries, next_cursor: null }))

const user = (id: string, email: string): UserRow => ({
  id,
  email,
  role: 'admin',
  active: true,
  created_at: '',
  updated_at: '',
})

const users = (...rows: UserRow[]) =>
  http.get('/api/v1/users', () => HttpResponse.json({ users: rows }))

/** The account every actor-bearing fixture row points at. */
const ada = users(user('u-ada', 'ada@example.com'))

const problem = (status: number, title: string, detail: string) =>
  HttpResponse.json(
    { title, status, detail, correlation_id: 'cid-audit' },
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

const row = async (id: number) => within(await screen.findByTestId(`audit-${id}`))

describe('/admin?section=audit', () => {
  it('renders each entry in the order the server sent it, with what it was and who did it', async () => {
    renderApp('/admin?section=audit', [onePage(...ALL), ada])

    const rows = within(await screen.findByTestId('audit-entries')).getAllByRole('listitem')
    expect(rows).toHaveLength(5)
    // NEWEST FIRST IS THE SERVER'S ANSWER AND THE CLIENT DOES NOT RE-DERIVE IT.
    // `ListPage` orders by `(occurred_at DESC, id DESC)` and the cursor carries
    // BOTH halves for a stated reason — one transaction emits several entries
    // from a single clock reading, so equal timestamps are the ordinary case. A
    // client that sorted by `occurred_at` alone would disagree with the cursor
    // at every page boundary. This asserts the rendered order IS the received
    // order, which is why the fixture is passed newest-first and read
    // positionally.
    expect(rows[0]).toHaveTextContent('harness.deleted')
    expect(rows[4]).toHaveTextContent('run.finished')

    const registered = await row(4)
    // The actor as a person, not as a UUID: `ApiAuditEntryResponse` carries
    // only `actor_user_id`, unlike the decision history's `acted_by_email`, so
    // the email is a join this screen performs against `['users']`.
    expect(await registered.findByText('ada@example.com')).toBeInTheDocument()
    expect(registered.queryByText('u-ada')).toBeNull()
    // KIND AND ID AS ONE STRING, in that order. Asserted whole rather than as
    // two `getByText(/harness/)`-shaped queries — which would match this row's
    // ACTION as well and throw on the ambiguity — and because the id alone does
    // not say which table it indexes: "h1" is a harness here and could be a
    // run somewhere else.
    expect(registered.getByText('harness h1')).toBeInTheDocument()
    expect(registered.getByText('cid-reg')).toBeInTheDocument()
    // Raw ISO in a <time>, the shape `decision-history.tsx` established for V1:
    // the machine-readable value is where a reader and a later formatter both
    // expect it.
    expect(registered.getByText('2026-08-27T12:04:00Z')).toBeInTheDocument()
  })

  it('says system for a row with no actor, and leaves an email subject alone', async () => {
    renderApp('/admin?section=audit', [onePage(loginFailed), ada])

    const failed = await row(2)
    expect(await failed.findByText(/system/i)).toBeInTheDocument()
    // A FAILED LOGIN'S SUBJECT IS AN EMAIL, not an id. Nothing may truncate it,
    // link it or otherwise treat it as an identifier — it is the whole content
    // of the row for whoever is reading the log because of it.
    expect(failed.getByText(/intruder@example\.com/)).toBeInTheDocument()
  })

  it('falls back to the raw actor id rather than to a blank or to somebody else', async () => {
    renderApp('/admin?section=audit', [onePage(projectCreated), ada])

    const created = await row(3)
    // The users list HAS an account and it is not this one. A join written as
    // `users[0]` or as `find(...)?.email` would render Ada's address or nothing
    // at all against a row she had no part in — the worst failure available to
    // an audit log.
    expect(await created.findByText('u-ghost')).toBeInTheDocument()
    expect(created.queryByText('ada@example.com')).toBeNull()
  })

  it('does not call an actor unknown while the account list is still in flight', async () => {
    /**
     * THE TWO MISSES ARE DIFFERENT STATEMENTS and the screen must not merge
     * them. With the users list unanswered, rendering the raw id would tell the
     * reader that this account is unknown to the system — the same sentence the
     * dangling-reference case makes deliberately, said about a request that has
     * simply not come back. Without this test the loading branch is a line no
     * fixture reaches, and deleting it changes nothing anywhere.
     */
    renderApp('/admin?section=audit', [
      onePage(harnessRegistered),
      http.get('/api/v1/users', async () => {
        await delay('infinite')
        return HttpResponse.json({ users: [] })
      }),
    ])

    const registered = await row(4)
    expect(registered.getByText('…')).toBeInTheDocument()
    expect(registered.queryByText('u-ada')).toBeNull()
  })

  it('says a missing correlation id is missing instead of rendering nothing', async () => {
    renderApp('/admin?section=audit', [onePage(runFinished, harnessRegistered), ada])

    // `correlation_id` is required and NON-nullable on the wire, so the absence
    // arrives as `""` — and `<code>{''}</code>` is an element with no text,
    // which every getByText in this file is blind to. That is the shape Task 18
    // recorded on the empty gate note, and it lands here on the rows an
    // operator is most likely to be tracing.
    expect(await (await row(1)).findByText(/not recorded/i)).toBeInTheDocument()
    // And the guard is not a blanket: a row that HAS one still shows it.
    expect((await row(4)).getByText('cid-reg')).toBeInTheDocument()
    expect((await row(4)).queryByText(/not recorded/i)).toBeNull()
  })

  it('keeps detail collapsed until it is asked for, and omits it when there is none', async () => {
    renderApp('/admin?section=audit', [onePage(harnessRegistered, harnessDeleted), ada])

    const registered = await row(4)
    const json = registered.getByText(/"image_ref": "ghcr.io\/x\/cc:1"/)
    // COLLAPSED, asserted as visibility rather than as absence: the JSON is in
    // the document either way, and a `<details>` that shipped `open` would pass
    // every query-based assertion while turning a five-row page into a wall of
    // objects.
    expect(json).not.toBeVisible()
    await userEvent.click(registered.getByText('Detail'))
    expect(json).toBeVisible()

    // NO DISCLOSURE ON AN EMPTY DETAIL. `Record` substitutes `{}` for a nil map
    // and `harness.deleted` passes no Detail at all, so `{}` is what most rows
    // carry — an unconditional `<details>` puts a triangle on nearly every line
    // of the screen, each opening onto two characters.
    expect((await row(5)).queryByText('Detail')).toBeNull()
  })

  it('survives a null detail, which the contract permits even though the reader never builds one', async () => {
    // The same standing of `entries: null` and the five other nulls in
    // `render-app.tsx`: `Detail` is `map[string]any` marked required and a Go
    // nil map marshals to null, so the generated type says `| null`. No such
    // body exists today — `ListPage` decodes into an initialized map and
    // `Record` substitutes `{}` before writing — so this is a contract
    // permission rather than an observed response, and the guard would be
    // unreachable from every test in the tree without a fixture that spends it.
    renderApp('/admin?section=audit', [onePage(entry({ ...harnessDeleted, detail: null })), ada])

    const deleted = await row(5)
    expect(deleted.getByText('harness.deleted')).toBeInTheDocument()
    expect(deleted.queryByText('Detail')).toBeNull()
  })

  it('loads the next page on the cursor the previous page returned', async () => {
    const cursors: (string | null)[] = []
    renderApp('/admin?section=audit', [
      http.get('/api/v1/audit', ({ request }) => {
        const cursor = new URL(request.url).searchParams.get('cursor')
        cursors.push(cursor)
        return HttpResponse.json(
          cursor === null
            ? { entries: [harnessDeleted], next_cursor: 'after-5' }
            : { entries: [runFinished], next_cursor: null },
        )
      }),
      ada,
    ])

    expect(await screen.findByText('harness.deleted')).toBeInTheDocument()
    expect(screen.queryByText('run.finished')).toBeNull()

    await userEvent.click(await screen.findByRole('button', { name: /load more/i }))

    // APPENDS, never replaces: page 1's row is still on screen beside page 2's.
    expect(await screen.findByText('run.finished')).toBeInTheDocument()
    expect(screen.getByText('harness.deleted')).toBeInTheDocument()
    // The cursor really travelled. Asserting only on the rendered rows would
    // pass against a "load more" that re-requested page 1 and appended it, and
    // with two pages that differ that failure looks like success right up until
    // the two pages are the same page.
    expect(cursors).toEqual([null, 'after-5'])
    // A null `next_cursor` is the last page, and the control that would ask for
    // a page that does not exist goes away with it.
    expect(screen.queryByRole('button', { name: /load more/i })).toBeNull()
  })

  it('re-walks the cursor from the page it just refetched, so a refresh cannot double a row', async () => {
    /**
     * THE ONE PROPERTY OF A CURSOR-PAGED QUERY THAT NOTHING ELSE PINS, and the
     * audit log is where it bites: `AUTH_SCOPED_QUERY_OPTIONS` refreshes this
     * query every 60 seconds, react-query refetches EVERY loaded page, and this
     * table grows between the two walks — so the refreshed page 1 holds rows
     * that did not exist when page 2's cursor was minted. If the refetch reused
     * the STORED cursor, page 2 would start from a boundary that has moved and
     * the rows in between would render twice, on the one screen where a
     * duplicated row is a fabricated fact.
     *
     * MEASURED on the pinned 5.101.4 rather than reasoned: the walk is
     * `[null, 'c1', null, 'c1-new']` — page 1 from the stored initial param,
     * every later page from `getNextPageParam` applied to the page just
     * fetched. `infiniteQueryBehavior.js` is the mechanism and it is worth
     * naming, because it is what makes this true of the INTERVAL and not only
     * of the invalidation this test drives it with: the recomputing loop is the
     * `else` arm taken by every fetch that carries no `direction`, and only
     * `fetchNextPage`/`fetchPreviousPage` carry one.
     *
     * `decisions/decisions-query.ts` states the "refetches every page" half of
     * this and could not have observed this half — its fixture answers the same
     * `next_cursor` every time, which is exactly the fixture in which both
     * behaviours look identical.
     */
    const cursors: (string | null)[] = []
    let firstPage = 0
    renderApp('/admin?section=audit', [
      http.get('/api/v1/audit', ({ request }) => {
        const cursor = new URL(request.url).searchParams.get('cursor')
        cursors.push(cursor)
        if (cursor !== null) return HttpResponse.json({ entries: [runFinished], next_cursor: null })
        firstPage += 1
        // THE CURSOR PAGE 1 HANDS OUT CHANGES BETWEEN THE WALKS. Without that
        // this test passes against either behaviour — the whole point is that
        // the second walk's page 2 asks for the boundary the second walk's page
        // 1 named, not the one the first walk's did.
        return HttpResponse.json({
          entries: [firstPage === 1 ? harnessDeleted : harnessRegistered],
          next_cursor: firstPage === 1 ? 'c1' : 'c1-new',
        })
      }),
      ada,
    ])

    await screen.findByTestId('audit-5')
    await userEvent.click(await screen.findByRole('button', { name: /load more/i }))
    await screen.findByTestId('audit-1')
    expect(cursors).toEqual([null, 'c1'])

    await queryClient.invalidateQueries({ queryKey: ['audit'] })

    await screen.findByTestId('audit-4')
    expect(cursors).toEqual([null, 'c1', null, 'c1-new'])
    // And the reader's view of it: two pages loaded, two rows on screen, the
    // superseded page-1 row gone rather than kept alongside its replacement.
    const rows = within(screen.getByTestId('audit-entries')).getAllByRole('listitem')
    expect(rows).toHaveLength(2)
    expect(screen.queryByTestId('audit-5')).toBeNull()
  })

  it('says the log is empty in its own words', async () => {
    // Runs on `render-app.tsx`'s DEFAULT handler, which answers `entries: null`
    // — the shape the contract permits (required AND nullable, a Go nil slice
    // marshals to null) and the one a client reading `.length` off the response
    // throws on. Deliberately NOT overridden here, so the `?? []` in
    // `audit/audit-query.ts` sits on a live path rather than on none.
    renderApp('/admin?section=audit')

    expect(await screen.findByText(/the audit log is empty/i)).toBeInTheDocument()
  })

  it('says a member lacks the admin ROLE, and never mentions a project grant', async () => {
    renderApp('/admin?section=audit', [
      asMember(),
      http.get('/api/v1/audit', () => problem(403, 'Forbidden', 'admin role required')),
    ])

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/admin role required/i)
    // `ApiErrorPanel` answers every 403 with "Ask an admin for a grant", which
    // is true on the project screens it was written for and false here: no
    // project grant will ever open the audit log. Routing this refusal through
    // `ErrorSurface` passes the first assertion and fails this one.
    expect(alert).not.toHaveTextContent(/grant/i)
    expect(within(alert).getByText('cid-audit')).toBeInTheDocument()
    // The frame survives — the refusal is one section's answer, not a blank screen.
    expect(screen.getByRole('tab', { name: 'Audit' })).toBeInTheDocument()
  })
})
