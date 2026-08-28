import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import type { components } from '../api/schema'
import { renderApp } from '../test/render-app'

type ProjectRow = components['schemas']['ApiProjectResponse']
type RepoRow = components['schemas']['ApiRepoResponse']
type RunRow = components['schemas']['ApiRunSummaryResponse']

const project: ProjectRow = {
  id: 'p1',
  name: 'Caesar',
  description: '',
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
}

const repo = (id: string, slug: string): RepoRow => ({
  id,
  project_id: 'p1',
  forge: 'github',
  slug,
  default_branch: 'main',
  credential_id: 'c-forge',
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
})

const primary = repo('r1', 'giiorg/caesar')
const secondary = repo('r2', 'giiorg/forge')

const run = (id: string, title: string): RunRow => ({
  id,
  attempt: 1,
  generation: 1,
  created_at: '2026-08-20T00:00:00Z',
  started_at: '2026-08-20T00:01:00Z',
  finished_at: null,
  phase_summary: [
    { node_id: 'plan', node_type: 'agent', status: 'succeeded', attempt: 1, generation: 1 },
  ],
  project_id: 'p1',
  project_name: 'Caesar',
  repo_slug: 'giiorg/caesar',
  requester_id: 'u1',
  status: 'running',
  task_id: 't1',
  task_title: title,
  workflow_name: 'implement',
})

const theProject = () => http.get('/api/v1/projects/:id', () => HttpResponse.json(project))

const repos = (...rows: RepoRow[]) =>
  http.get('/api/v1/projects/:id/repos', () => HttpResponse.json({ repos: rows }))

/**
 * A runs endpoint that ANSWERS PER `repo_id` and records every filter it was
 * asked for.
 *
 * Keyed on the parameter rather than returning one fixed list, because the
 * property under test is that each repo's activity is fetched with ITS OWN id:
 * a handler answering the same rows regardless would pass against a component
 * that sent the wrong repo's id, or no id at all.
 */
const runsByRepo = (seen: (id: string | null) => void, rows: Record<string, RunRow[]>) =>
  http.get('/api/v1/runs', ({ request }) => {
    const repoId = new URL(request.url).searchParams.get('repo_id')
    seen(repoId)
    return HttpResponse.json({
      runs: repoId === null ? [] : (rows[repoId] ?? []),
      next_cursor: null,
    })
  })

