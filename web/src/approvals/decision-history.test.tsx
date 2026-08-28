import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { components } from '../api/schema'
import { renderApp } from '../test/render-app'

type DecisionRow = components['schemas']['ApiDecisionResponse']

/**
 * A history row as the wire carries it.
 *
 * Annotated `DecisionRow` so a field added to or renamed on `DecisionResponse`
 * reddens `tsc --noEmit` here rather than arriving at the surface as a missing
 * property — the same discipline `approvals.test.tsx` applies to `Run`.
 *
 * `generation` and `attempt` DISAGREE with each other and with 1. The link this
 * suite asserts on carries both, and a fixture where they coincide cannot tell
 * a correct link from one that writes the same number into both slots — nor
 * from one that hard-codes the first generation. Task 9's own selector tests
 * learned this: the pair is only a pair if the fixture makes it one.
 */
const changes: DecisionRow = {
  id: 'd2',
  run_id: 'run-1',
  task_id: 't1',
  task_title: 'Add a thing',
  node_id: 'gate',
  generation: 2,
  attempt: 3,
  decision: 'request_changes',
  note: 'the migration needs a down',
  acted_by_email: 'alice@example.com',
  acted_at: '2026-08-27T10:00:00Z',
  examined_artifact_id: 'a1',
}

const approved: DecisionRow = {
  id: 'd1',
  run_id: 'run-2',
  task_id: 't2',
  task_title: 'Fix the other thing',
  node_id: 'review',
  generation: 1,
  attempt: 1,
  decision: 'approve',
  note: '',
  acted_by_email: 'bob@example.com',
  acted_at: '2026-08-26T09:00:00Z',
  examined_artifact_id: 'a2',
}

/** One page, no cursor: the whole history fits and there is nothing to load. */
const onePage = (...decisions: DecisionRow[]) =>
  http.get('/api/v1/approvals/decisions', () => HttpResponse.json({ decisions, next_cursor: null }))

/** The search of an href, parsed rather than string-matched — see the link test. */
const searchOf = (href: string): URLSearchParams => new URL(href, 'http://x').searchParams

