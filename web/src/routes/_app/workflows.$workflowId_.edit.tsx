import { useQuery, useSuspenseQuery } from '@tanstack/react-query'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useReducer, useState } from 'react'
import { z } from 'zod'
import { AdminRefusal } from '../../admin/admin-refusal'
import { harnessesQueryOptions } from '../../admin/harnesses-query'
import { useMe } from '../../auth/me-query'
import { StubPanel } from '../../ui/stub-panel'
import { DagPreview } from '../../workflows/dag-preview'
import {
  type Card,
  type DocShape,
  isChainShaped,
  readDocument,
  toCard,
  toDocument,
} from '../../workflows/derive'
import { readDraft, useDraftPersistence } from '../../workflows/drafts'
import { EMPTY_EDITOR, editorReducer } from '../../workflows/editor-state'
import { OutlineView } from '../../workflows/outline-view'
import { workflowVersionQueryOptions } from '../../workflows/workflows-query'

/**
 * The definition editor — an ordered outline of typed cards with the DAG
 * drawing itself above them as they change.
 *
 * AUTHORING IS "NEW VERSION", NEVER "EDIT IN PLACE" (§6.2). Versions are
 * immutable content-hashed rows, so this screen never modifies one: it starts
 * from scratch, or PREFILLS from an existing version and saves a new one. The
 * heading says so, because a screen reached from a version's page could
 * otherwise be read as editing it.
 *
 * `$workflowId_` CARRIES THE ESCAPE SUFFIX, for the reason the version view
 * records at length: without it, `workflows.$workflowId.tsx` becomes this
 * route's layout and this page paints the detail screen instead of itself. The
 * plan's file list spells this route without the `_`, a path that does not
 * exist.
 *
 * IT IS AN ADMIN SCREEN because `createWorkflowVersion` is `adminOnly`
 * (`workflows/api/api.go`). A member who follows a link here is told so rather
 * than handed an editor whose save answers 403 — the same call
 * `workflows.index.tsx` makes about its New button, and the reason there is
 * the reason here: a control that lies about what the person can do, at the
 * moment they try to do it, is worse than no control.
 */
const searchSchema = z.object({ from: z.number().int().positive().optional() })

export const Route = createFileRoute('/_app/workflows/$workflowId_/edit')({
  validateSearch: (search: Record<string, unknown>) => {
    const parsed = searchSchema.safeParse(search)
    return parsed.success ? parsed.data : {}
  },
  loaderDeps: ({ search }) => ({ from: versionOrdinal(search.from) }),
  /**
   * ONLY THE VERSION, and only when one was asked for.
   *
   * The harness registry is deliberately NOT prefetched here, for the reason
   * `admin.tsx` states about its own sections: `GET /harnesses` is admin-only,
   * so a route-level prefetch fires a request that 403s for a member before the
   * component that knows how to explain the refusal has rendered. `GET
   * /workflows/{id}/versions/{version}` carries no such restriction.
   */
  loader: ({ context, params, deps }) =>
    deps.from === undefined
      ? undefined
      : context.queryClient.ensureQueryData(
          workflowVersionQueryOptions(params.workflowId, String(deps.from)),
        ),
  component: WorkflowEditorScreen,
})

/**
 * A version ordinal, re-narrowed at the read site.
 *
 * `validateSearch` REJECTING A VALUE DOES NOT REMOVE IT — measured twice in
 * this tree and written up in `runs.$runId.tsx`: a route's search is its
 * ancestors' search with its own validated result merged over it, and nothing
 * between `/` and here declares one, so the raw object survives whatever this
 * schema says. `?from=true` really can reach `useSearch()`. Everything this
 * refuses lands where "no version asked for" lands, which is a fresh editor —
 * the same place a link with no `from` at all lands.
 */
function versionOrdinal(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isInteger(value) && value > 0 ? value : undefined
}

function WorkflowEditorScreen() {
  const { workflowId } = Route.useParams()
  const from = versionOrdinal(Route.useSearch().from)
  const isAdmin = useMe().user.role === 'admin'
  /**
   * A DRAFT OUTRANKS A PREFILL, and it is read ONCE per mount.
   *
   * A draft is work this person did and did not save; a prefill is a version
   * still sitting on the server, reproducible by coming back. Restoring the
   * draft is the whole reason §6.4 puts one in `localStorage` — "so a phone tab
   * death doesn't eat ten cards of typing" — and it would buy nothing if
   * arriving with `?from=` threw it away.
   */
  const [draft] = useState(() => (isAdmin ? readDraft(workflowId) : null))

  if (!isAdmin) {
    return (
      <section className="flex flex-col gap-4">
        <h2 className="text-text text-lg font-medium">Workflow definition</h2>
        <p role="alert" className="text-text-muted text-sm">
          Editing a workflow definition is admin-only. Ask an admin to make the change.
        </p>
        <BackToWorkflow workflowId={workflowId} />
      </section>
    )
  }
  if (draft !== null) return <Editor workflowId={workflowId} initialCards={draft} restored />
  if (from === undefined) return <Editor workflowId={workflowId} initialCards={[]} />
  return <PrefilledEditor workflowId={workflowId} from={from} />
}

