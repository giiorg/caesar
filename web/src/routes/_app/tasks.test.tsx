import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import { onTheWire } from '../../runs/on-the-wire.testkit'
import type { Run } from '../../runs/run-query'
import type { RunSummary } from '../../runs/runs-query'
import type { Task } from '../../tasks/tasks-query'
import { renderApp, scriptedStream } from '../../test/render-app'

const AT = '2026-08-20T00:00:00Z'

/**
 * A task row, annotated `Task` so `tsc --noEmit` forces every key the contract
 * declares. Plan 12a marked `TaskResponse` required throughout — `inputs`
 * included, which is required AND nullable because a Go nil map marshals to
 * null — so this fixture can write the shape the server really sends.
 */
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

/** One task per status, so the four sections all have something in them. */
const everyStatus = (): Task[] =>
  ['waiting', 'running', 'pending', 'succeeded', 'failed', 'superseded', 'dead'].map((status) =>
    taskRow({ id: `task-${status}`, title: `a ${status} task`, status }),
  )

const tasks = (rows: Task[] | (() => Task[])) =>
  http.get('/api/v1/tasks', () =>
    HttpResponse.json({ tasks: typeof rows === 'function' ? rows() : rows }),
  )

const runRow = (over: Partial<RunSummary> & Pick<RunSummary, 'id'>): RunSummary => ({
  task_id: 't1',
  task_title: 'a run',
  project_id: 'p1',
  project_name: 'caesar',
  repo_slug: 'acme/app',
  workflow_name: 'default',
  status: 'running',
  attempt: 1,
  generation: 1,
  requester_id: 'u1',
  created_at: AT,
  started_at: AT,
  finished_at: null,
  phase_summary: [
    { node_id: 'build', node_type: 'implement', status: 'running', attempt: 1, generation: 1 },
  ],
  ...over,
})

const runs = (rows: RunSummary[]) =>
  http.get('/api/v1/runs', () => HttpResponse.json({ runs: rows, next_cursor: null }))

/**
 * A session with no grants AND the stream frame that proves it. `scopeEmpty`
 * is not read off `/auth/me`: the transport answers the ambiguous `forbidden`
 * frame by probing `/auth/me` and only then concluding, which is the path
 * `approvals.test.tsx` established. Both halves are needed or the flag stays
 * false and the screen says the ordinary thing.
 */
const noGrants = () => [
  http.get('/api/v1/auth/me', () =>
    HttpResponse.json({
      user: {
        id: 'u1',
        email: 'user@example.com',
        role: 'member',
        active: true,
        created_at: '',
        updated_at: '',
      },
      grants: [],
    }),
  ),
  http.get(
    '/api/v1/stream',
    () =>
      new HttpResponse('event: forbidden\ndata: {}\n\n', {
        headers: { 'content-type': 'text/event-stream' },
      }),
  ),
]

const projects = () =>
  http.get('/api/v1/projects', () =>
    HttpResponse.json({
      projects: [
        { id: 'p1', name: 'caesar', description: '', created_at: AT, updated_at: AT },
        { id: 'p2', name: 'other', description: '', created_at: AT, updated_at: AT },
      ],
    }),
  )

/**
 * Repos ANSWERING THE PATH PARAMETER, the way the server does. A handler that
 * ignored `{id}` would let the cascade test pass against a form that asked for
 * every repo in the deployment and merely labelled the control "Repo" — the
 * exact defect the cascade exists to prevent.
 */
const repos = () =>
  http.get('/api/v1/projects/:id/repos', ({ params }) =>
    HttpResponse.json({
      repos:
        params.id === 'p1'
          ? [
              {
                id: 'repo-1',
                project_id: 'p1',
                forge: 'github',
                slug: 'acme/app',
                default_branch: 'main',
                credential_id: 'c1',
                created_at: AT,
                updated_at: AT,
              },
            ]
          : [],
    }),
  )

const workflows = () =>
  http.get('/api/v1/workflows', () =>
    HttpResponse.json({
      workflows: [
        {
          id: 'wf-1',
          name: 'implement-and-review',
          description: '',
          latest_version: 3,
          created_at: AT,
          updated_at: AT,
        },
      ],
    }),
  )

/** The run the created task lands on, so the destination route really resolves. */
const createdRun = (): Run => ({
  id: 'run-new',
  task_id: 'task-new',
  task_title: 'Add revocation',
  repo_slug: 'acme/app',
  workflow_name: 'implement-and-review',
  status: 'pending',
  definition_hash: 'h1',
  base_sha: 'abc123',
  policy_snapshot: {},
  generation: 1,
  execution_used_ms: 0,
  wall_deadline: '2099-01-01T00:00:00Z',
  created_at: AT,
  started_at: null,
  finished_at: null,
  artifacts: null,
  publications: null,
  phases: null,
})

