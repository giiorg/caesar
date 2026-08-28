import { useSuspenseQuery } from '@tanstack/react-query'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { useMe } from '../../auth/me-query'
import { FormSheet } from '../../ui/form-sheet'
import { WorkflowForm } from '../../workflows/workflow-form'
import { type Workflow, workflowsQueryOptions } from '../../workflows/workflows-query'

/**
 * Workflows. `workflows.tsx` — the Plan 12 stub — is deleted by this task.
 *
 * THE SIBLING ROUTES CARRY THE `_` ESCAPE AND THAT IS NOT COSMETIC.
 * `workflows.$workflowId_.edit.tsx` and
 * `workflows.$workflowId_.versions.$version.tsx` sit beside a
 * `workflows.$workflowId.tsx` that is a real route file, so without the suffix
 * TanStack's flat router makes the detail route their LAYOUT — MEASURED with
 * `tsr generate`, which emits `AppWorkflowsWorkflowIdRouteWithChildren` and
 * lists both as children. The detail component renders no `<Outlet/>`, so both
 * pages would paint the detail screen and nothing else. The URLs are identical
 * either way; the `_` costs nothing but the nesting.
 *
 * `repos.index.tsx` says approvals proves the no-`_` shape, and it does — for
 * approvals, where there is no `approvals.$runId.tsx` FILE for a child to nest
 * into. The precedent that matches this screen is
 * `runs.$runId_.artifacts.$artifactId.tsx`.
 */
export const Route = createFileRoute('/_app/workflows/')({
  loader: ({ context }) => context.queryClient.ensureQueryData(workflowsQueryOptions),
  component: Workflows,
})

function Workflows() {
  const { data: workflows } = useSuspenseQuery(workflowsQueryOptions)
  const navigate = useNavigate()
  const [creating, setCreating] = useState(false)
  /**
   * `POST /workflows` IS ADMIN-ONLY (`workflows/api/api.go`, `adminOnly`), so
   * the role decides whether this screen has a create action at all. A FAB
   * opening a form the API answers 403 to is a control that lies about what
   * the person can do, at the moment they are trying to do it.
   *
   * `useMe` suspends on a query `_app`'s loader has already awaited on every
   * protected route, so it costs no request.
   */
  const canCreate = useMe().user.role === 'admin'

  return (
    <section className="flex flex-col gap-4">
      <h2 className="text-text text-lg font-medium">Workflows</h2>
      <WorkflowCards workflows={workflows} canCreate={canCreate} />
      {/* The FAB pattern, as on Tasks and Repos: the one thing this screen is
          for besides reading. `bottom-tab-bar mb-4` clears the phone tab bar;
          `md:bottom-6 md:mb-0` drops it where the nav is a left rail. */}
      {canCreate && (
        <button
          type="button"
          onClick={() => setCreating(true)}
          className="bg-accent text-accent-contrast fixed right-4 bottom-tab-bar z-10 mb-4 flex items-center justify-center rounded-xl px-5 py-3 text-sm font-medium md:bottom-6 md:mb-0"
        >
          New workflow
        </button>
      )}
      <FormSheet
        open={creating}
        onOpenChange={setCreating}
        title="New workflow"
        description="Name it, then build its first version in the editor."
      >
        {/**
         * CREATION IS TWO WRITES AND THIS IS THE FIRST, which is why success
         * here NAVIGATES rather than closing the sheet the way New project
         * does. `POST /workflows` stores a name and a description; the
         * definition is a separate write through `POST /workflows/{id}/versions`,
         * and a workflow with `latest_version: 0` is a legal row — nothing to
         * roll back, nothing half-created.
         *
         * IF THE SECOND WRITE NEVER HAPPENS, THE LIST IS THE RECOVERY PATH.
         * The card below renders "no versions yet — finish setup" and opens
         * this same editor route, so an operator who closed the tab mid-edit
         * resumes rather than starting again — and a retry of the FIRST write
         * would be refused by the unique name anyway. There is deliberately no
         * cleanup here: nothing to strand and nothing to duplicate.
         */}
        <WorkflowForm
          onDone={(workflow) =>
            navigate({ to: '/workflows/$workflowId/edit', params: { workflowId: workflow.id } })
          }
        />
      </FormSheet>
    </section>
  )
}

/**
 * The workflows, as cards, at every width — `ProjectCards`' call, for
 * `ProjectCards`' reason: a table earns its keep when rows have columns to line
 * up, and a workflow is a name, a sentence and a version number.
 *
 * TWO EMPTY STATES, AND THE DISCRIMINATOR IS DIFFERENT FROM THE REPOS SCREEN'S.
 * There, `handleListProjects` scopes by grant, so an empty list MEANS something
 * different to an admin than to a member. `GET /workflows` is plain
 * `authenticated` and scopes nothing — everyone signed in sees every workflow —
 * so here an empty list means the same fact for both, and only the next move
 * differs. "Create the first" would still be a lie told to a non-admin, who
 * would be sent to a form the API answers 403 to; they are told who can.
 */
function WorkflowCards(props: { workflows: readonly Workflow[]; canCreate: boolean }) {
  if (props.workflows.length === 0) {
    return (
      <p className="text-text-muted text-sm">
        {props.canCreate
          ? 'No workflows are defined yet — create the first.'
          : 'No workflows are defined yet. An admin defines them.'}
      </p>
    )
  }
  return (
    <ul data-testid="workflow-cards" className="flex flex-col gap-2">
      {props.workflows.map((workflow) => (
        <li key={workflow.id}>
          <WorkflowCard workflow={workflow} />
        </li>
      ))}
    </ul>
  )
}

const CARD_CLASS = 'border-border bg-surface flex flex-col gap-1 rounded-md border p-3'

/**
 * ONE CARD, AND WHERE IT GOES DEPENDS ON WHETHER THE SECOND WRITE EVER LANDED.
 *
 * `latest_version: 0` is a workflow whose metadata exists and whose definition
 * does not — the honest state between the two writes, and the only one the
 * server can be left in. Its card goes to the EDITOR, because a detail screen
 * for it would be a header over an empty version history: true, and useless to
 * the person whose actual next move is to finish. Everything else goes to the
 * detail screen.
 *
 * THE WHOLE CARD IS THE LINK, matching `ProjectCards`, `RunCard` and the task
 * rows: on a phone the difference is one tap versus an aimed one.
 */
function WorkflowCard(props: { workflow: Workflow }) {
  const { workflow } = props
  const name = <p className="text-text font-medium">{workflow.name}</p>
  /* A DESCRIPTION IS OPTIONAL SERVER-SIDE and the empty string is what an
     absent one arrives as — required and non-nullable, so `''` is the value,
     not `null`. Rendering it unconditionally puts an empty paragraph under
     every unlabelled workflow. */
  const description =
    workflow.description !== '' ? (
      <p className="text-text-muted text-sm">{workflow.description}</p>
    ) : null

  if (workflow.latest_version === 0) {
    return (
      <Link
        to="/workflows/$workflowId/edit"
        params={{ workflowId: workflow.id }}
        className={CARD_CLASS}
      >
        {name}
        {description}
        <p className="text-text-muted font-mono text-xs">no versions yet — finish setup</p>
      </Link>
    )
  }
  return (
    <Link to="/workflows/$workflowId" params={{ workflowId: workflow.id }} className={CARD_CLASS}>
      {name}
      {description}
      <p className="text-text-muted font-mono text-xs">v{workflow.latest_version}</p>
    </Link>
  )
}