/**
 * A new version starting from an existing one — or, when that one did not come
 * out of this editor, its read-only projection.
 *
 * THE REFUSAL IS `isChainShaped`'s, NOT A GUESS. A document opens editable iff
 * re-deriving the edges from its node sequence reproduces the stored edge set
 * exactly; a branching document, a lone plan node and two gates in sequence all
 * pass the server's `Validate` and none of them is representable as an ordered
 * card list. Opening one in the editor would silently rewrite it on the next
 * save into the nearest thing the editor can say.
 */
function PrefilledEditor(props: { workflowId: string; from: number }) {
  const { data } = useSuspenseQuery(
    workflowVersionQueryOptions(props.workflowId, String(props.from)),
  )
  const definition = readDocument(data.document)

  if (definition === null) {
    return (
      <Screen workflowId={props.workflowId} heading="New version">
        <p className="text-danger text-sm">
          v{props.from} could not be read by this version of the app, so it cannot be used as a
          starting point.
        </p>
      </Screen>
    )
  }
  if (!isChainShaped(definition)) {
    return <ForeignDocument workflowId={props.workflowId} from={props.from} doc={definition} />
  }
  return (
    <Editor
      workflowId={props.workflowId}
      initialCards={definition.nodes.map(toCard)}
      from={props.from}
    />
  )
}

/**
 * A version this editor cannot round-trip, shown rather than refused.
 *
 * IT IS THE SAME `OutlineView` WITH ITS CONTROLS OFF, which is the whole reason
 * that component takes its edit handles as one optional bundle: the read-only
 * body here and the version read view's are the same rows, so a document that
 * renders on one renders on the other.
 */
function ForeignDocument(props: { workflowId: string; from: number; doc: DocShape }) {
  return (
    <Screen workflowId={props.workflowId} heading={`v${props.from}`}>
      <p role="alert" className="text-text-muted text-sm">
        This version was authored outside the editor, so it is shown read-only. Its steps do not
        form the ordered sequence the editor builds.
      </p>
      <DagPreview doc={props.doc} />
      <OutlineView cards={props.doc.nodes.map(toCard)} />
    </Screen>
  )
}

function Editor(props: {
  workflowId: string
  initialCards: readonly Card[]
  from?: number
  restored?: boolean
}) {
  const [state, dispatch] = useReducer(editorReducer, {
    ...EMPTY_EDITOR,
    cards: props.initialCards,
  })
  useDraftPersistence(props.workflowId, state.cards)
  const harnesses = useQuery(harnessesQueryOptions)

  return (
    <Screen workflowId={props.workflowId} heading="New version">
      {props.restored === true && (
        <p role="status" className="text-text-muted text-sm">
          Restored from an unsaved draft on this device.
        </p>
      )}
      {props.from !== undefined && (
        <p className="text-text-muted text-sm">Starting from v{props.from}.</p>
      )}

      {/* THE PREVIEW IS LIVE AND COSTS NOTHING. `toDocument` is pure — no
          network, no cache — so it re-derives on every keystroke, which is what
          locked variant B means by putting the preview IN the editor. It draws
          whatever the outline currently is, including the intermediate states
          that are not yet a legal document. */}
      <DagPreview doc={toDocument(state.cards)} />

      {harnesses.isError && <AdminRefusal error={harnesses.error} />}

      <OutlineView
        cards={state.cards}
        edit={{
          dispatch,
          harnesses: (harnesses.data ?? []).map((h) => h.name),
          refusal: state.refusal,
          note: state.note,
        }}
      />

      {/* Saving is Task 28's, and a `StubPanel` says so out loud. A Save button
          that did nothing — or one disabled with a reason that never clears —
          would be the plausible-looking control a stub exists not to fake. */}
      <StubPanel plan="Plan 12b Task 28" what="Saving a new version" />
    </Screen>
  )
}

function Screen(props: { workflowId: string; heading: string; children: React.ReactNode }) {
  return (
    <section className="flex flex-col gap-4">
      <h2 className="text-text text-lg font-medium">{props.heading}</h2>
      {props.children}
      <BackToWorkflow workflowId={props.workflowId} />
    </section>
  )
}

function BackToWorkflow(props: { workflowId: string }) {
  return (
    <Link
      to="/workflows/$workflowId"
      params={{ workflowId: props.workflowId }}
      className="border-border text-text self-start rounded-md border px-3 py-2 text-sm font-medium"
    >
      All versions
    </Link>
  )
}
