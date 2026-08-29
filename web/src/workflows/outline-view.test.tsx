import { fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useReducer } from 'react'
import { describe, expect, it, vi } from 'vitest'
import type { Card } from './derive'
import { EMPTY_EDITOR, editorReducer } from './editor-state'
import { OutlineView } from './outline-view'

const CONVEYOR: Card[] = [
  { id: 'plan-1', type: 'plan', harness: 'claude-code', instructions: 'draft it' },
  { id: 'impl-1', type: 'implement', harness: 'claude-code', instructions: 'do it' },
  { id: 'review-1', type: 'review', harness: 'codex', instructions: 'check it' },
  { id: 'gate-1', type: 'gate', revision: { target: 'impl-1', maxRounds: 3 } },
  { id: 'publish-1', type: 'git-publish' },
]

/**
 * A conveyor with TWO implements, for the reorder cases.
 *
 * The five-card fixture above cannot exhibit a legal reorder at all: every
 * adjacent swap in `plan · implement · review · gate · git-publish` breaks the
 * grammar, so a test that "moved a card" there would be asserting a refusal
 * while claiming to assert a move. Two implements are the only pair the V1
 * grammar lets change places.
 */
const TWO_IMPL: Card[] = [
  { id: 'plan-1', type: 'plan', harness: 'claude-code', instructions: 'draft it' },
  { id: 'impl-1', type: 'implement', harness: 'claude-code', instructions: 'first' },
  { id: 'impl-2', type: 'implement', harness: 'codex', instructions: 'second' },
  { id: 'gate-1', type: 'gate', revision: { target: 'impl-2', maxRounds: 2 } },
  { id: 'publish-1', type: 'git-publish' },
]

const HARNESSES = ['claude-code', 'codex']

/** The editor's real wiring: a `useReducer` whose handles go straight to the view. */
function LiveOutline(props: { initial?: readonly Card[] }) {
  const [state, dispatch] = useReducer(editorReducer, {
    ...EMPTY_EDITOR,
    cards: props.initial ?? [],
  })
  return (
    <OutlineView
      cards={state.cards}
      edit={{ dispatch, harnesses: HARNESSES, refusal: state.refusal, note: state.note }}
    />
  )
}

const rowTypes = () =>
  [...screen.getByRole('list', { name: 'Steps' }).querySelectorAll('[data-node-type]')].map((n) =>
    n.getAttribute('data-node-type'),
  )

/** Row order by id — the only way to see a swap of two cards of the same type. */
const rowIds = () =>
  screen.getAllByRole('textbox', { name: 'Step id' }).map((i) => (i as HTMLInputElement).value)

describe('OutlineView, read-only', () => {
  it('renders one row per node, badged by type, with its typed fields', () => {
    render(<OutlineView cards={CONVEYOR} />)

    expect(rowTypes()).toEqual(['plan', 'implement', 'review', 'gate', 'git-publish'])
    // Two cards share a harness, which is ordinary — the assertion is that the
    // value reaches the row, not that it is unique in the document.
    expect(screen.getAllByText('claude-code')).toHaveLength(2)
    expect(screen.getByText('draft it')).toBeInTheDocument()
    expect(screen.getByText('impl-1, up to 3 rounds')).toBeInTheDocument()
  })

  /**
   * A BRANCHING DOCUMENT STILL RENDERS. Two implements off one plan is
   * API-authorable and not editor-producible — `isChainShaped` answers false —
   * and this view is what its reader gets, so blanking would be the version
   * read view refusing to show a definition the runner will happily execute.
   * Nothing here consults the grammar, which is what makes that true.
   */
  it('renders a document the editor could never author', () => {
    render(
      <OutlineView
        cards={[
          { id: 'plan-1', type: 'plan', harness: 'cc', instructions: 'x' },
          { id: 'impl-a', type: 'implement', harness: 'cc', instructions: 'x' },
          { id: 'impl-b', type: 'implement', harness: 'cc', instructions: 'x' },
          { id: 'gate-1', type: 'gate' },
          { id: 'publish-1', type: 'git-publish' },
        ]}
      />,
    )
    expect(rowTypes()).toEqual(['plan', 'implement', 'implement', 'gate', 'git-publish'])
    expect(screen.getByText('impl-a')).toBeInTheDocument()
    expect(screen.getByText('impl-b')).toBeInTheDocument()
  })

  /** No control of any kind — this is the assertion that the mode is real. */
  it('offers nothing to edit', () => {
    render(<OutlineView cards={CONVEYOR} />)
    expect(screen.queryAllByRole('textbox')).toHaveLength(0)
    expect(screen.queryAllByRole('combobox')).toHaveLength(0)
    expect(screen.queryAllByRole('button')).toHaveLength(0)
  })
})