const createTaskOk = (seen: { body?: unknown }) => [
  http.post('/api/v1/tasks', async ({ request }) => {
    seen.body = await request.json()
    return HttpResponse.json(
      taskRow({
        id: 'task-new',
        title: 'Add revocation',
        status: 'pending',
        latest_run_id: 'run-new',
      }),
      { status: 201 },
    )
  }),
  http.get('/api/v1/runs/:id', () => HttpResponse.json(onTheWire(createdRun()))),
]

const problem = (status: number, body: Record<string, unknown>) =>
  HttpResponse.json(body, {
    status,
    headers: { 'content-type': 'application/problem+json' },
  })

const createTaskFieldError = () =>
  http.post('/api/v1/tasks', () =>
    problem(400, {
      type: 'about:blank',
      title: 'Invalid request',
      status: 400,
      correlation_id: 'cid-42',
      errors: [{ field: 'title', message: 'title must not be empty' }],
    }),
  )

/**
 * The 404 `createTask` really answers when the forge cannot see the repo
 * (`handleCreateTask` maps an unresolvable repo to ClassNotFound). It names no
 * field, so `applyProblemToForm` has nowhere to route it and it must reach the
 * form-level banner instead of being swallowed.
 */
const createTaskForge404 = () =>
  http.post('/api/v1/tasks', () =>
    problem(404, {
      type: 'about:blank',
      title: 'Not found',
      status: 404,
      detail: 'the repository or its default branch is not visible to caesar',
      correlation_id: 'cid-7',
    }),
  )

const formHandlers = () => [projects(), repos(), workflows()]

async function openForm() {
  await userEvent.click(await screen.findByRole('button', { name: /new task/i }))
  await userEvent.selectOptions(await screen.findByLabelText(/^project$/i), 'p1')
  const repo = await screen.findByLabelText(/^repo$/i)
  // The repo list arrives only after a project is picked, and `selectOptions`
  // throws on an option that is not there yet — so the cascade's second request
  // is awaited by waiting for what it produced.
  await screen.findByRole('option', { name: 'acme/app' })
  await userEvent.selectOptions(repo, 'repo-1')
  await userEvent.selectOptions(await screen.findByLabelText(/^workflow$/i), 'wf-1')
  await userEvent.type(screen.getByLabelText(/^title$/i), 'Add revocation')
  await userEvent.type(screen.getByLabelText(/^instructions$/i), 'Add a session revocation table.')
}

async function openFormAndSubmit() {
  await openForm()
  await userEvent.click(screen.getByRole('button', { name: /^create task$/i }))
}

