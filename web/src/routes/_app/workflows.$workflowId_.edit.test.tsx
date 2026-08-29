import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { beforeEach, describe, expect, it } from 'vitest'
import { renderApp } from '../../test/render-app'
import { draftKey } from '../../workflows/drafts'

const harnesses = (names: string[] = ['claude-code', 'codex']) =>
  http.get('/api/v1/harnesses', () =>
    HttpResponse.json({
      harnesses: names.map((name) => ({
        id: name,
        name,
        kind: 'claude-code',
        image_ref: 'img',
        provider_endpoint: '',
        credential_id: 'c1',
        defaults: {},
        created_at: '',
        updated_at: '',
      })),
    }),
  )

/** A chain-shaped version — one the editor can round-trip and therefore prefill from. */
const CHAIN = {
  schema: 2,
  nodes: [
    { id: 'plan-1', type: 'plan', harness: 'claude-code', instructions: 'draft it' },
    { id: 'impl-1', type: 'implement', harness: 'claude-code', instructions: 'do it' },
    { id: 'gate-1', type: 'gate' },
    { id: 'publish-1', type: 'git-publish' },
  ],
  edges: [
    { from: 'plan-1', to: 'impl-1', artifact: 'plan' },
    { from: 'impl-1', to: 'gate-1', artifact: 'patch' },
    { from: 'gate-1', to: 'publish-1', artifact: 'patch' },
  ],
}

/**
 * TWO IMPLEMENTS OFF ONE PLAN — API-authorable (`Validate`: 0 violations) and
 * not representable as an ordered card list, so `isChainShaped` answers false.
 */
const BRANCHING = {
  schema: 2,
  nodes: [
    { id: 'plan-1', type: 'plan', harness: 'cc', instructions: 'x' },
    { id: 'impl-a', type: 'implement', harness: 'cc', instructions: 'x' },
    { id: 'impl-b', type: 'implement', harness: 'cc', instructions: 'x' },
    { id: 'gate-1', type: 'gate' },
    { id: 'publish-1', type: 'git-publish' },
  ],
  edges: [
    { from: 'plan-1', to: 'impl-a', artifact: 'plan' },
    { from: 'plan-1', to: 'impl-b', artifact: 'plan' },
    { from: 'impl-a', to: 'gate-1', artifact: 'patch' },
    { from: 'gate-1', to: 'publish-1', artifact: 'patch' },
  ],
}

const version = (document: unknown) =>
  http.get('/api/v1/workflows/:id/versions/:version', ({ params }) =>
    HttpResponse.json({
      workflow_id: 'w1',
      version: Number(params.version),
      content_hash: 'a'.repeat(64),
      document,
      created_at: '2026-08-21T09:00:00Z',
    }),
  )

beforeEach(() => localStorage.clear())

