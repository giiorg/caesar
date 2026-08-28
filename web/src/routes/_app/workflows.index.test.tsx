import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import type { components } from '../../api/schema'
import { renderApp } from '../../test/render-app'

type WorkflowRow = components['schemas']['ApiWorkflowResponse']

const workflow = (over: Partial<WorkflowRow> & Pick<WorkflowRow, 'id' | 'name'>): WorkflowRow => ({
  description: '',
  latest_version: 3,
  created_at: '2026-08-20T00:00:00Z',
  updated_at: '2026-08-20T00:00:00Z',
  ...over,
})

const conveyor = workflow({ id: 'w1', name: 'conveyor', description: 'plan → implement → gate' })
/** The half-created workflow: metadata written, definition never registered. */
const halfMade = workflow({ id: 'w2', name: 'draft-only', latest_version: 0 })

const list = (...rows: WorkflowRow[]) =>
  http.get('/api/v1/workflows', () => HttpResponse.json({ workflows: rows }))

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

const nameTaken = () =>
  HttpResponse.json(
    {
      title: 'Conflict',
      status: 409,
      detail: 'workflow name already taken',
      correlation_id: 'cid-dup',
    },
    { status: 409, headers: { 'content-type': 'application/problem+json' } },
  )

const openNewWorkflow = async () => {
  await userEvent.click(await screen.findByRole('button', { name: /new workflow/i }))
  await userEvent.type(await screen.findByLabelText(/^name$/i), 'conveyor')
  await userEvent.type(screen.getByLabelText(/^description$/i), 'plan → implement → gate')
}

describe('/workflows', () => {
  /**
   * NO HANDLER OF ITS OWN, deliberately. `render-app.tsx`'s default answers
   * `workflows: null` — the shape the contract permits and a Go nil slice
   * produces — so the `?? []` in `workflows-query.ts` is on this test's path
   * rather than unreachable from every test in the tree. Overriding it here
   * with `[]` is the Task 19 trap: the coalesce would then be deletable with
   * nothing going red.
   */
  it('says the deployment has no workflows, and tells an admin to make one', async () => {
    renderApp('/workflows')

    expect(
      await screen.findByText(/no workflows are defined yet — create the first/i),
    ).toBeInTheDocument()
  })

  /**
   * THE SECOND EMPTY SENTENCE, and the discriminator is NOT what the list
   * means. `GET /workflows` is plain `authenticated` and scopes nothing, so an
   * empty list is the same fact for both readers; `POST /workflows` is
   * `adminOnly`, so only the next move differs. "Create the first" shown here
   * would send someone to a form the API answers 403 to.
   */
  it('tells a member who defines workflows instead of telling them to', async () => {
    renderApp('/workflows', [memberSession()])

    expect(await screen.findByText(/an admin defines them/i)).toBeInTheDocument()
    expect(screen.queryByText(/create the first/i)).not.toBeInTheDocument()
    // And no FAB: a control the server refuses is a control that lies at the
    // moment it is pressed.
    expect(screen.queryByRole('button', { name: /new workflow/i })).not.toBeInTheDocument()
  })

  it('renders a card per workflow, with the latest version and only a real description', async () => {
    renderApp('/workflows', [list(conveyor, halfMade)])

    const rows = within(await screen.findByTestId('workflow-cards')).getAllByRole('listitem')
    expect(rows).toHaveLength(2)
    expect(rows[0]).toHaveTextContent('conveyor')
    expect(rows[0]).toHaveTextContent('plan → implement → gate')
    expect(rows[0]).toHaveTextContent('v3')
    // The undescribed one says its name and its version and invents nothing
    // between them.
    expect(rows[1]).toHaveTextContent('draft-only')
    // COUNTED, NOT MERELY ABSENT. `not.toHaveTextContent` passes against an
    // EMPTY <p> as readily as against no <p> at all — Task 18's empty-element
    // species — so an unconditional description would survive it. Two
    // paragraphs on the described card, two on the bare one only if a third is
    // being rendered blank.
    expect(within(rows[0] as HTMLElement).getAllByRole('paragraph')).toHaveLength(3)
    expect(within(rows[1] as HTMLElement).getAllByRole('paragraph')).toHaveLength(2)
  })

  /**
   * `latest_version: 0` IS A LEGAL ROW — the honest state between the two
   * writes — and its card must say so and resume the editor rather than open a
   * detail screen with an empty history.
   *
   * The two assertions are a pair on purpose. The sentence alone would pass
   * against a card that then linked to the detail screen, and the href alone
   * would pass against a card that said "v0".
   */
  it('names a workflow with no versions and sends it back to the editor', async () => {
    renderApp('/workflows', [list(halfMade, conveyor)])

    const rows = within(await screen.findByTestId('workflow-cards')).getAllByRole('listitem')
    expect(rows[0]).toHaveTextContent('no versions yet — finish setup')
    expect(within(rows[0] as HTMLElement).getByRole('link')).toHaveAttribute(
      'href',
      '/workflows/w2/edit',
    )
    // Its neighbour, which HAS versions, goes to the detail screen instead.
    expect(within(rows[1] as HTMLElement).getByRole('link')).toHaveAttribute(
      'href',
      '/workflows/w1',
    )
    expect(rows[1]).not.toHaveTextContent('no versions yet')
  })

  /**
   * THE FIRST OF THE TWO WRITES, and the navigation is the whole point of the
   * test: `POST /workflows` stores a name, and the definition is a separate
   * write the editor makes. A form that closed its sheet here would leave the
   * operator looking at a list, with the second write unmade and nothing on
   * screen saying so.
   */
  it('creates the metadata, then opens the editor on the new id', async () => {
    const posted = vi.fn()
    const router = renderApp('/workflows', [
      list(),
      http.post('/api/v1/workflows', async ({ request }) => {
        posted(await request.json())
        return HttpResponse.json(workflow({ id: 'w9', name: 'conveyor', latest_version: 0 }), {
          status: 201,
        })
      }),
    ])

    await openNewWorkflow()
    await userEvent.click(screen.getByRole('button', { name: /^create workflow$/i }))

    // THE BODY, not merely that a request happened: a form that dropped
    // `description` would still POST and still navigate.
    expect(posted).toHaveBeenCalledWith({
      name: 'conveyor',
      description: 'plan → implement → gate',
    })
    // THE ID FROM THE RESPONSE, not from anything the form had. A navigation
    // built from a value the client already held would land on the wrong
    // workflow and this assertion is what tells the two apart.
    await waitFor(() => expect(router.state.location.pathname).toBe('/workflows/w9/edit'))
  })

  it('puts a duplicate-name conflict in the form banner, with its correlation id', async () => {
    renderApp('/workflows', [list(), http.post('/api/v1/workflows', nameTaken)])

    await openNewWorkflow()
    await userEvent.click(screen.getByRole('button', { name: /^create workflow$/i }))

    /**
     * THE BANNER IS THE ONLY PLACE THIS CAN LAND, and that is the server's
     * shape rather than a client choice: `CreateWorkflow` raises
     * `xerrors.E(ClassConflict, "workflow name already taken")` with NO
     * `WithFields` (`workflows/workflows.go`), so the problem document carries
     * no `errors[]` and `applyProblemToForm` takes its no-fields arm.
     */
    const banner = await screen.findByRole('alert')
    expect(banner).toHaveTextContent('workflow name already taken')
    expect(banner).toHaveTextContent('cid-dup')
    expect(screen.getByLabelText(/^name$/i)).toHaveAttribute('aria-invalid', 'false')
  })
})
