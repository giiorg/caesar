import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useEffect, useId, useRef, useState } from 'react'
import { type Entry, paletteEntries } from './palette-entries'

/**
 * ⌘K, hand-rolled — decisions §7's "desktop = full sidebar + top bar with ⌘K".
 *
 * NO `cmdk`, and the reason that actually holds is the dependency surface the
 * pin lane guards, not CSP: a bundled npm package is same-origin script and
 * `script-src 'self'` has no opinion about it. (The plan says otherwise; it is
 * wrong about the mechanism and right about the conclusion.)
 *
 * `radix-ui` IS already a pinned dependency here — declared in the scaffold
 * commit and imported nowhere — and its Dialog would have brought a real focus
 * trap. It was not taken because in a command palette FOCUS NEVER LEAVES THE
 * INPUT: the arrow keys move an `aria-activedescendant` highlight, not focus,
 * which is what lets the whole trap below be a cycle over the panel's focusable
 * elements. That is a genuinely small thing to own, unlike a general modal.
 *
 * IT IS AN ACCELERATOR, NEVER THE ONLY PATH. The locked mockups offered a
 * "command-first minimal" shell in which ⌘K was the primary way to move, and it
 * was NOT the option chosen — A was, bottom tabs plus a desktop sidebar. So
 * everything reachable here is reachable without it: the seven pages are in the
 * nav, and "New task" is the FAB that has always been on `/tasks`.
 */

/** The overlay sits above the tab bar (z-20) AND the new-task sheet (z-30). */
const OVERLAY = 'bg-ground/70 fixed inset-0 z-40 flex items-start justify-center p-4 pt-[10vh]'

const FOCUSABLE = 'input, button, [href], select, textarea, [tabindex]:not([tabindex="-1"])'

export function CommandPalette(props: { open: boolean; onClose: () => void }) {
  // MOUNTED ONLY WHILE OPEN, so the panel's focus-capture effect runs on open
  // and its cleanup runs on close — the whole focus-restore contract falls out
  // of the lifecycle instead of needing a second effect keyed on `open`.
  if (!props.open) return null
  return <Panel onClose={props.onClose} />
}

