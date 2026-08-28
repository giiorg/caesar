import { useMutation, useQueryClient, useSuspenseQuery } from '@tanstack/react-query'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { api, unwrap } from '../../api/client'
import { asApiError, ErrorSurface } from '../../errors/error-surface'
import { FormSheet } from '../../ui/form-sheet'
import { WorkflowForm } from '../../workflows/workflow-form'
import {
  type Workflow,
  type WorkflowVersionMeta,
  workflowQueryOptions,
  workflowVersionsQueryOptions,
} from '../../workflows/workflows-query'

/**
 * One workflow: what it is, and every definition version ever registered on it.
 *
 * BOTH READS LOAD IN PARALLEL AND BOTH MAY REJECT, the shape `/repos/$projectId`
 * takes. A 404 on either lands at `_app`'s boundary as `ApiErrorPanel`, the
 * only surface outside a form that renders `correlation_id`.
 *
 * NOTE `ListVersions` 404s ON THE WORKFLOW ITSELF before it lists anything
 * (`workflows/workflows.go` calls `GetWorkflow` first), so a bad id in the
 * address bar fails both reads with the same sentence rather than one of them
 * with a confusing one.
 */
export const Route = createFileRoute('/_app/workflows/$workflowId')({
  loader: ({ context, params }) =>
    Promise.all([
      context.queryClient.ensureQueryData(workflowQueryOptions(params.workflowId)),
      context.queryClient.ensureQueryData(workflowVersionsQueryOptions(params.workflowId)),
    ]),
  component: WorkflowDetail,
})

function WorkflowDetail() {
  const { workflowId } = Route.useParams()
  const { data: workflow } = useSuspenseQuery(workflowQueryOptions(workflowId))
  const { data: versions } = useSuspenseQuery(workflowVersionsQueryOptions(workflowId))

  return (
    <div className="flex flex-col gap-6">
      <WorkflowHeader workflow={workflow} />
      <VersionHistory workflowId={workflowId} versions={versions} />
    </div>
  )
}

/**
 * The workflow's identity and the three things that can be done to it.
 *
 * DELETE NAVIGATES AWAY ON SUCCESS, because the screen ceases to describe
 * anything real. A refusal STAYS PUT: `DeleteWorkflow` classifies the
 * task→workflow_version RESTRICT as `ClassConflict` "workflow has runs; delete
 * is blocked" (`workflows/workflows.go`), and the operator's next move is the
 * version list right below, which is still there.
 *
 * THE REFUSAL IS RENDERED VERBATIM through `ErrorSurface`, never paraphrased.
 * Task 16 settled that rule on the repo delete: a client sentence would state a
 * cause the server did not find. `ErrorSurface`'s null arm covers the other
 * failure — a dropped connection has no status, no problem document and no
 * correlation id — so a 409 reads as a 409 and a dead backend reads as a dead
 * backend.
 *
 * EDIT IS UNGATED even though `PATCH` and `DELETE` are both `adminOnly`. That
 * is `RepoSection`'s call and its reason holds: a client-side role gate here
 * would duplicate a check that can drift, and a 403 from either button reaches
 * `ApiErrorPanel` already. The LIST's create action is gated for the different
 * reason stated there — a FAB is the screen's headline affordance, not a
 * recovery from a refusal.
 */
function WorkflowHeader(props: { workflow: Workflow }) {
  const { workflow } = props
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [editing, setEditing] = useState(false)

  const remove = useMutation({
    mutationFn: async () =>
      unwrap(await api.DELETE('/api/v1/workflows/{id}', { params: { path: { id: workflow.id } } })),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['workflows'] })
      await navigate({ to: '/workflows' })
    },
  })

  return (
    <section className="flex flex-col gap-2">
      <h2 className="text-text text-lg font-medium">{workflow.name}</h2>
      {workflow.description !== '' && (
        <p className="text-text-muted text-sm">{workflow.description}</p>
      )}
      <div className="flex gap-2">
        <button type="button" onClick={() => setEditing(true)} className={ACTION_CLASS}>
          Edit workflow
        </button>
        <Link
          to="/workflows/$workflowId/edit"
          params={{ workflowId: workflow.id }}
          className={ACTION_CLASS}
        >
          {/* A NEW VERSION, NOT AN EDIT OF THE CURRENT ONE, and the label says
              so because versions are immutable: the editor opens the latest
              definition and saving registers the NEXT number. Calling this
              "Edit definition" would promise an in-place change the write door
              does not perform. */}
          New version
        </Link>
        <button
          type="button"
          onClick={() => remove.mutate()}
          disabled={remove.isPending}
          className={ACTION_CLASS}
        >
          Delete workflow
        </button>
      </div>
      {remove.isError && <ErrorSurface failure={asApiError(remove.error)} />}
      <FormSheet
        open={editing}
        onOpenChange={setEditing}
        title="Edit workflow"
        description="Rename the workflow or change what it says it is for. The definition is versioned separately."
      >
        <WorkflowForm workflow={workflow} onDone={() => setEditing(false)} />
      </FormSheet>
    </section>
  )
}

/**
 * Every version, newest first — the server's order, not a client re-sort.
 *
 * THE EMPTY STATE IS THE HALF-CREATED WORKFLOW, reached by anyone who typed the
 * detail address for a `latest_version: 0` row rather than following its card
 * (which goes straight to the editor). It is distinct from the LIST's two empty
 * sentences because it is about one workflow rather than about the deployment,
 * and it names the same next move the card does.
 */
function VersionHistory(props: { workflowId: string; versions: readonly WorkflowVersionMeta[] }) {
  return (
    <section className="flex flex-col gap-3">
      <h3 className="text-text font-medium">Versions</h3>
      {props.versions.length === 0 ? (
        <p className="text-text-muted text-sm">
          No versions yet — this workflow has a name but no definition.
        </p>
      ) : (
        <ul data-testid="version-history" className="flex flex-col gap-2">
          {props.versions.map((version) => (
            <li key={version.version}>
              <Link
                to="/workflows/$workflowId/versions/$version"
                params={{ workflowId: props.workflowId, version: String(version.version) }}
                className="border-border bg-surface flex flex-col gap-1 rounded-md border p-3"
              >
                <p className="text-text font-medium">v{version.version}</p>
                {/* THE HASH IS TRUNCATED FOR READING AND WHOLE FOR COMPARING.
                    A content hash is 64 hex characters — on a phone that is
                    three wrapped lines of noise per row, and the reason anyone
                    reads one is to check it against another, which needs all of
                    it. `title` is the disclosure that costs no layout; the
                    element's TEXT is the short form, so an assertion on the
                    full hash proves the attribute rather than the body. */}
                <p title={version.content_hash} className="text-text-muted font-mono text-xs">
                  {version.content_hash.slice(0, HASH_PREFIX)}
                </p>
                {/* Raw ISO in a <time>, the shape `decision-history.tsx` set
                    for V1 and `audit-section.tsx` followed. */}
                <time dateTime={version.created_at} className="text-text-muted font-mono text-xs">
                  {version.created_at}
                </time>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

/**
 * Twelve, not eight. `git`'s short form is seven and collides at scale, and the
 * comparison this prefix exists for is between two rows on ONE workflow — a
 * handful of values, all produced by the same canonicalizer. Twelve hex
 * characters is 48 bits, which is a length a person can still read across at a
 * glance.
 */
const HASH_PREFIX = 12

/** The secondary-action style, spelled once — `repos.$projectId.tsx`'s. */
const ACTION_CLASS =
  'border-border text-text self-start rounded-md border px-3 py-2 text-sm font-medium'