describe('the add-step menu', () => {
  /**
   * THE MENU IS `legalTypesAt`'s ANSWER, EXACTLY. At the start of an empty
   * outline that is plan and implement — a review has no patch to consume and
   * a publish no gate to follow, and both are refused by the validator's
   * consumption table rather than merely discouraged.
   */
  it('lists only the types the position accepts', async () => {
    render(<LiveOutline />)
    const menu = screen.getByRole('combobox', { name: 'Add the first step' })
    expect([...menu.querySelectorAll('option')].map((o) => o.value)).toEqual([
      '',
      'plan',
      'implement',
    ])
  })

  it('lists a different answer at a different position', () => {
    render(<LiveOutline initial={CONVEYOR.slice(0, 2)} />)
    // Before the plan: only a plan may open a workflow, and one is already
    // there — so nothing at all, and the menu does not render.
    expect(screen.queryByRole('combobox', { name: 'Add a step before plan-1' })).toBeNull()
    // After the implement: another implement, a review, or the gate.
    const end = screen.getByRole('combobox', { name: 'Add a step at the end' })
    expect([...end.querySelectorAll('option')].map((o) => o.value)).toEqual([
      '',
      'implement',
      'review',
      'gate',
    ])
  })

  it('adds the picked type at that position', async () => {
    const user = userEvent.setup()
    render(<LiveOutline />)
    await user.selectOptions(screen.getByRole('combobox', { name: 'Add the first step' }), 'plan')
    expect(rowTypes()).toEqual(['plan'])

    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Add a step at the end' }),
      'implement',
    )
    expect(rowTypes()).toEqual(['plan', 'implement'])
  })
})

describe('a gate card', () => {
  /**
   * THE REVIEW IS ABSENT FROM THE TARGETS and the fixture has one, which is
   * what makes the assertion bite: the review precedes the gate and is agentic,
   * so every intuition puts it in this list, and `validateRevisions` refuses it
   * because the bypass edge means it does not dominate the gate.
   */
  it('offers preceding plan and implement steps as revision targets, never the review', () => {
    render(<LiveOutline initial={CONVEYOR} />)
    const select = screen.getByRole('combobox', { name: 'Revision loop sends work back to' })
    expect([...select.querySelectorAll('option')].map((o) => o.value)).toEqual([
      '',
      'plan-1',
      'impl-1',
    ])
  })

  it('shows the rounds input only once a target is picked', async () => {
    const user = userEvent.setup()
    render(
      <LiveOutline
        initial={[
          { id: 'impl-1', type: 'implement' },
          { id: 'gate-1', type: 'gate' },
        ]}
      />,
    )
    expect(screen.queryByRole('spinbutton', { name: 'Maximum rounds' })).toBeNull()

    await user.selectOptions(
      screen.getByRole('combobox', { name: 'Revision loop sends work back to' }),
      'impl-1',
    )
    expect(screen.getByRole('spinbutton', { name: 'Maximum rounds' })).toHaveValue(1)
  })
})

