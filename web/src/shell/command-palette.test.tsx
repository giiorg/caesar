import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import type { Task } from '../tasks/tasks-query'
import { renderApp } from '../test/render-app'

const AT = '2026-08-20T00:00:00Z'

const taskRow = (over: Partial<Task> & Pick<Task, 'id'>): Task => ({
  project_id: 'p1',
  repo_id: 'repo-1',
  workflow_version_id: 'wv-1',
  title: 'a task',
  inputs: null,
  requester_id: 'u1',
  status: 'running',
  latest_run_id: 'run-1',
  created_at: AT,
  ...over,
})

const tasks = (rows: Task[]) => http.get('/api/v1/tasks', () => HttpResponse.json({ tasks: rows }))

/** The chord, held the way a person holds it. */
const chord = (key: 'Meta' | 'Control') => userEvent.keyboard(`{${key}>}k{/${key}}`)

const palette = () => screen.findByRole('dialog', { name: /command palette/i })

/**
 * THE PALETTE IS TESTED THROUGH `renderApp`, NOT AS A BARE COMPONENT.
 *
 * Three of its four contracts are only true in the assembled app: the listener
 * lives in `AppShell` and not in the palette, the entries are read from the
 * REAL query cache that the routes fill, and "navigates to the highlighted
 * entry" is a claim about the router's location. A harness that rendered the
 * panel alone with hand-made props would assert none of them, and would pass
 * with the shell's effect deleted.
 */
describe('the command palette', () => {
  it('opens on ⌘K with the input focused', async () => {
    renderApp('/')
    expect(screen.queryByRole('dialog')).toBeNull()
    await chord('Meta')
    expect(await palette()).toBeInTheDocument()
    expect(await screen.findByRole('combobox')).toHaveFocus()
  })

  it('opens on Ctrl+K too', async () => {
    // The same chord on a machine that is not a Mac. Asserted separately
    // because dropping either half of `metaKey || ctrlKey` leaves the other
    // test green, and the missing half is invisible to whoever is not on that
    // platform.
    renderApp('/')
    await chord('Control')
    expect(await palette()).toBeInTheDocument()
  })

  it('offers the page map as a listbox', async () => {
    // The accessibility contract the desktop e2e drives later: a named dialog
    // and options in a listbox, rather than a pile of divs that happens to
    // look like a menu.
    renderApp('/')
    await chord('Meta')
    const list = await screen.findByRole('listbox')
    const options = within(list).getAllByRole('option')
    expect(options.map((o) => o.textContent)).toEqual([
      'Home',
      'Runs',
      'Approvals',
      'Tasks',
      'Workflows',
      'Repos',
      'Admin',
      'New task',
    ])
  })

  it('ArrowDown then Enter navigates to the SECOND entry', async () => {
    // The index matters, and that is the whole design of this assertion. The
    // first entry is Home and the test starts on `/`, so a palette that ignored
    // ArrowDown entirely — or highlighted nothing and let Enter fall through —
    // would leave the location at `/` and pass a weaker assertion. Runs is the
    // second entry and a different place.
    const router = renderApp('/')
    await chord('Meta')
    await palette()
    await userEvent.keyboard('{ArrowDown}{Enter}')
    await waitFor(() => expect(router.state.location.pathname).toBe('/runs'))
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('stops at the LAST entry however far down you hold the arrow', async () => {
    // The high end of the read-site clamp. Twelve presses against eight
    // entries: unclamped, `active` reaches 11, `entries[11]` is undefined and
    // Enter does nothing at all — so the assertion has to be that Enter still
    // ACTS, on the last row, rather than that the location is unchanged.
    const router = renderApp('/', [
      http.get('/api/v1/projects', () => HttpResponse.json({ projects: [] })),
    ])
    await chord('Meta')
    await palette()
    await userEvent.keyboard(`${'{ArrowDown}'.repeat(12)}{Enter}`)
    await waitFor(() => expect(router.state.location.pathname).toBe('/tasks'))
    expect(router.state.location.search).toEqual({ new: 1 })
  })

  it('stops at the FIRST entry however far up you hold the arrow', async () => {
    // The low end, and it needs a starting route that is NOT the first entry's
    // destination. From `/` an unclamped -1 would be a no-op that leaves the
    // location at `/` — which is exactly where a correct clamp also lands, and
    // the test would pass either way. Starting on `/runs` separates them.
    const router = renderApp('/runs')
    await chord('Meta')
    await palette()
    await userEvent.keyboard('{ArrowUp}{ArrowUp}{Enter}')
    await waitFor(() => expect(router.state.location.pathname).toBe('/'))
  })

  it('filters on a substring of a page name', async () => {
    renderApp('/')
    await chord('Meta')
    await userEvent.type(await screen.findByRole('combobox'), 'appro')
    const list = await screen.findByRole('listbox')
    await waitFor(() =>
      expect(
        within(list)
          .getAllByRole('option')
          .map((o) => o.textContent),
      ).toEqual(['Approvals']),
    )
  })

  it('offers a task the cache already holds', async () => {
    // THE PEEK IS WIRED TO THE CACHE THE APP REALLY FILLS. `/tasks` loads
    // `['tasks']` through its loader, so by the time the palette opens the
    // entry is there — with no request of the palette's own, which is what
    // makes a cold palette honest rather than empty-looking.
    renderApp('/tasks', [tasks([taskRow({ id: 't1', title: 'Add revocation' })])])
    await screen.findByText('Add revocation')
    await chord('Meta')
    const list = await screen.findByRole('listbox')
    expect(within(list).getByRole('option', { name: 'Add revocation' })).toBeInTheDocument()
  })

  it('Escape closes it and hands focus back', async () => {
    // The dialog took focus off the trigger to put it in the input; closing
    // must put it back, or a keyboard user is dropped at the top of the
    // document every time they change their mind.
    renderApp('/')
    const trigger = await screen.findByRole('button', { name: '⌘K' })
    await userEvent.click(trigger)
    expect(await screen.findByRole('combobox')).toHaveFocus()
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(trigger).toHaveFocus()
  })

  it('"New task" OPENS THE DRAWER, from a route that is not /tasks', async () => {
    // The entry has to perform its command. Landing on `/tasks` with the
    // drawer shut would be a palette row that walks you to a button and leaves
    // — which is why the drawer moved to `?new=1` in this same task.
    const router = renderApp('/runs', [
      http.get('/api/v1/projects', () => HttpResponse.json({ projects: [] })),
    ])
    await chord('Meta')
    await userEvent.type(await screen.findByRole('combobox'), 'new task')
    await userEvent.keyboard('{Enter}')
    await waitFor(() => expect(router.state.location.pathname).toBe('/tasks'))
    expect(router.state.location.search).toEqual({ new: 1 })
    // BOTH HALVES: the address AND the sheet. The search param alone would
    // pass against a `/tasks` that ignored it, which is exactly the state this
    // feature was in before the drawer left `useState`.
    expect(await screen.findByRole('heading', { name: 'New task' })).toBeInTheDocument()
    expect(screen.queryByRole('dialog', { name: /command palette/i })).toBeNull()
  })

  it('says so when nothing matches', async () => {
    renderApp('/')
    await chord('Meta')
    await userEvent.type(await screen.findByRole('combobox'), 'zzzz')
    expect(await screen.findByText(/nothing matches that/i)).toBeInTheDocument()
    expect(screen.queryByRole('listbox')).toBeNull()
  })
})
