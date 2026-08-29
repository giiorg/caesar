import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { DagPreview } from './dag-preview'
import type { DocEdge, DocNode, DocShape } from './derive'

/**
 * NO QueryClientProvider AND NO MSW IN THIS FILE, and that is the subject's
 * design rather than the test's convenience: `DagPreview` takes the document it
 * draws. The route fetches, the editor derives, this renders — which is what
 * lets one component serve the read view and the live preview both.
 */
const node = (id: string, type: DocNode['type']): DocNode => ({ id, type })
const edge = (from: string, to: string, artifact: DocEdge['artifact']): DocEdge => ({
  from,
  to,
  artifact,
})

const conveyor: DocShape = {
  nodes: [
    node('im', 'implement'),
    node('rev', 'review'),
    node('g', 'gate'),
    node('pub', 'git-publish'),
  ],
  edges: [
    edge('im', 'rev', 'patch'),
    edge('im', 'g', 'patch'),
    edge('rev', 'g', 'review_verdict'),
    edge('g', 'pub', 'patch'),
  ],
}

const pills = (container: HTMLElement) => [...container.querySelectorAll('[data-node-type]')]
const lines = (container: HTMLElement) => [...container.querySelectorAll('line')]

/**
 * SVG tooltips, read directly rather than through `getByTitle`. MEASURED:
 * testing-library's title query selects `[title], svg > title`, so it sees only
 * a `<title>` that is a DIRECT child of the `<svg>` — every one here hangs off
 * a `<g>` or a `<line>`, which is where SVG puts the tooltip for a shape.
 */
const titles = (container: HTMLElement) =>
  [...container.querySelectorAll('title')].map((t) => t.textContent)