function Panel(props: { onClose: () => void }) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)
  const listId = useId()

  const entries = paletteEntries(qc, query)
  /**
   * THE HIGHLIGHT IS CLAMPED ON READ, AT BOTH ENDS, rather than kept valid at
   * every write.
   *
   * `entries` is derived, so it changes without the arrow keys: a keystroke
   * shrinks it, and so does a route load finishing while the palette is open.
   * An index that was valid when it was stored can therefore be out of range by
   * the time Enter reads it. Clamping here makes that unrepresentable for every
   * reader at once — `entries[highlighted]` is in range whenever there is a
   * range — where guarding each of the three writers would leave the next
   * writer to remember.
   *
   * Both ends, because both go wrong: ArrowDown past the last row would run off
   * the top end, and ArrowUp on an empty list stores -1. On an empty list this
   * answers 0, which indexes nothing — and Enter's `undefined` check is what
   * makes that a no-op rather than a crash.
   */
  const highlighted = Math.min(Math.max(active, 0), Math.max(entries.length - 1, 0))

  useEffect(() => {
    // The element to hand focus BACK to, captured before we take it away.
    const opener = document.activeElement
    inputRef.current?.focus()
    return () => {
      if (opener instanceof HTMLElement) opener.focus()
    }
  }, [])

  const go = (entry: Entry) => {
    props.onClose()
    switch (entry.kind) {
      case 'page':
        void navigate({ to: entry.to })
        return
      case 'new-task':
        // THE COMMAND PERFORMS ITS COMMAND. Navigating to `/tasks` and stopping
        // would land the operator next to the button rather than in the form;
        // `?new=1` is the drawer's open state since this task moved it out of
        // `useState`, so this opens it from anywhere.
        void navigate({ to: '/tasks', search: { new: 1 } })
        return
      case 'task':
      case 'run':
        void navigate({ to: '/runs/$runId', params: { runId: entry.runId } })
        return
    }
  }

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault()
      props.onClose()
      return
    }
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      setActive(highlighted + (e.key === 'ArrowDown' ? 1 : -1))
      return
    }
    if (e.key === 'Enter') {
      e.preventDefault()
      const entry = entries[highlighted]
      if (entry !== undefined) go(entry)
      return
    }
    if (e.key === 'Tab') {
      // THE TRAP, as a cycle rather than a blanket preventDefault. Today the
      // input is the only focusable thing in the panel and the two are
      // equivalent — but a later edit adding a close button would silently turn
      // a blanket preventDefault into "Tab does nothing", which reads as broken
      // rather than as trapped.
      const nodes = panelRef.current?.querySelectorAll<HTMLElement>(FOCUSABLE) ?? []
      if (nodes.length === 0) return
      const list = Array.from(nodes)
      const first = list[0]
      const last = list[list.length - 1]
      if (first === undefined || last === undefined) return
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault()
        last.focus()
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault()
        first.focus()
      }
    }
  }

  return (
    // The overlay is a DISMISSAL SURFACE, not a control, and both suppressions
    // say the same thing: the keyboard path out of this dialog is Escape,
    // handled on the panel below. Making the backdrop a real button would add a
    // focusable element to a dialog whose entire design is that focus stays in
    // the input, and screen-reader users would meet a control that duplicates
    // the key they already have.
    // biome-ignore lint/a11y/noStaticElementInteractions: backdrop, not a control
    // biome-ignore lint/a11y/useKeyWithClickEvents: Escape is the keyboard equivalent
    <div className={OVERLAY} onClick={props.onClose} data-testid="palette-overlay">
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-label="Command palette"
        onKeyDown={onKeyDown}
        onClick={(e) => e.stopPropagation()}
        className="border-border bg-surface flex w-full max-w-lg flex-col gap-2 rounded-xl border p-3 shadow-lg"
      >
        <input
          ref={inputRef}
          type="text"
          role="combobox"
          aria-expanded="true"
          aria-controls={listId}
          aria-activedescendant={entries.length > 0 ? `${listId}-${highlighted}` : undefined}
          aria-label="Search pages, tasks and runs"
          placeholder="Search pages, tasks and runs"
          value={query}
          onChange={(e) => {
            setQuery(e.target.value)
            // Back to the top on every keystroke: the list under the highlight
            // has changed, so keeping the old index would leave it pointing at
            // a row the operator never looked at.
            setActive(0)
          }}
          className="border-border bg-ground text-text rounded-md border px-3 py-2 text-sm"
        />
        {entries.length === 0 ? (
          <p className="text-text-muted px-3 py-2 text-sm">Nothing matches that.</p>
        ) : (
          /* DIVS AND NOT `<ul>/<li>`, which is the fix rather than a
             suppression: an `<li>` carries an implicit `listitem` role, and
             overriding a non-interactive element with an interactive role is
             the thing the rule is right about. A listbox's children are
             options, not list items, so there was never a list here to mark
             up. It also removes the `getAllByRole('listitem')` trap this tree
             has already been bitten by once. */
          <div id={listId} role="listbox" aria-label="Results" className="flex flex-col">
            {entries.map((entry, i) => (
              // Options are driven from the INPUT's keymap via
              // `aria-activedescendant`; the handler below is the POINTER path
              // only. A key handler here would require focusing options, which
              // the activedescendant pattern specifically forbids.
              // biome-ignore lint/a11y/useKeyWithClickEvents: keyboard lives on the input
              <div
                key={`${entry.kind}:${entry.label}`}
                id={`${listId}-${i}`}
                role="option"
                // -1 AND NOT 0: an activedescendant listbox keeps every option
                // out of the tab sequence, because the input owns focus and
                // announces the highlighted option through `aria-activedescendant`.
                tabIndex={-1}
                aria-selected={i === highlighted}
                data-active={i === highlighted}
                onClick={() => go(entry)}
                onMouseEnter={() => setActive(i)}
                className="text-text data-[active=true]:bg-accent data-[active=true]:text-accent-contrast cursor-pointer rounded-md px-3 py-2 text-sm"
              >
                {entry.label}
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