describe('/approvals?tab=history', () => {
  it('lists decisions in the order the server sent them, with verdict, note, actor and time', async () => {
    renderApp('/approvals?tab=history', [onePage(changes, approved)])

    const rows = within(await screen.findByTestId('decision-history')).getAllByRole('listitem')
    expect(rows).toHaveLength(2)
    // NEWEST FIRST IS THE SERVER'S ANSWER AND THE CLIENT'S JOB IS NOT TO
    // RE-DERIVE IT. `ListDecisionsPage` orders by `(acted_at DESC, id DESC)`
    // and the cursor walks that same key, so a client that sorted its own pages
    // would disagree with the cursor at every page boundary — two rows sharing
    // an `acted_at` would swap sides on page 2 and one of them would be shown
    // twice. What this asserts is that the rendered order IS the received
    // order, which is why the fixture is passed newest-first and read
    // positionally.
    expect(rows[0]).toHaveTextContent('Add a thing')
    expect(rows[1]).toHaveTextContent('Fix the other thing')

    const first = within(rows[0] as HTMLElement)
    // The verb, not the token: `requested changes on` is what `gate-decision.tsx`
    // already says on the run timeline, and the two surfaces show the same
    // event to the same person.
    expect(first.getByText(/requested changes on/)).toBeInTheDocument()
    expect(first.getByText(/alice@example\.com/)).toBeInTheDocument()
    expect(first.getByText('the migration needs a down')).toBeInTheDocument()
    expect(first.getByText('2026-08-27T10:00:00Z')).toBeInTheDocument()

    const second = within(rows[1] as HTMLElement)
    expect(second.getByText(/approved/)).toBeInTheDocument()
    // AN APPROVE WITH NO NOTE RENDERS NO NOTE LINE, counted rather than
    // described. `note` is required and non-nullable on the wire, so "no note"
    // is the empty string and not absence — and an unguarded `<p>{d.note}</p>`
    // renders an element with no text, which every `getByText` in this file is
    // blind to. Counting the paragraphs is what sees it: title, sentence, note,
    // time for the row that has one; the same minus the note for the row that
    // does not.
    expect(first.getAllByRole('paragraph')).toHaveLength(4)
    expect(second.getAllByRole('paragraph')).toHaveLength(3)
  })

  it('links each row into its run at the exact attempt the decision was made on', async () => {
    renderApp('/approvals?tab=history', [onePage(changes)])

    const link = await screen.findByRole('link', { name: /Add a thing/ })
    const href = link.getAttribute('href') ?? ''
    expect(href.startsWith('/runs/run-1')).toBe(true)
    // THE TRIPLE, ASSERTED PART BY PART rather than against a whole query
    // string, because the router owns the parameter ORDER and this suite must
    // not go red the day it changes one. Each part is asserted on its own so
    // that dropping any one of the three fails here: `?phase=` alone opens the
    // node's NEWEST attempt (`phaseForSelector` falls through when either half
    // of the selector is missing), which by the time anybody reads a history
    // row is usually a different decision, by a different person, about a
    // different patch.
    const search = searchOf(href)
    expect(search.get('phase')).toBe('gate')
    expect(search.get('generation')).toBe('2')
    expect(search.get('attempt')).toBe('3')
  })

  it('loads the next page on the cursor the previous page returned', async () => {
    const cursors: (string | null)[] = []
    renderApp('/approvals?tab=history', [
      http.get('/api/v1/approvals/decisions', ({ request }) => {
        const cursor = new URL(request.url).searchParams.get('cursor')
        cursors.push(cursor)
        return HttpResponse.json(
          cursor === null
            ? { decisions: [changes], next_cursor: 'after-d2' }
            : { decisions: [approved], next_cursor: null },
        )
      }),
    ])

    expect(await screen.findByText('Add a thing')).toBeInTheDocument()
    expect(screen.queryByText('Fix the other thing')).toBeNull()

    await userEvent.click(await screen.findByRole('button', { name: /load more/i }))

    // APPENDS, never replaces: page 1's row is still on screen beside page 2's.
    expect(await screen.findByText('Fix the other thing')).toBeInTheDocument()
    expect(screen.getByText('Add a thing')).toBeInTheDocument()
    // The cursor really travelled. Asserting only on the rendered rows would
    // pass against a "load more" that re-requested page 1 and appended it —
    // and with a fixture whose two pages differ, that failure looks like
    // success only until the two pages are the same page.
    expect(cursors).toEqual([null, 'after-d2'])
    // A null `next_cursor` is the last page, and the control that would ask for
    // a page that does not exist goes away with it.
    expect(screen.queryByRole('button', { name: /load more/i })).toBeNull()
  })

  it('puts which tab is open in the URL, and opens on Waiting', async () => {
    const router = renderApp('/approvals', [onePage(changes)])

    // URL-AS-STATE. The default tab is the one a bare `/approvals` shows, and
    // it is the waiting inbox — this screen exists for the gates waiting on the
    // reader, and history is what they go looking for.
    expect(await screen.findByText(/nothing is waiting on you/i)).toBeInTheDocument()
    expect(screen.queryByTestId('decision-history')).toBeNull()

    await userEvent.click(screen.getByRole('tab', { name: /history/i }))

    expect(await screen.findByTestId('decision-history')).toBeInTheDocument()
    // The address bar, not just the DOM: a tab implemented as React state would
    // render the same rows while leaving the URL on `/approvals`, and the whole
    // point of a tab that is a link is that the reader can send it to somebody.
    expect(router.state.location.search).toEqual({ tab: 'history' })
    expect(screen.queryByText(/nothing is waiting on you/i)).toBeNull()

    // Which tab is selected is this component's answer, stated on the element
    // that carries the role — see the component for why it is not the router's.
    const historyTab = screen.getByRole('tab', { name: /history/i })
    const waitingTab = screen.getByRole('tab', { name: /waiting/i })
    expect(historyTab).toHaveAttribute('aria-selected', 'true')
    expect(waitingTab).toHaveAttribute('aria-selected', 'false')
    // AND THE ROUTER AGREES WITH IT, which it does not by default. `<Link>`
    // compares its search as a SUBSET of the address bar's, so Waiting — whose
    // search is `{}` — is a subset of `?tab=history`. MEASURED, both ways:
    // with `activeOptions={{ exact: true }}` the pair reads `null` / `"page"`,
    // and with the option deleted it reads `"page"` / `"page"` — two tabs both
    // announcing themselves as the current page. This is the assertion that
    // makes that option falsifiable.
    expect(historyTab).toHaveAttribute('aria-current', 'page')
    expect(waitingTab).not.toHaveAttribute('aria-current')
  })

  it("says the HISTORY is empty in its own words, not the inbox's", async () => {
    // Two empty states that must not collapse into one, for the reason
    // `inbox-empty.tsx` carries: "nothing is waiting on you" told to somebody
    // reading the history says the wrong thing about a screen that is simply
    // young. This runs on `render-app.tsx`'s DEFAULT handler, which answers
    // `decisions: null` — the shape the contract permits (required AND
    // nullable, a Go nil slice marshals to null) and the one a client reading
    // `.length` straight off the response throws on.
    renderApp('/approvals?tab=history')

    expect(await screen.findByText(/no decisions have been made yet/i)).toBeInTheDocument()
    expect(screen.queryByText(/nothing is waiting on you/i)).toBeNull()
  })

  it('an unusable ?tab= lands on the waiting list rather than on nothing', async () => {
    // `validateSearch` CANNOT DROP A KEY — measured twice in this tree
    // (`runs.index.tsx`, `runs.$runId.tsx`): a route's search is its ancestors'
    // merged with its own validated result, nothing between `/` and here
    // declares one, so a rejected value survives the merge. TanStack Router's
    // default parser runs `JSON.parse` per value, so `?tab=42` arrives as the
    // NUMBER 42 and `tab?: string` is a type the runtime is free to violate.
    // The narrow at the read site is what sends it to the default tab instead
    // of to a screen with neither panel on it.
    renderApp('/approvals?tab=42')

    expect(await screen.findByText(/nothing is waiting on you/i)).toBeInTheDocument()
    expect(screen.queryByTestId('decision-history')).toBeNull()
  })
})