describe('per-repo recent activity', () => {
  it('asks for nothing until a repo is expanded, then filters on that repo', async () => {
    const seen = vi.fn()
    renderApp('/repos/p1', [
      theProject(),
      repos(primary, secondary),
      runsByRepo(seen, { r1: [run('run-1', 'add the egress test')] }),
    ])

    const cards = within(await screen.findByTestId('repo-cards'))

    /**
     * NOTHING FETCHED ON ARRIVAL, and this is the assertion the expansion
     * exists for. Rendering every repo's activity inline would fire one runs
     * query PER REPO on first paint — react-query dedupes the two projections
     * onto one key, but not twenty repos onto one request. One open row at a
     * time bounds it to a single live query, which is also why the open row is
     * hoisted state beside the list rather than state inside a row that exists
     * twice.
     */
    expect(seen).not.toHaveBeenCalled()

    await userEvent.click(cards.getByRole('button', { name: /^activity for giiorg\/caesar$/i }))

    expect(await screen.findByText('add the egress test')).toBeInTheDocument()
    expect(seen).toHaveBeenCalledWith('r1')
    expect(seen).not.toHaveBeenCalledWith('r2')
  })

  it('renders the runs as the same RunCard the lists use, linked to the run', async () => {
    renderApp('/repos/p1', [
      theProject(),
      repos(primary),
      runsByRepo(vi.fn(), { r1: [run('run-1', 'add the egress test')] }),
    ])

    const cards = within(await screen.findByTestId('repo-cards'))
    await userEvent.click(cards.getByRole('button', { name: /^activity for giiorg\/caesar$/i }))

    /**
     * `RunCard` ITSELF, asserted through the testid it already carries, rather
     * than a lookalike built here. The plan's phrase is "exactly as they are
     * today", and the card is what carries the phase dots and the run's own
     * status — neither of which this surface re-derives.
     *
     * NOT Task 6's status line: `PhaseSummaryResponse` is `node_id`,
     * `node_type`, `status`, `attempt`, `generation` and nothing else, so it
     * carries neither `max_attempts` nor `outcome` and `phaseStatusLine` cannot
     * run on a list row at all.
     */
    const card = await screen.findByTestId('run-card')
    expect(card).toHaveAttribute('href', '/runs/run-1')
    expect(within(card).getByText('add the egress test')).toBeInTheDocument()
    expect(within(card).getByText(/running/)).toBeInTheDocument()
  })

  it('shows only the most recent few of a busy repo’s runs', async () => {
    /**
     * SEVEN ON THE WIRE, FIVE ON SCREEN. `GET /runs` answers a whole page and
     * this surface is a glance; without the cap a repo with a page of runs
     * renders all of them under a project detail. Asserting the count here is
     * what makes `RECENT` a guard rather than a number in a comment — the
     * fixtures everywhere else in this file hold one run, against which
     * deleting the slice changes nothing.
     */
    const many = Array.from({ length: 7 }, (_, i) => run(`run-${i}`, `run number ${i}`))
    renderApp('/repos/p1', [theProject(), repos(primary), runsByRepo(vi.fn(), { r1: many })])

    const cards = within(await screen.findByTestId('repo-cards'))
    await userEvent.click(cards.getByRole('button', { name: /^activity for giiorg\/caesar$/i }))

    expect(await screen.findByText('run number 0')).toBeInTheDocument()
    expect(within(screen.getByTestId('repo-activity')).getAllByTestId('run-card')).toHaveLength(5)
    // NAMED, NOT ONLY COUNTED: the sixth is the first casualty of the slice,
    // and a cap applied at the wrong end would keep it.
    expect(screen.queryByText('run number 5')).not.toBeInTheDocument()
  })

  it('closes the open row when its own control is pressed again', async () => {
    renderApp('/repos/p1', [
      theProject(),
      repos(primary),
      runsByRepo(vi.fn(), { r1: [run('run-1', 'add the egress test')] }),
    ])

    const cards = within(await screen.findByTestId('repo-cards'))
    const toggle = cards.getByRole('button', { name: /^activity for giiorg\/caesar$/i })

    await userEvent.click(toggle)
    expect(await screen.findByText('add the egress test')).toBeInTheDocument()

    await userEvent.click(toggle)
    expect(screen.queryByTestId('repo-activity')).not.toBeInTheDocument()
  })

  it('says a repo has no runs rather than showing an empty space', async () => {
    renderApp('/repos/p1', [theProject(), repos(primary), runsByRepo(vi.fn(), {})])

    const cards = within(await screen.findByTestId('repo-cards'))
    await userEvent.click(cards.getByRole('button', { name: /^activity for giiorg\/caesar$/i }))

    expect(await screen.findByText(/no runs have touched this repo/i)).toBeInTheDocument()
  })

  it('closes the open row when another is opened, so one query is live at a time', async () => {
    renderApp('/repos/p1', [
      theProject(),
      repos(primary, secondary),
      runsByRepo(vi.fn(), {
        r1: [run('run-1', 'add the egress test')],
        r2: [run('run-2', 'fix the lease race')],
      }),
    ])

    const cards = within(await screen.findByTestId('repo-cards'))
    await userEvent.click(cards.getByRole('button', { name: /^activity for giiorg\/caesar$/i }))
    expect(await screen.findByText('add the egress test')).toBeInTheDocument()

    await userEvent.click(cards.getByRole('button', { name: /^activity for giiorg\/forge$/i }))

    expect(await screen.findByText('fix the lease race')).toBeInTheDocument()
    expect(screen.queryByText('add the egress test')).not.toBeInTheDocument()
  })
})