describe('an agentic card', () => {
  it('offers the registered harnesses', () => {
    render(<LiveOutline initial={[{ id: 'impl-1', type: 'implement' }]} />)
    const select = screen.getByRole('combobox', { name: 'Harness' })
    expect([...select.querySelectorAll('option')].map((o) => o.value)).toEqual([
      '',
      'claude-code',
      'codex',
    ])
  })

  /**
   * A HARNESS THE REGISTRY NO LONGER HAS IS STILL SHOWN, marked. Prefilling
   * from an older version can name one that has since been deregistered; a
   * select that dropped it would show blank and quietly rewrite the step on the
   * next save.
   */
  it('keeps a harness the registry has forgotten, and says so', () => {
    render(<LiveOutline initial={[{ id: 'impl-1', type: 'implement', harness: 'retired-1' }]} />)
    const select = screen.getByRole('combobox', { name: 'Harness' })
    expect(select).toHaveValue('retired-1')
    expect(within(select).getByText('retired-1 (not registered)')).toBeInTheDocument()
  })

  /**
   * BYTES, NOT CHARACTERS, AND BEFORE SAVE. The fixture is deliberately UNDER
   * the limit by `String.length` and over it on the wire — 4097 two-byte
   * characters is 4097 characters and 8194 bytes — so a counter that measured
   * length would call this fine and the server would refuse it.
   */
  it('shows the byte-count error for instructions the wire cannot carry', async () => {
    const overByBytes = 'é'.repeat(4097)
    render(
      <LiveOutline initial={[{ id: 'impl-1', type: 'implement', instructions: overByBytes }]} />,
    )
    expect(overByBytes.length).toBeLessThan(8192)
    expect(screen.getByRole('alert')).toHaveTextContent(
      '8194 bytes of instructions; the limit is 8192.',
    )
    expect(screen.getByRole('textbox', { name: 'Instructions' })).toBeInvalid()
  })

  it('shows no byte error for instructions that fit', () => {
    render(<LiveOutline initial={[{ id: 'impl-1', type: 'implement', instructions: 'short' }]} />)
    expect(screen.queryByRole('alert')).toBeNull()
  })

  /**
   * EXACTLY AT THE LIMIT IS LEGAL — `validate.go` refuses `> maxInstructions`,
   * not `>=`. Without this the comparison is unfalsifiable: the 8194-byte
   * fixture above is over the line on either spelling, so a `>=` mutant would
   * survive the whole suite and the editor would refuse a document the server
   * accepts.
   */
  it('accepts instructions of exactly the limit', () => {
    render(
      <LiveOutline
        initial={[{ id: 'impl-1', type: 'implement', instructions: 'x'.repeat(8192) }]}
      />,
    )
    expect(screen.queryByRole('alert')).toBeNull()
  })
})

describe('refusals', () => {
  /**
   * A REORDER THAT BREAKS THE GRAMMAR IS REFUSED WITH THE REASON ON THE CARD —
   * the spec's words. The outline does not move, and the sentence appears
   * where the person acted.
   */
  it('shows a refused reorder’s reason and leaves the order alone', async () => {
    const user = userEvent.setup()
    render(<LiveOutline initial={CONVEYOR} />)

    await user.click(screen.getByRole('button', { name: 'Move publish-1 up' }))

    expect(rowTypes()).toEqual(['plan', 'implement', 'review', 'gate', 'git-publish'])
    expect(screen.getByRole('alert')).toHaveTextContent(/git-publish is the final step/i)
  })

  /** A refusal about a POSITION has no card to sit on, so it sits above the list. */
  it('puts a refused add above the outline', () => {
    const dispatch = vi.fn()
    render(
      <OutlineView
        cards={[]}
        edit={{
          dispatch,
          harnesses: HARNESSES,
          refusal: { cardId: null, reason: 'a review needs a patch before it' },
          note: null,
        }}
      />,
    )
    expect(screen.getByRole('alert')).toHaveTextContent('a review needs a patch before it')
  })

  it('announces a repair the reducer made', () => {
    render(
      <OutlineView
        cards={CONVEYOR}
        edit={{
          dispatch: vi.fn(),
          harnesses: HARNESSES,
          refusal: null,
          note: 'The revision loop on gate-1 → impl-2 was cleared.',
        }}
      />,
    )
    expect(screen.getByRole('status')).toHaveTextContent('was cleared')
  })
})