describe('Tasks', () => {
  it('renders the four locked groups in order', async () => {
    renderApp('/tasks', [tasks(everyStatus()), runs([])])
    const order = (await screen.findAllByRole('heading')).map((h) => h.textContent)
    expect(order).toEqual(['Needs you', 'In flight', 'Up next', 'Recently done'])
  })

  it('puts each of the seven statuses in the group the mapping names', async () => {
    // The mapping's own test proves the function; this proves the SCREEN reads
    // it. Both are needed: a component that grouped by its own inline switch
    // would pass `task-groups.test.ts` untouched and be wrong here.
    renderApp('/tasks', [tasks(everyStatus()), runs([])])
    const group = async (id: string) => within(await screen.findByTestId(`group-${id}`))
    expect((await group('needs-you')).getByText('a waiting task')).toBeInTheDocument()
    expect((await group('in-flight')).getByText('a running task')).toBeInTheDocument()
    expect((await group('up-next')).getByText('a pending task')).toBeInTheDocument()
    const done = await group('recently-done')
    for (const status of ['succeeded', 'failed', 'superseded', 'dead']) {
      expect(done.getByText(`a ${status} task`)).toBeInTheDocument()
    }
  })

  it('designs the empty state that is on screen most of the time', async () => {
    // "Up next" is `pending` and pending lasts seconds here. Its empty state is
    // not an edge case; it is the default appearance of a quarter of this
    // screen.
    renderApp('/tasks', [tasks([]), runs([])])
    expect(await screen.findByText(/nothing queued/i)).toBeInTheDocument()
  })

  it('keeps the two list empty states distinct', async () => {
    // The pair the constitution asks of every list: a statement about the list
    // against a statement about GRANTS. With no projects shared, the four group
    // empty states would each read as "the system is idle" to somebody who
    // simply cannot see anything — the same lie `InboxEmpty` exists to prevent.
    renderApp('/tasks', [tasks([]), runs([]), ...noGrants()])
    expect(await screen.findByText(/no projects are shared with you yet/i)).toBeInTheDocument()
    expect(screen.queryByText(/nothing queued/i)).toBeNull()
  })

  it("shows live dots for a task, from ITS run and not the page's first", async () => {
    // TWO RUNS, AND THE TASK POINTS AT THE SECOND. With one run on the page a
    // row that ignored `latest_run_id` entirely — taking whatever run came
    // first — would render dots and pass. The states are deliberately different
    // so the assertion says WHICH run was matched rather than that some run was.
    renderApp('/tasks', [
      tasks([taskRow({ id: 'task-1', title: 'has a run', latest_run_id: 'run-2' })]),
      runs([
        runRow({
          id: 'run-1',
          phase_summary: [
            {
              node_id: 'build',
              node_type: 'implement',
              status: 'succeeded',
              attempt: 1,
              generation: 1,
            },
          ],
        }),
        runRow({ id: 'run-2' }),
      ]),
    ])
    const dots = await screen.findByTestId('phase-dots')
    expect(within(dots).getAllByRole('listitem')[0]).toHaveAttribute('data-state', 'running')
  })

  it('shows the task WITHOUT dots when its latest run is not on the page', async () => {
    // GET /tasks is unpaginated and GET /runs is one page, so an old task's run
    // can be absent. The task must still be listed and must not get invented
    // dots — a row of grey dots on a succeeded run reads as "never started".
    renderApp('/tasks', [
      tasks([taskRow({ id: 'task-1', title: 'an older task', latest_run_id: 'run-999' })]),
      runs([]),
    ])
    expect(await screen.findByText(/an older task/i)).toBeInTheDocument()
    expect(screen.queryByTestId('phase-dots')).toBeNull()
    // AND NOT `PhaseDots`' EMPTY RENDERING EITHER, which is the assertion that
    // does the work here — MEASURED. `PhaseDots` answers a null or empty array
    // with "No phases reported for this run", so the obvious mutant
    // (`<PhaseDots phases={run?.phase_summary ?? []} />`, dots for every row)
    // draws no dots and SURVIVED the testid check alone. That sentence is a
    // claim about the run's shape not arriving, and a task whose run is simply
    // not on this page has made no such claim.
    expect(screen.queryByText(/no phases reported/i)).toBeNull()
    // And it says nothing else it cannot know. `repo_slug` and `workflow_name`
    // live on the RUN summary, never on the task, so a row without its run has
    // no business naming either.
    expect(screen.queryByText(/acme\/app/)).toBeNull()
  })

  it('moves a task between groups when a frame says its run changed', async () => {
    // THE GROUPS ARE LIVE, and the mechanism is the cache bridge invalidating
    // `['tasks']` — a task's group comes from `TaskResponse.Status`, which is
    // its latest run's status, so a frame that changes a run changes what this
    // screen shows. Without that invalidation the row sits in the wrong section
    // until the bounded refresh floor fires a minute later, and nothing else in
    // the tree would notice.
    const stream = scriptedStream()
    let calls = 0
    renderApp('/tasks', [
      tasks(() => {
        calls += 1
        return [
          taskRow({
            id: 'task-1',
            title: 'changes group',
            status: calls === 1 ? 'running' : 'succeeded',
          }),
        ]
      }),
      runs([]),
      stream.handler,
    ])
    const inFlight = await screen.findByTestId('group-in-flight')
    expect(within(inFlight).getByText('changes group')).toBeInTheDocument()

    await stream.emit({ kind: 'run.succeeded', run_id: 'run-1' })
    await waitFor(
      () =>
        expect(
          within(screen.getByTestId('group-recently-done')).getByText('changes group'),
        ).toBeInTheDocument(),
      { timeout: 5_000 },
    )
  })

  it('the FAB creates a task and lands on its run', async () => {
    const seen: { body?: unknown } = {}
    const router = renderApp('/tasks', [
      tasks([]),
      runs([]),
      ...formHandlers(),
      ...createTaskOk(seen),
    ])
    await openFormAndSubmit()
    await waitFor(() => expect(router.state.location.pathname).toMatch(/^\/runs\//))
    // THE VERSION IS THE WORKFLOW'S LATEST, and it is asserted because nothing
    // on screen shows it: `CreateTaskRequest.workflow_version` is an int32 the
    // form has to read off `WorkflowResponse.latest_version`, and a client that
    // sent 0 or 1 would create runs against the wrong pinned definition while
    // every visible assertion here stayed green.
    //
    // `instructions` rides in `inputs` because `CreateTaskRequest` has no
    // instructions field — `inputs` is the task-level map the definition's
    // nodes read.
    expect(seen.body).toEqual({
      repo_id: 'repo-1',
      workflow_id: 'wf-1',
      workflow_version: 3,
      title: 'Add revocation',
      inputs: { instructions: 'Add a session revocation table.' },
    })
  })

  it('opens the drawer from the URL alone', async () => {
    // THE WHOLE POINT OF MOVING IT OUT OF `useState`: the command palette
    // cannot reach a component's local state, so "New task" as a palette entry
    // is only possible if the open drawer is an address. It is also now
    // linkable and survives a reload, which the local-state version was not.
    renderApp('/tasks?new=1', [tasks([]), runs([]), ...formHandlers()])
    expect(await screen.findByRole('heading', { name: 'New task' })).toBeInTheDocument()
    expect(await screen.findByLabelText(/^title$/i)).toBeInTheDocument()
  })

  it('leaves the drawer CLOSED for a value that is not 1', async () => {
    // THE READ-SITE NARROW, AND THE MUTANT IT EXISTS FOR: `search.new !==
    // undefined`. `validateSearch` cannot drop a key — a route's search is its
    // ancestors' merged over its own validated result, and nothing above
    // `/tasks` validates anything — so `2` is rejected by the schema and STILL
    // arrives here. The widened narrow opens the drawer on it and passes every
    // other test in this file.
    renderApp('/tasks?new=2', [tasks([]), runs([]), ...formHandlers()])
    // Awaited on the list, not on an absence: `queryByRole` before the route
    // has rendered anything would answer null for a screen that has not
    // painted yet, which is a pass for any implementation.
    expect(await screen.findByText(/nothing queued/i)).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: 'New task' })).toBeNull()
    expect(screen.queryByLabelText(/^title$/i)).toBeNull()
  })

  it('the FAB writes the drawer into the address', async () => {
    // The other direction: opening by hand must produce the URL the palette
    // navigates to, or the two entry paths are two different features that
    // happen to look alike.
    const router = renderApp('/tasks', [tasks([]), runs([]), ...formHandlers()])
    await userEvent.click(await screen.findByRole('button', { name: /new task/i }))
    await waitFor(() => expect(router.state.location.search).toEqual({ new: 1 }))
  })

  it('Cancel takes the param back out', async () => {
    // Every way out of the sheet is one `onOpenChange(false)` in vaul, so this
    // covers Escape and the swipe with it. A drawer that closed on screen but
    // left `?new=1` in the address would re-open on the next reload — and on
    // the back button, which is the same bug arriving faster.
    const router = renderApp('/tasks?new=1', [tasks([]), runs([]), ...formHandlers()])
    await userEvent.click(await screen.findByRole('button', { name: /^cancel$/i }))
    await waitFor(() => expect(router.state.location.search).toEqual({}))
  })

  it('asks only for the repos of the project that was picked', async () => {
    // The cascade, and the half a single-project fixture cannot see: `p2` has no
    // repos, so a form that fetched every repo in the deployment would still
    // offer `acme/app` here.
    renderApp('/tasks', [tasks([]), runs([]), ...formHandlers()])
    await userEvent.click(await screen.findByRole('button', { name: /new task/i }))
    await userEvent.selectOptions(await screen.findByLabelText(/^project$/i), 'p2')
    expect(await screen.findByText(/no repos are registered on that project/i)).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'acme/app' })).toBeNull()
  })

  it('routes a server field error back onto its field', async () => {
    // Plan 11's problem-to-fields, unchanged. The assertion is on the field's
    // accessible DESCRIPTION rather than on any text being somewhere in the
    // document: that is `aria-describedby` pointing at the message, which is
    // the whole difference between an error a screen reader announces with the
    // input and one that merely exists on the page.
    renderApp('/tasks', [tasks([]), runs([]), ...formHandlers(), createTaskFieldError()])
    await openFormAndSubmit()
    const title = await screen.findByLabelText(/^title$/i)
    await waitFor(() => expect(title).toHaveAccessibleDescription(/title must not be empty/i))
    // And the reference survives a fully-routed problem, which is the shape
    // that loses it most easily: every error mapped to a field, so
    // `applyProblemToForm` returns no form-level message at all.
    expect(screen.getByText(/cid-42/)).toBeInTheDocument()
  })

  it('surfaces a forge failure as itself', async () => {
    renderApp('/tasks', [tasks([]), runs([]), ...formHandlers(), createTaskForge404()])
    await openFormAndSubmit()
    expect(await screen.findByText(/repository or its default branch/i)).toBeInTheDocument()
    expect(screen.getByText(/cid-7/)).toBeInTheDocument()
  })
})
