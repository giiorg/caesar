import { useSuspenseQuery } from '@tanstack/react-query'
import { createFileRoute, Link } from '@tanstack/react-router'
import { DagPreview } from '../../workflows/dag-preview'
import { readDocument, toCard } from '../../workflows/derive'
import { OutlineView } from '../../workflows/outline-view'
import { workflowVersionQueryOptions } from '../../workflows/workflows-query'

/**
 * One registered version, read-only — the definition as the runner will see it.
 *
 * `$workflowId_` CARRIES THE ESCAPE SUFFIX. Without it TanStack's flat router
 * makes `workflows.$workflowId.tsx` this route's LAYOUT — measured with `tsr
 * generate`, which emits the detail route `WithChildren` and lists this file
 * among them — and the detail component renders no `<Outlet/>`, so this page
 * would paint the detail screen and nothing of its own. The URL is identical
 * either way. Same escape, same reason, as
 * `runs.$runId_.artifacts.$artifactId.tsx`.
 *
 * NO PARALLEL FETCH OF THE WORKFLOW ITSELF. `WorkflowVersionResponse` carries
 * `workflow_id` and nothing else about its parent, so a name in the heading
 * would cost a second request for one string — and the back link below needs
 * only the id, which is in the path. The heading names what this page IS.
 */
export const Route = createFileRoute('/_app/workflows/$workflowId_/versions/$version')({
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(
      workflowVersionQueryOptions(params.workflowId, params.version),
    ),
  component: WorkflowVersionView,
})

function WorkflowVersionView() {
  const { workflowId, version } = Route.useParams()
  const { data } = useSuspenseQuery(workflowVersionQueryOptions(workflowId, version))
  // READ ONCE, FOR THE WHOLE PAGE. Both projections below are readings of the
  // same document, so unreadability is a property of the page's data rather
  // than of one region — reading it inside each would put two copies of the
  // same sentence on one screen the moment the outline lands beside the
  // preview.
  // `definition`, not `document`: this file renders into a DOM and the global
  // of that name is a keystroke away.
  const definition = readDocument(data.document)

  return (
    <div className="flex flex-col gap-6">
      <section className="flex flex-col gap-2">
        <h2 className="text-text text-lg font-medium">v{data.version}</h2>
        {/* The whole hash, not a prefix: the history list truncates for
            scanning, and this is the page a person opens to check one. */}
        <p className="text-text-muted font-mono text-xs break-all">{data.content_hash}</p>
        <time dateTime={data.created_at} className="text-text-muted font-mono text-xs">
          {data.created_at}
        </time>
        <Link
          to="/workflows/$workflowId"
          params={{ workflowId }}
          className="border-border text-text self-start rounded-md border px-3 py-2 text-sm font-medium"
        >
          All versions
        </Link>
      </section>

      {/* THE TWO PROJECTIONS §6.1 NAMES, both built now. The outline is the
          editor's own renderer with its controls off — one component, so a
          document that draws here draws identically in the editor's read-only
          mode, and neither can drift into showing a field the other hides.

          IT RENDERS ANY DOCUMENT THE API WILL RETURN, chain-shaped or not: a
          branching definition written through the API is unreachable for the
          editor and perfectly ordinary for the runner, so this view consults no
          grammar and lists the nodes in document order.

          `null` IS "UNREADABLE", NOT "EMPTY", and the two must not render the
          same way. `Validate` refuses a document with no nodes, so an empty
          definition cannot come off the wire; a page that drew an empty picture
          here would describe a state the system does not have, and would send
          an operator looking for a workflow problem instead of a client one. */}
      {definition === null ? (
        <p className="text-danger text-sm">
          This definition could not be read by this version of the app.
        </p>
      ) : (
        <>
          <section data-testid="dag-preview-slot">
            <DagPreview doc={definition} />
          </section>
          <section data-testid="outline-slot">
            <OutlineView cards={definition.nodes.map(toCard)} />
          </section>
        </>
      )}
    </div>
  )
}
