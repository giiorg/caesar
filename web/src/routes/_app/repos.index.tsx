import { useSuspenseQuery } from '@tanstack/react-query'
import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { ProjectCards } from '../../projects/project-cards'
import { ProjectForm } from '../../projects/project-form'
import { projectsQueryOptions } from '../../projects/projects-query'
import { FormSheet } from '../../ui/form-sheet'
import { useMe } from '../_app'

/**
 * Repos, and the screen is a list of PROJECTS — which is the API's shape rather
 * than a hierarchy invented for the navigation. There is no unscoped repo
 * collection: the only listing endpoint is `GET /projects/{id}/repos`
 * (`assets/api/api.go`), so a flat repo list would be N requests to build and
 * would have to guess N from somewhere. The nav entry keeps its name because
 * repos are what a person comes here for; projects are how the system stores
 * them.
 *
 * `repos.tsx` — the Plan 12 stub — is deleted by this task, and the two files
 * that replace it are `repos.index.tsx` and `repos.$projectId.tsx` with no
 * layout route between them. That is the shape `approvals.index.tsx` and
 * `approvals.$runId.$nodeId.tsx` already prove, and it is why neither needs the
 * `_` escape suffix that `runs.$runId_.artifacts.$artifactId.tsx` carries.
 */
export const Route = createFileRoute('/_app/repos/')({
  loader: ({ context }) => context.queryClient.ensureQueryData(projectsQueryOptions),
  component: Repos,
})

function Repos() {
  const { data: projects } = useSuspenseQuery(projectsQueryOptions)
  const [creating, setCreating] = useState(false)
  /**
   * `POST /projects` IS ADMIN-ONLY (`assets/api/api.go`, `adminOnly`), so the
   * role decides whether this screen has a create action at all — a FAB that
   * opens a form the API answers 403 to is a control that lies about what the
   * person can do, and it lies at the exact moment they are trying to do it.
   *
   * The same bit picks which empty sentence the card list shows, which is why
   * it is read here and passed down rather than read twice.
   *
   * `useMe` suspends on a query `_app`'s loader has already awaited on every
   * protected route, so this costs no request.
   */
  const canCreate = useMe().user.role === 'admin'

  return (
    <section className="flex flex-col gap-4">
      <h2 className="text-text text-lg font-medium">Projects</h2>
      <ProjectCards projects={projects} canCreate={canCreate} />
      {/* THE FAB PATTERN, as on Tasks: the one thing this screen is for besides
          reading, so it is always reachable rather than a link under a list
          whose length nobody controls. `bottom-tab-bar mb-4` clears the phone
          tab bar; `md:bottom-6 md:mb-0` drops it back down where the nav is a
          left rail and there is nothing to clear. */}
      {canCreate && (
        <button
          type="button"
          onClick={() => setCreating(true)}
          className="bg-accent text-accent-contrast fixed right-4 bottom-tab-bar z-10 mb-4 flex items-center justify-center rounded-xl px-5 py-3 text-sm font-medium md:bottom-6 md:mb-0"
        >
          New project
        </button>
      )}
      <FormSheet
        open={creating}
        onOpenChange={setCreating}
        title="New project"
        description="A project holds repos and the grants that say who may use them."
      >
        {/* CLOSING IS THE WHOLE SUCCESS STATE, and deliberately not a
            navigation. `ProjectForm` has already invalidated `['projects']`, so
            the card the operator just made is in the list behind the sheet —
            unlike a new task, whose honest landing place is the run it started.
            Nothing has happened here that the operator has to go and watch. */}
        <ProjectForm onDone={() => setCreating(false)} />
      </FormSheet>
    </section>
  )
}
