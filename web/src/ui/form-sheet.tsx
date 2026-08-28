import type { ReactNode } from 'react'
import { Drawer } from 'vaul'

/**
 * A bottom sheet holding a form, opened by something the caller owns.
 *
 * THE LOCKED MOBILE-FIRST DECISION NAMES BOTTOM SHEETS AS THE DEFAULT MODAL
 * (`docs/brainstorm/2026-08-01-decisions.md`) and `vaul` is the dependency this
 * project took for them. The Repos screens open four of these — new project,
 * edit project, new repo, edit repo — and the chrome around each is identical:
 * portal, overlay, panel, title, description, Cancel.
 *
 * IT EXISTS FOR THE Z-INDEX, not for the line count. The tab bar is `z-20`;
 * vaul sets no z-index anywhere and ships no stylesheet, so both the overlay
 * and the panel default to `auto` — and a positioned `z-20` element paints
 * above every positioned `auto` element regardless of DOM order or portal.
 * These sheets are bottom-anchored, exactly where the bar is, so without the
 * `z-30` here the bar covers their last stretch: the submit button and Cancel.
 * That fact was measured once, on the task FAB, and four hand-copies of it are
 * four chances to lose it.
 *
 * CONTROLLED, WITH NO TRIGGER OF ITS OWN. Two of the four openers are per-row
 * buttons inside a list that renders twice — once as cards, once as a table —
 * and a trigger-bearing sheet would then exist twice per row. One sheet beside
 * the list, opened by whichever copy of the button was pressed, is the shape
 * that keeps a single dialog in the document.
 *
 * `repositionInputs={false}`: vaul's default keyboard handling transforms the
 * sheet against the visual viewport, and on iOS Safari that moved the whole
 * form out of view the moment a field was focused (operator checkpoint,
 * 2026-08-26, real iPhone — invisible in desktop emulation).
 */
export function FormSheet(props: {
  open: boolean
  onOpenChange: (open: boolean) => void
  title: string
  description: string
  children: ReactNode
}) {
  return (
    <Drawer.Root open={props.open} onOpenChange={props.onOpenChange} repositionInputs={false}>
      <Drawer.Portal>
        <Drawer.Overlay className="bg-ground/70 fixed inset-0 z-30" />
        <Drawer.Content className="bg-surface border-border fixed inset-x-0 bottom-0 z-30 max-h-[90dvh] overflow-y-auto rounded-t-xl border-t p-4">
          {/* Radix requires both, and neither is decoration: the title names the
              dialog for a screen reader and the description is what
              `aria-describedby` points at when it opens. */}
          <Drawer.Title className="text-text mb-1 text-lg font-medium">{props.title}</Drawer.Title>
          <Drawer.Description className="text-text-muted mb-4 text-sm">
            {props.description}
          </Drawer.Description>
          {props.children}
          <Drawer.Close className="text-text-muted mt-3 w-full px-4 py-2 text-sm">
            Cancel
          </Drawer.Close>
        </Drawer.Content>
      </Drawer.Portal>
    </Drawer.Root>
  )
}
