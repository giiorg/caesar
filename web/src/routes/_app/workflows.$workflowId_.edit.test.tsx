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

  /** Saving is Task 28's, and the screen says so rather than showing a dead button. */
  it('says that saving is not built yet', async () => {
    renderApp('/workflows/w1/edit', [harnesses()])
    const stub = await screen.findByText('Saving a new version')
    expect(
      within(stub.parentElement as HTMLElement).getByText(/not built yet/i),
    ).toBeInTheDocument()
  })
})