describe('DagPreview', () => {
  /**
   * `data-node-type` IS ON THE DOM BECAUSE IT IS THE ONLY WAY A TEST CAN ASSERT
   * WHAT A PERSON SEES. The whole content of a pill is its colour and its
   * shape; a test reading class names would be asserting Tailwind's spelling
   * rather than the design, and `phase-dots.tsx` records the same call for the
   * same reason.
   */
  it('draws one badged pill per node, in document order', () => {
    const { container } = render(<DagPreview doc={conveyor} />)

    expect(pills(container).map((p) => p.getAttribute('data-node-type'))).toEqual([
      'implement',
      'review',
      'gate',
      'git-publish',
    ])
  })

  /**
   * THE PICTURE HAS TO SAY WHAT IT IS. `role="img"` collapses the SVG's
   * internals for assistive technology — which is right, since a pile of
   * `<rect>`s and `<line>`s reads as nothing — and the label is then the only
   * description of the workflow a screen reader gets. So it names the shape,
   * step by step, rather than saying "diagram".
   */
  it('names the workflow shape as the image label', () => {
    render(<DagPreview doc={conveyor} />)

    expect(
      screen.getByRole('img', { name: '4 steps: implement, review, gate, git-publish' }),
    ).toBeInTheDocument()
  })

  it('says one step in the singular', () => {
    render(<DagPreview doc={{ nodes: [node('im', 'implement')] }} />)

    expect(screen.getByRole('img', { name: '1 step: implement' })).toBeInTheDocument()
  })

  /**
   * A DOCUMENT WITH NO BINDINGS DRAWS PILLS AND NOTHING ELSE. The pair of
   * assertions is the point: the pill count alone would pass against a
   * component that drew a line for every node, and the line count alone would
   * pass against one that drew nothing at all.
   */
  it('draws an unbound document as pills with no lines', () => {
    const { container } = render(
      <DagPreview doc={{ nodes: [node('a', 'implement'), node('b', 'gate')] }} />,
    )

    expect(pills(container)).toHaveLength(2)
    expect(lines(container)).toHaveLength(0)
  })

  it('draws one line per binding', () => {
    const { container } = render(<DagPreview doc={conveyor} />)

    expect(lines(container)).toHaveLength(4)
  })

  /**
   * THE ARTIFACT TYPE IS ON THE LINE, and it is the only place a reader can
   * learn it. Two of the gate's incoming lines look identical otherwise — a
   * patch from the implement and a verdict from the review — and which is which
   * is exactly what a person opening a definition came to find out.
   */
  it('names each binding’s artifact on its line', () => {
    const { container } = render(<DagPreview doc={conveyor} />)

    expect(titles(container)).toEqual(
      expect.arrayContaining(['rev → g: review_verdict', 'im → g: patch']),
    )
  })

  /**
   * COLUMNS BECOME x AND ROWS BECOME y, asserted as an ORDER rather than as
   * pixel values — the geometry constants are presentation and free to change,
   * but a layout that mapped a column to anything but a horizontal offset would
   * stop being a left-to-right reading of the workflow.
   */
  it('places each column further right than the last, on one row', () => {
    const { container } = render(<DagPreview doc={conveyor} />)
    const rects = [...container.querySelectorAll('[data-node-type] rect')]
    const x = rects.map((r) => Number(r.getAttribute('x')))
    const y = rects.map((r) => Number(r.getAttribute('y')))

    expect(x).toEqual([...x].sort((a, b) => a - b))
    expect(new Set(x).size).toBe(4)
    expect(new Set(y).size).toBe(1)
  })

  it('places a second row of a column below the first', () => {
    const { container } = render(
      <DagPreview
        doc={{
          nodes: [node('root', 'plan'), node('a', 'implement'), node('b', 'implement')],
          edges: [edge('root', 'a', 'plan'), edge('root', 'b', 'plan')],
        }}
      />,
    )
    const rects = [...container.querySelectorAll('[data-node-type] rect')]
    const [root, a, b] = rects.map((r) => ({
      x: Number(r.getAttribute('x')),
      y: Number(r.getAttribute('y')),
    }))

    expect(a?.x).toBe(b?.x)
    expect(a?.x).toBeGreaterThan(root?.x ?? 0)
    expect(b?.y).toBeGreaterThan(a?.y ?? 0)
  })

  /**
   * A 64-CHARACTER ID IS LEGAL — `^[a-z][a-z0-9_-]{0,63}$` is the validator's
   * pattern — and SVG text neither wraps nor clips on its own, so an untruncated
   * label runs straight over the next column. The full id stays reachable on
   * the pill's title, because truncating the only copy of a step's name would
   * make two long ids indistinguishable.
   */
  it('truncates a long step id in the drawing and keeps it whole on the pill', () => {
    const long = `a${'b'.repeat(63)}`
    const { container } = render(<DagPreview doc={{ nodes: [node(long, 'implement')] }} />)

    expect(titles(container)).toEqual([`${long} — implement`])
    expect(screen.queryByText(long)).not.toBeInTheDocument()
    expect(screen.getByText(/^ab+…$/)).toBeInTheDocument()
  })

  it('leaves a short step id alone', () => {
    render(<DagPreview doc={{ nodes: [node('impl-1', 'implement')] }} />)

    expect(screen.getByText('impl-1')).toBeInTheDocument()
  })

  /**
   * A GATE IS TOLD APART WITHOUT COLOUR, exactly as it is in the phase dots: it
   * is a decision rather than work, and a reader who cannot separate the hues
   * still has to see which step stops the conveyor.
   *
   * The assertion is that the corner radius DIFFERS, not that it is any
   * particular number — the shape is presentation and free to change, the
   * distinction is not. It is asserted at all, unlike the fill colours, because
   * the radius is computed in this file rather than carried by a class:
   * `data-node-type` cannot pin it the way it pins the colour a stylesheet
   * applies.
   */
  it('draws the gate with a corner the work steps do not have', () => {
    const { container } = render(<DagPreview doc={conveyor} />)
    const radius = pills(container).map((p) => p.querySelector('rect')?.getAttribute('rx'))
    const [implement, review, gate, publish] = radius

    expect(gate).not.toBe(implement)
    expect(new Set([implement, review, publish]).size).toBe(1)
  })

  /**
   * TWO PREVIEWS ON ONE PAGE MUST NOT SHARE A MARKER ID — which is what the
   * editor screen is, once the read-only outline lands beside the live picture.
   * `id` is document-global in SVG and `url(#…)` resolves to whichever element
   * came first, so a constant id would point both previews' arrowheads at one
   * definition and quietly lose the second when the first unmounts.
   */
  it('gives each instance its own arrowhead marker', () => {
    const { container } = render(
      <>
        <DagPreview doc={conveyor} />
        <DagPreview doc={conveyor} />
      </>,
    )
    const ids = [...container.querySelectorAll('marker')].map((m) => m.id)

    expect(ids).toHaveLength(2)
    expect(new Set(ids).size).toBe(2)
  })

  /**
   * TWO CARDS ON ONE NAME IS AN ORDINARY INTERMEDIATE STATE — `grammarViolation`
   * refuses a duplicate id at Save and not before — so the picture of it has to
   * be complete. Keying the pills by id alone makes React drop one of the two
   * from the drawing of the very state the person is trying to fix; the console
   * check is what sees it, since the warning is the only place React says so.
   */
  it('draws both pills when two steps share a name', () => {
    const complain = vi.spyOn(console, 'error').mockImplementation(() => {})
    const { container } = render(
      <DagPreview doc={{ nodes: [node('impl-1', 'implement'), node('impl-1', 'implement')] }} />,
    )

    expect(pills(container)).toHaveLength(2)
    expect(complain).not.toHaveBeenCalled()
  })

  /**
   * AN EMPTY OUTLINE SAYS SO. Unreachable from the wire — `Validate` refuses a
   * document with no nodes — but the FIRST thing the editor renders in Task 27
   * is a preview of nothing, and an `<svg>` with no children is a bordered
   * empty box: indistinguishable, to the person looking at it, from a preview
   * that failed to draw. The `queryByRole` half is what makes that bite; the
   * sentence alone would pass beside an empty frame.
   */
  it('says an empty outline has nothing to draw, rather than drawing an empty frame', () => {
    render(<DagPreview doc={{ nodes: [] }} />)

    expect(screen.getByText(/no steps to draw/i)).toBeInTheDocument()
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
  })

  /**
   * THE DOCUMENT THE EDITOR CANNOT OPEN, DRAWN IN FULL — two implements
   * consuming one plan. This is the case the preview exists for: §6.1 says
   * every document renders here including branching ones authored via the API,
   * and the outline beside it will be read-only for exactly this shape.
   */
  it('draws a branching document the outline editor could not author', () => {
    const { container } = render(
      <DagPreview
        doc={{
          nodes: [
            node('plan-1', 'plan'),
            node('impl-a', 'implement'),
            node('impl-b', 'implement'),
            node('gate-1', 'gate'),
            node('publish-1', 'git-publish'),
          ],
          edges: [
            edge('plan-1', 'impl-a', 'plan'),
            edge('plan-1', 'impl-b', 'plan'),
            edge('impl-a', 'gate-1', 'patch'),
            edge('gate-1', 'publish-1', 'patch'),
          ],
        }}
      />,
    )

    expect(pills(container)).toHaveLength(5)
    expect(lines(container)).toHaveLength(4)
    expect(screen.getByRole('img', { name: /^5 steps:/ })).toBeInTheDocument()
  })
})
