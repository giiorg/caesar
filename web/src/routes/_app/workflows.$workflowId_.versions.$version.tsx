import { useSuspenseQuery } from '@tanstack/react-query'
import { createFileRoute, Link } from '@tanstack/react-router'
import { StubPanel } from '../../ui/stub-panel'
import { readNodeIds, workflowVersionQueryOptions } from '../../workflows/workflows-query'

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
  const nodeIds = readNodeIds(data.document)

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

      {/* THE TWO SLOTS THIS PAGE EXISTS TO HOLD, and neither is built yet.
          Task 26 replaces the first with the SVG DAG preview and Task 27 the
          second with the read-only typed outline it shares with the editor.
          They are `StubPanel`s rather than empty divs because a stub never
          fakes success: a bordered box with nothing in it is indistinguishable,
          to the person looking at it, from a preview that failed to draw. */}
      <section data-testid="dag-preview-slot">
        <StubPanel plan="Plan 12b Task 26" what="The DAG preview" />
      </section>
      <section data-testid="outline-slot" className="flex flex-col gap-2">
        <StubPanel plan="Plan 12b Task 27" what="The typed outline" />
        <NodeIds ids={nodeIds} />
      </section>
    </div>
  )
}

/**
 * THE DOCUMENT ITSELF, at the only depth this task can honestly read it.
 *
 * It is here so that the page's central claim — the definition arrived, whole,
 * and this is which one — is on the screen and under test before Task 25's
 * parser exists. Both slots above render identically for every version, so a
 * test that asserted only on them would pass against a page that never fetched
 * anything.
 *
 * `null` IS "UNREADABLE", NOT "EMPTY", and the two must not be rendered the
 * same way. `Validate` refuses a document with no nodes, so an empty list
 * cannot come off the wire — a page that said "no steps" for an unreadable
 * document would be describing a state the system does not have, and the
 * operator would go looking for a workflow problem instead of a client one.
 */
function NodeIds(props: { ids: readonly string[] | null }) {
  if (props.ids === null) {
    return (
      <p className="text-danger text-sm">
        This definition could not be read by this version of the app.
      </p>
    )
  }
  return (
    <ul data-testid="node-ids" className="flex flex-wrap gap-2">
      {props.ids.map((id) => (
        <li
          key={id}
          className="border-border text-text rounded-md border px-2 py-1 font-mono text-xs"
        >
          {id}
        </li>
      ))}
    </ul>
  )
}
