import { fireEvent, render } from '@testing-library/react'
import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { Card } from './derive'
import { DRAFT_DEBOUNCE_MS, draftKey, readDraft, useDraftPersistence, writeDraft } from './drafts'

const CARDS: Card[] = [
  { id: 'plan-1', type: 'plan', harness: 'claude-code', instructions: 'draft it' },
  { id: 'impl-1', type: 'implement', harness: 'claude-code', instructions: 'do it' },
  { id: 'gate-1', type: 'gate', revision: { target: 'impl-1', maxRounds: 3 } },
  { id: 'publish-1', type: 'git-publish' },
]

beforeEach(() => localStorage.clear())

describe('the draft key', () => {
  /** `caesar.theme`'s convention, plus the id — a draft belongs to one workflow. */
  it('is namespaced per workflow', () => {
    expect(draftKey('w1')).toBe('caesar.workflow-draft.w1')
    expect(draftKey('w2')).not.toBe(draftKey('w1'))
  })
})

describe('readDraft / writeDraft', () => {
  it('round-trips an outline, revision loop included', () => {
    writeDraft('w1', CARDS)
    expect(readDraft('w1')).toEqual(CARDS)
  })

  it('answers null when nothing was ever written', () => {
    expect(readDraft('w1')).toBeNull()
  })

  /**
   * A DRAFT IS SOMEONE ELSE'S BYTES — an older build of this app wrote it, and
   * the person cannot be asked to fix it. Both failure shapes answer the same
   * `null`: text that is not JSON at all, and JSON that is not a document this
   * build can describe.
   */
  it('answers null for stored text it cannot read', () => {
    localStorage.setItem(draftKey('w1'), 'not json{')
    expect(readDraft('w1')).toBeNull()

    localStorage.setItem(draftKey('w1'), JSON.stringify({ schema: 99, nodes: [] }))
    expect(readDraft('w1')).toBeNull()
  })

  /**
   * THE STORED FORM IS THE DOCUMENT, which is what lets `readDocument` be the
   * reader. Asserted on the bytes because it is the whole basis of the
   * paragraph above: stored as cards, the reading would need a second guard of
   * its own and this test would pass either way.
   */
  it('stores the document the outline would save as', () => {
    writeDraft('w1', CARDS)
    const stored = JSON.parse(localStorage.getItem(draftKey('w1')) as string)
    expect(stored.schema).toBe(2)
    expect(stored.nodes.map((n: { id: string }) => n.id)).toEqual([
      'plan-1',
      'impl-1',
      'gate-1',
      'publish-1',
    ])
    // The derived edges ride along; `readDraft` reads the nodes back as cards
    // and re-derives them, so they cost nothing and prove the stored thing is
    // a real document rather than a bag of nodes.
    expect(stored.edges).toContainEqual({ from: 'impl-1', to: 'gate-1', artifact: 'patch' })
  })
})

/** A component that owns cards the way the editor does, so the hook runs its shipped path. */
function Harness(props: { workflowId: string; initial: readonly Card[] }) {
  const [cards, setCards] = useState<readonly Card[]>(props.initial)
  useDraftPersistence(props.workflowId, cards)
  return (
    <button
      type="button"
      onClick={() => setCards([...cards, { id: 'publish-1', type: 'git-publish' }])}
    >
      add
    </button>
  )
}

describe('useDraftPersistence', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  /**
   * MOUNTING IS NOT AN EDIT. Without the first-run guard, opening the editor
   * and leaving writes a draft of the empty outline — and since a draft
   * outranks prefill, the next `/edit?from=5` would restore that emptiness and
   * silently decline to prefill. This is the test that fails when the guard is
   * deleted.
   */
  it('writes nothing for a mount with no edit', () => {
    render(<Harness workflowId="w1" initial={[]} />)
    vi.advanceTimersByTime(DRAFT_DEBOUNCE_MS * 4)
    expect(localStorage.getItem(draftKey('w1'))).toBeNull()
  })

  it('writes the outline once the typing pause has passed', () => {
    const { getByRole } = render(
      <Harness workflowId="w1" initial={[{ id: 'impl-1', type: 'implement' }]} />,
    )
    fireEvent.click(getByRole('button'))

    // BEFORE the pause: nothing yet. Without this half the assertion below
    // would pass against a hook that wrote on every keystroke, which is the
    // behaviour the debounce exists to prevent.
    expect(localStorage.getItem(draftKey('w1'))).toBeNull()

    vi.advanceTimersByTime(DRAFT_DEBOUNCE_MS)
    expect(readDraft('w1')?.map((c) => c.id)).toEqual(['impl-1', 'publish-1'])
  })

  /**
   * THE TIMER RESTARTS ON EVERY CHANGE — a debounce, not a throttle. Two edits
   * inside one window produce ONE write, and it carries the second edit.
   */
  it('collapses a burst of edits into a single write', () => {
    const { getByRole } = render(<Harness workflowId="w1" initial={[]} />)
    fireEvent.click(getByRole('button'))
    vi.advanceTimersByTime(DRAFT_DEBOUNCE_MS - 1)
    fireEvent.click(getByRole('button'))
    vi.advanceTimersByTime(DRAFT_DEBOUNCE_MS - 1)
    expect(localStorage.getItem(draftKey('w1'))).toBeNull()

    vi.advanceTimersByTime(1)
    expect(readDraft('w1')).toHaveLength(2)
  })
})
