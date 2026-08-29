import { useEffect, useRef } from 'react'
import { type Card, readDocument, toCard, toDocument } from './derive'

/**
 * `caesar.workflow-draft.<workflowId>` — `caesar.theme`'s naming convention,
 * extended with the id because a draft belongs to one workflow.
 */
const KEY_PREFIX = 'caesar.workflow-draft.'

export const draftKey = (workflowId: string) => `${KEY_PREFIX}${workflowId}`

/**
 * How long typing has to pause before the draft is written.
 *
 * Long enough that a sentence is one write rather than forty, short enough that
 * the phone tab death §6.4 is about — the whole reason drafts exist — lands
 * after a pause the person has already taken.
 */
export const DRAFT_DEBOUNCE_MS = 500

/**
 * A DRAFT IS STORED AS THE DOCUMENT IT WOULD SAVE AS, not as the editor's
 * cards, and that is what buys the reading below its safety.
 *
 * `localStorage` holds a string some older build of this app wrote, so reading
 * it is reading someone else's bytes — exactly the situation `readDocument`
 * exists for, and it already refuses fifteen malformed shapes with tests to
 * match. Storing cards instead would need a second reader of the same care,
 * written from scratch, guarding the same document.
 *
 * The round trip is lossy in one harmless way: `toNode` drops an empty
 * `harness` or `instructions` because `Node`'s `omitempty` does, so a field the
 * person cleared comes back `undefined` rather than `''`. Every control renders
 * `?? ''`, so the two are the same screen.
 */
export function writeDraft(workflowId: string, cards: readonly Card[]): void {
  localStorage.setItem(draftKey(workflowId), JSON.stringify(toDocument(cards)))
}

/** `null` means no draft — including a draft this build cannot read. */
export function readDraft(workflowId: string): Card[] | null {
  const raw = localStorage.getItem(draftKey(workflowId))
  if (raw === null) return null
  const doc = readDocument(parse(raw))
  // A draft that will not read is DISCARDED SILENTLY rather than announced.
  // There is nothing the person can do about it and nothing of theirs is lost
  // that telling them would recover — the alternative is a screen that opens
  // with an error about storage instead of the editor they asked for.
  if (doc === null) return null
  return doc.nodes.map(toCard)
}

/**
 * THERE IS DELIBERATELY NO `clearDraft` HERE. The one thing that clears a draft
 * is a successful save, and save is Task 28 — an exported clear with no
 * production caller is a seam whose tests all pass against code nothing runs.
 * It arrives with the mutation that calls it.
 */
function parse(raw: string): unknown {
  try {
    return JSON.parse(raw)
  } catch {
    return null
  }
}

/**
 * Persist the outline as it changes, debounced.
 *
 * THE FIRST RUN IS SKIPPED, and it is not an optimization. `useEffect` fires on
 * mount as well as on change, so without the guard merely OPENING the editor
 * and leaving writes a draft of whatever it opened with — an empty outline for
 * a fresh editor. That draft is then indistinguishable from real unsaved work,
 * and since a draft outranks prefill, a later `/edit?from=5` would restore the
 * empty outline and silently refuse to prefill. A draft is work the person did;
 * arriving is not work.
 *
 * The timer is reset by every change, which is what makes it a debounce rather
 * than a throttle: the write lands once the typing stops.
 */
export function useDraftPersistence(workflowId: string, cards: readonly Card[]): void {
  const mounted = useRef(false)
  useEffect(() => {
    if (!mounted.current) {
      mounted.current = true
      return
    }
    const timer = setTimeout(() => writeDraft(workflowId, cards), DRAFT_DEBOUNCE_MS)
    return () => clearTimeout(timer)
  }, [workflowId, cards])
}