describe('reordering', () => {
  /**
   * THE ARROWS ARE THE PHONE'S MECHANISM, not a fallback: HTML5 drag-and-drop
   * emits nothing from a touch, so on the device this screen was designed for
   * the grip does nothing at all. They are also what a keyboard reaches.
   */
  it('moves a card with the keyboard-reachable buttons', async () => {
    const user = userEvent.setup()
    render(<LiveOutline initial={TWO_IMPL} />)

    await user.click(screen.getByRole('button', { name: 'Move impl-2 up' }))
    expect(rowIds()).toEqual(['plan-1', 'impl-2', 'impl-1', 'gate-1', 'publish-1'])
  })

  it('disables the arrow that would leave the outline', () => {
    render(<LiveOutline initial={CONVEYOR} />)
    expect(screen.getByRole('button', { name: 'Move plan-1 up' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Move publish-1 down' })).toBeDisabled()
  })

  /**
   * The hand-rolled HTML5 drag, driven the way a desktop browser drives it.
   * The dragged index rides a ref rather than `dataTransfer`, which is what
   * makes this assertable in jsdom at all — a synthetic `dragStart` carries no
   * usable DataTransfer.
   */
  it('reorders on a drop from the grip', () => {
    /**
     * THREE IMPLEMENTS AND A NON-ADJACENT DROP, and that is measured rather
     * than thorough. Dragging a card onto its NEIGHBOUR is symmetric — moving
     * 2→1 and moving 1→2 both swap the same pair — so the two-implement
     * fixture this test started with could not tell the drop's direction from
     * its reverse, and the from/to-swapped mutant survived it. Card 3 onto
     * position 1 is the smallest fixture where the two answers differ.
     */
    render(
      <LiveOutline
        initial={[
          { id: 'plan-1', type: 'plan' },
          { id: 'impl-1', type: 'implement' },
          { id: 'impl-2', type: 'implement' },
          { id: 'impl-3', type: 'implement' },
          { id: 'gate-1', type: 'gate' },
          { id: 'publish-1', type: 'git-publish' },
        ]}
      />,
    )
    const rows = screen.getByRole('list', { name: 'Steps' }).querySelectorAll('[data-node-type]')

    fireEvent.dragStart(screen.getByTestId('grip-impl-3'))
    fireEvent.dragOver(rows[1] as Element)
    fireEvent.drop(rows[1] as Element)

    // The dragged card lands where it was dropped and the rest close up behind
    // it. Reversed, the drop would instead carry `impl-1` down to position 3.
    expect(rowIds()).toEqual(['plan-1', 'impl-3', 'impl-1', 'impl-2', 'gate-1', 'publish-1'])
  })
})

describe('renaming a step', () => {
  /**
   * COMMITTED ON BLUR, and the intermediate states are the reason. A rename
   * dispatched per keystroke would refuse the empty string the moment someone
   * selected the text to retype it.
   */
  it('renames on blur, not on every keystroke', async () => {
    const user = userEvent.setup()
    render(<LiveOutline initial={CONVEYOR} />)

    const input = screen.getAllByRole('textbox', { name: 'Step id' })[1] as HTMLElement
    await user.clear(input)
    await user.type(input, 'build_it')
    // Still the old name: nothing has been committed, so the card's controls
    // and the move buttons still answer to it.
    expect(screen.getByRole('button', { name: 'Remove impl-1' })).toBeInTheDocument()

    await user.tab()
    expect(screen.getByRole('button', { name: 'Remove build_it' })).toBeInTheDocument()
  })

  it('keeps the typed text and shows the reason when a rename is refused', async () => {
    const user = userEvent.setup()
    render(<LiveOutline initial={CONVEYOR} />)

    const input = screen.getAllByRole('textbox', { name: 'Step id' })[1] as HTMLElement
    await user.clear(input)
    await user.type(input, 'Not Valid')
    await user.tab()

    expect(input).toHaveValue('Not Valid')
    expect(screen.getByRole('alert')).toHaveTextContent('is not a usable step id')
    // The card itself is untouched — the id is still the committed one.
    expect(screen.getByRole('button', { name: 'Remove impl-1' })).toBeInTheDocument()
  })
})

describe('removing a step', () => {
  it('removes the card', async () => {
    const user = userEvent.setup()
    render(<LiveOutline initial={CONVEYOR} />)
    await user.click(screen.getByRole('button', { name: 'Remove review-1' }))
    expect(rowTypes()).toEqual(['plan', 'implement', 'gate', 'git-publish'])
  })

  /** The reducer's repair, seen through the UI: the loop goes and the note says so. */
  it('announces a revision loop cleared by a removal', async () => {
    const user = userEvent.setup()
    render(<LiveOutline initial={TWO_IMPL} />)

    await user.click(screen.getByRole('button', { name: 'Remove impl-2' }))

    expect(rowIds()).toEqual(['plan-1', 'impl-1', 'gate-1', 'publish-1'])
    expect(screen.getByRole('status')).toHaveTextContent(/revision loop on gate-1 → impl-2/i)
    expect(screen.getByRole('combobox', { name: 'Revision loop sends work back to' })).toHaveValue(
      '',
    )
  })

  /**
   * REMOVAL IS JUDGED, and this is what that looks like on screen. Taking the
   * only implement out leaves a review with no patch to consume, which no
   * completion can rescue — so the outline does not move and the reason says
   * which step was stranded.
   */
  it('refuses a removal that would strand the outline', async () => {
    const user = userEvent.setup()
    render(<LiveOutline initial={CONVEYOR} />)

    await user.click(screen.getByRole('button', { name: 'Remove impl-1' }))

    expect(rowTypes()).toEqual(['plan', 'implement', 'review', 'gate', 'git-publish'])
    expect(screen.getByRole('alert')).toHaveTextContent(/review needs a patch before it/i)
  })
})