describe('/workflows/$workflowId/edit', () => {
  it('opens an empty editor that authors a NEW version', async () => {
    renderApp('/workflows/w1/edit', [harnesses()])

    expect(await screen.findByRole('heading', { name: 'New version' })).toBeInTheDocument()
    // The one control an empty outline has: the menu that starts it.
    expect(screen.getByRole('combobox', { name: 'Add the first step' })).toBeInTheDocument()
  })

  /**
   * THE PREVIEW IS LIVE, AND PURE. Adding a card redraws the DAG with no
   * request in between — `toDocument` is a function of the cards — which is
   * what locked variant B means by putting the preview in the editor.
   */
  it('redraws the preview as cards are added, without fetching', async () => {
    const user = userEvent.setup()
    renderApp('/workflows/w1/edit', [harnesses()])

    await user.selectOptions(
      await screen.findByRole('combobox', { name: 'Add the first step' }),
      'implement',
    )
    expect(screen.getByRole('img', { name: '1 step: implement' })).toBeInTheDocument()

    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Add a step at the end' }),
      'implement',
    )
    expect(screen.getByRole('img', { name: '2 steps: implement, implement' })).toBeInTheDocument()
  })

  it('offers the registered harnesses on an agentic card', async () => {
    const user = userEvent.setup()
    renderApp('/workflows/w1/edit', [harnesses(['claude-code', 'codex'])])

    await user.selectOptions(
      await screen.findByRole('combobox', { name: 'Add the first step' }),
      'plan',
    )
    const picker = await screen.findByRole('combobox', { name: 'Harness' })
    expect([...picker.querySelectorAll('option')].map((o) => o.value)).toEqual([
      '',
      'claude-code',
      'codex',
    ])
  })

  describe('prefill', () => {
    it('populates the outline from a chain-shaped version', async () => {
      renderApp('/workflows/w1/edit?from=3', [harnesses(), version(CHAIN)])

      expect(await screen.findByText('Starting from v3.')).toBeInTheDocument()
      const ids = screen
        .getAllByRole('textbox', { name: 'Step id' })
        .map((i) => (i as HTMLInputElement).value)
      expect(ids).toEqual(['plan-1', 'impl-1', 'gate-1', 'publish-1'])
      expect(screen.getAllByRole('combobox', { name: 'Harness' })[0]).toHaveValue('claude-code')
    })

    /**
     * A NON-CHAIN VERSION OPENS READ-ONLY, with the drawing and the outline and
     * NO card controls. Opening it editable would silently rewrite it on save
     * into the nearest thing an ordered card list can say — here, dropping one
     * of the two implements' plan edges.
     */
    it('refuses a document it cannot round-trip into read-only mode', async () => {
      renderApp('/workflows/w1/edit?from=4', [harnesses(), version(BRANCHING)])

      expect(await screen.findByText(/authored outside the editor/i)).toBeInTheDocument()
      // The drawing and the rows are both there — read-only is not "refused".
      expect(screen.getByRole('img', { name: /5 steps/ })).toBeInTheDocument()
      expect(
        screen.getByRole('list', { name: 'Steps' }).querySelectorAll('[data-node-type]'),
      ).toHaveLength(5)

      // And nothing to type into.
      expect(screen.queryByRole('textbox')).toBeNull()
      expect(screen.queryByRole('combobox')).toBeNull()
      expect(screen.queryByRole('button', { name: /^Remove/ })).toBeNull()
    })

    /**
     * `validateSearch` REJECTING A VALUE DOES NOT REMOVE IT — measured twice in
     * this tree — so `?from=nope` really reaches the read site. It lands where
     * "no version asked for" lands: a fresh editor, NOT an error about a
     * version nobody named. The absence of a fetch is what proves the narrow
     * ran: `onUnhandledRequest: 'error'` would fail this test if the loader had
     * asked for `/versions/undefined`.
     */
    it('opens a fresh editor for a from= that is not a version ordinal', async () => {
      renderApp('/workflows/w1/edit?from=nope', [harnesses()])

      expect(await screen.findByRole('heading', { name: 'New version' })).toBeInTheDocument()
      expect(screen.getByRole('combobox', { name: 'Add the first step' })).toBeInTheDocument()
      expect(screen.queryByText(/Starting from/)).toBeNull()
    })

    it('says so when the version it was asked to start from cannot be read', async () => {
      renderApp('/workflows/w1/edit?from=2', [harnesses(), version({ schema: 99 })])

      expect(
        await screen.findByText(/could not be read by this version of the app/i),
      ).toBeInTheDocument()
      expect(screen.queryByRole('combobox', { name: 'Add the first step' })).toBeNull()
    })
  })

  describe('drafts', () => {
    /**
     * A DRAFT IS RESTORED ON ARRIVAL — the whole point of §6.4's localStorage
     * draft, "so a phone tab death doesn't eat ten cards of typing". This
     * mounts the route cold against a draft written by an earlier session.
     */
    it('restores an unsaved draft, and says it did', async () => {
      localStorage.setItem(
        draftKey('w1'),
        JSON.stringify({
          schema: 2,
          nodes: [{ id: 'impl-1', type: 'implement', harness: 'codex', instructions: 'half done' }],
        }),
      )
      renderApp('/workflows/w1/edit', [harnesses()])

      expect(await screen.findByText(/Restored from an unsaved draft/i)).toBeInTheDocument()
      expect(screen.getByRole('textbox', { name: 'Step id' })).toHaveValue('impl-1')
      expect(screen.getByRole('textbox', { name: 'Instructions' })).toHaveValue('half done')
    })

    /**
     * THE DRAFT OUTRANKS THE PREFILL. A draft is work this person did and did
     * not save; the version is still on the server and coming back reproduces
     * it. Restoring the draft is what the feature is for, and it would buy
     * nothing if arriving with `?from=` threw it away.
     */
    it('prefers the draft over the version named in the address', async () => {
      localStorage.setItem(
        draftKey('w1'),
        JSON.stringify({ schema: 2, nodes: [{ id: 'impl-9', type: 'implement' }] }),
      )
      renderApp('/workflows/w1/edit?from=3', [harnesses(), version(CHAIN)])

      expect(await screen.findByText(/Restored from an unsaved draft/i)).toBeInTheDocument()
      expect(
        screen
          .getAllByRole('textbox', { name: 'Step id' })
          .map((i) => (i as HTMLInputElement).value),
      ).toEqual(['impl-9'])
    })

    /** Storage this build cannot read is discarded, not surfaced — see `readDraft`. */
    it('ignores a draft it cannot read', async () => {
      localStorage.setItem(draftKey('w1'), 'not json{')
      renderApp('/workflows/w1/edit', [harnesses()])

      expect(
        await screen.findByRole('combobox', { name: 'Add the first step' }),
      ).toBeInTheDocument()
      expect(screen.queryByText(/Restored from an unsaved draft/i)).toBeNull()
    })

    /** Per workflow, so two workflows' unsaved work do not overwrite each other. */
    it('does not restore another workflow’s draft', async () => {
      localStorage.setItem(
        draftKey('w2'),
        JSON.stringify({ schema: 2, nodes: [{ id: 'impl-9', type: 'implement' }] }),
      )
      renderApp('/workflows/w1/edit', [harnesses()])

      expect(
        await screen.findByRole('combobox', { name: 'Add the first step' }),
      ).toBeInTheDocument()
      expect(screen.queryByText(/Restored from an unsaved draft/i)).toBeNull()
    })
  })

  /**
   * `createWorkflowVersion` IS `adminOnly`, so a member is told rather than
   * handed an editor whose save answers 403 — `workflows.index.tsx`'s call
   * about its New button, applied to the screen that button opens.
   *
   * NO HARNESS HANDLER IS SUPPLIED, and that is half the assertion: `GET
   * /harnesses` is admin-only too, and `renderApp`'s default would answer it.
   * What this really pins is that the screen renders its refusal without the
   * editor mounting — the outline is what would have fetched.
   */
  it('refuses a member, without opening an editor', async () => {
    renderApp('/workflows/w1/edit', [
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
          grants: [],
        }),
      ),
    ])

    expect(await screen.findByRole('alert')).toHaveTextContent(/admin-only/i)
    expect(screen.queryByRole('combobox', { name: 'Add the first step' })).toBeNull()
  })

  /**
   * SAVE.
   *
   * Every case below OPENS FROM A DRAFT rather than clicking an outline
   * together, and that is a choice about what is under test: building
   * `plan · implement · gate · git-publish` through the menus is five
   * interactions this file already covers one by one, and repeating them in
   * front of each save assertion would put the add-step menu on the failure
   * path of every rejection test. `readDraft` is production code with its own
   * suite; here it is the fixture loader.
   */
  describe('save', () => {
    const created = (status: number, version: number) =>
      http.post('/api/v1/workflows/:id/versions', () =>
        HttpResponse.json(
          {
            workflow_id: 'w1',
            version,
            content_hash: 'a'.repeat(64),
            document: CHAIN,
            created_at: '2026-08-29T09:00:00Z',
          },
          { status },
        ),
      )

    const rejected = (body: Record<string, unknown>) =>
      http.post('/api/v1/workflows/:id/versions', () =>
        HttpResponse.json(body, {
          status: 400,
          headers: { 'content-type': 'application/problem+json' },
        }),
      )

    it('posts the derived document, lands on the new version, and clears the draft', async () => {
      const user = userEvent.setup()
      const posted: unknown[] = []
      localStorage.setItem(draftKey('w1'), JSON.stringify(CHAIN))
      renderApp('/workflows/w1/edit', [
        harnesses(),
        http.post('/api/v1/workflows/:id/versions', async ({ request }) => {
          posted.push(await request.json())
          return HttpResponse.json(
            {
              workflow_id: 'w1',
              version: 7,
              content_hash: 'a'.repeat(64),
              document: CHAIN,
              created_at: '2026-08-29T09:00:00Z',
            },
            { status: 201 },
          )
        }),
        version(CHAIN),
      ])

      await user.click(await screen.findByRole('button', { name: 'Save version' }))

      // The version view, reached by navigation — not a banner claiming success.
      expect(await screen.findByRole('heading', { name: 'v7' })).toBeInTheDocument()
      // THE BODY IS THE ROUND TRIP: this document came off the wire, through
      // `toCard`, through the reducer's state, and back through `toDocument`.
      expect(posted).toEqual([{ document: CHAIN }])
      expect(localStorage.getItem(draftKey('w1'))).toBeNull()
    })

    /**
     * THE UNDECLARED 200. `createWorkflowVersion` answers 201 for a new
     * document and 200 with the EXISTING version when the content hash already
     * exists (`workflows/api/api.go`, `workflows.go`'s `GetVersionByHash`
     * branch) — and `httpx.Operation.Status` holds one value, so the generated
     * types predict only the 201. Saving an unchanged outline is the ordinary
     * way to reach it: open v3, change nothing, save.
     *
     * A save path keyed on `status === 201` would strand that person on the
     * editor with no error and no navigation, which is the one outcome worse
     * than a visible failure.
     */
    it('treats the idempotent 200 replay as success', async () => {
      const user = userEvent.setup()
      localStorage.setItem(draftKey('w1'), JSON.stringify(CHAIN))
      renderApp('/workflows/w1/edit', [harnesses(), created(200, 3), version(CHAIN)])

      await user.click(await screen.findByRole('button', { name: 'Save version' }))

      expect(await screen.findByRole('heading', { name: 'v3' })).toBeInTheDocument()
      expect(localStorage.getItem(draftKey('w1'))).toBeNull()
    })

    /**
     * A REJECTION LANDS ON THE CARD IT IS ABOUT, and the mapping is POSITIONAL
     * — `Violation.Path` is `nodes[1].instructions`, an index into the array
     * this editor posted, which is why the card order and the node order are
     * the same list.
     */
    it('binds a nodes[i] field error onto that card', async () => {
      const user = userEvent.setup()
      localStorage.setItem(draftKey('w1'), JSON.stringify(CHAIN))
      renderApp('/workflows/w1/edit', [
        harnesses(),
        rejected({
          title: 'Invalid request',
          status: 400,
          detail: 'definition is invalid',
          correlation_id: 'corr-1',
          errors: [{ field: 'nodes[1].instructions', message: 'must be 1-8192 bytes' }],
        }),
      ])

      await user.click(await screen.findByRole('button', { name: 'Save version' }))

      const cards = await screen.findAllByRole('listitem')
      expect(within(cards[1] as HTMLElement).getByRole('alert')).toHaveTextContent(
        'instructions: must be 1-8192 bytes',
      )
      // Not on its neighbours, and not doubled onto the banner.
      expect(within(cards[0] as HTMLElement).queryByRole('alert')).toBeNull()
      expect(screen.queryByText('definition is invalid')).toBeNull()
      // The reference survives a routed rejection — see `FormError`.
      expect(screen.getByText(/corr-1/)).toBeInTheDocument()
    })

    /**
     * A FIELD ERROR THAT NAMES NO CARD GOES TO THE BANNER. `ParseDocument`'s
     * own rejections carry `document` and `schema`, and `validateShape` writes
     * a bare `nodes` for the count rules — none of them is a card, and
     * inventing one to hang the sentence on would blame a step at random.
     */
    it('sends a non-positional field error to the banner', async () => {
      const user = userEvent.setup()
      localStorage.setItem(draftKey('w1'), JSON.stringify(CHAIN))
      renderApp('/workflows/w1/edit', [
        harnesses(),
        rejected({
          title: 'Invalid request',
          status: 400,
          detail: 'definition is invalid',
          correlation_id: 'corr-2',
          errors: [{ field: 'document', message: 'must be at most 1048576 bytes' }],
        }),
      ])

      await user.click(await screen.findByRole('button', { name: 'Save version' }))

      expect(await screen.findByText('must be at most 1048576 bytes')).toBeInTheDocument()
      expect(
        screen.getAllByRole('listitem').every((card) => within(card).queryByRole('alert') === null),
      ).toBe(true)
    })

    /**
     * A MIXED REJECTION SHOWS BOTH HALVES, and the unplaceable half wins the
     * banner even though something else routed. `applyProblemToForm` made the
     * same call for the same reason: a message this screen cannot put next to a
     * card is still the server's explanation, and dropping it because a
     * DIFFERENT error found a home leaves the person reading a partial account
     * of why their save failed.
     *
     * `nodes[9]` is the second half deliberately — a path that IS positional
     * and names no card this outline has. It is the branch a mapper that
     * trusted the index would put on a card that does not exist, or drop
     * silently.
     */
    it('shows a card error and an unplaceable one together', async () => {
      const user = userEvent.setup()
      localStorage.setItem(draftKey('w1'), JSON.stringify(CHAIN))
      renderApp('/workflows/w1/edit', [
        harnesses(),
        rejected({
          title: 'Invalid request',
          status: 400,
          detail: 'definition is invalid',
          correlation_id: 'corr-4',
          errors: [
            { field: 'nodes[1].instructions', message: 'must be 1-8192 bytes' },
            { field: 'nodes[9].harness', message: 'must name a registered harness' },
          ],
        }),
      ])

      await user.click(await screen.findByRole('button', { name: 'Save version' }))

      expect(await screen.findByText('must name a registered harness')).toBeInTheDocument()
      const cards = screen.getAllByRole('listitem')
      expect(within(cards[1] as HTMLElement).getByRole('alert')).toHaveTextContent(
        'instructions: must be 1-8192 bytes',
      )
    })

    /**
     * PARSE-SHAPED REJECTIONS CARRY NO FIELDS AT ALL — detail
     * `"invalid definition document"` and nothing else (pre-flight 12) — so the
     * banner is the only place they can go, and a save path that only knew how
     * to place field errors would refuse silently.
     */
    it('shows a detail-only rejection on the banner', async () => {
      const user = userEvent.setup()
      localStorage.setItem(draftKey('w1'), JSON.stringify(CHAIN))
      renderApp('/workflows/w1/edit', [
        harnesses(),
        rejected({
          title: 'Invalid request',
          status: 400,
          detail: 'invalid definition document',
          correlation_id: 'corr-3',
        }),
      ])

      await user.click(await screen.findByRole('button', { name: 'Save version' }))

      expect(await screen.findByText('invalid definition document')).toBeInTheDocument()
      expect(screen.getByText(/corr-3/)).toBeInTheDocument()
    })

    /**
     * A DROPPED CONNECTION IS NOT A REJECTION. `unwrap` turns every answer the
     * server gives into an `ApiError`, so the only way past it is `fetch`
     * itself failing — no status, no problem document, no correlation id,
     * nothing that names a card. The banner says what happened instead of
     * blaming a step at random.
     */
    it('says so when the request never reached the server', async () => {
      const user = userEvent.setup()
      localStorage.setItem(draftKey('w1'), JSON.stringify(CHAIN))
      renderApp('/workflows/w1/edit', [
        harnesses(),
        http.post('/api/v1/workflows/:id/versions', () => HttpResponse.error()),
      ])

      await user.click(await screen.findByRole('button', { name: 'Save version' }))

      expect(await screen.findByRole('alert')).toHaveTextContent(/failed/i)
      // Nothing was saved, so the draft is still the only copy of this outline.
      expect(localStorage.getItem(draftKey('w1'))).not.toBeNull()
    })

    /**
     * SAVE IS THE ONE PLACE `grammarViolation` IS ASKED, and an incomplete
     * outline disables it WITH THE REASON. The alternative — an enabled button
     * that posts a document the server refuses — spends a round trip to learn
     * what the client already knows, and answers in the validator's words
     * rather than the editor's.
     */
    it('refuses to save an incomplete outline, and says what is missing', async () => {
      localStorage.setItem(
        draftKey('w1'),
        JSON.stringify({
          schema: 2,
          nodes: [{ id: 'impl-1', type: 'implement', harness: 'codex', instructions: 'do it' }],
        }),
      )
      renderApp('/workflows/w1/edit', [harnesses()])

      expect(await screen.findByRole('button', { name: 'Save version' })).toBeDisabled()
      expect(screen.getByText(/still needs a gate and a git-publish/)).toBeInTheDocument()
    })
  })
})
