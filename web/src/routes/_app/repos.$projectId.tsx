import { useMutation, useQueryClient, useSuspenseQuery } from '@tanstack/react-query'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useState } from 'react'
import { api, unwrap } from '../../api/client'
import { useMe } from '../../auth/me-query'
import { asApiError, ErrorSurface } from '../../errors/error-surface'
import { canManageProject, GrantsSection } from '../../projects/grants-section'
import { ProjectForm } from '../../projects/project-form'
import {
  type Project,
  projectQueryOptions,
  projectReposQueryOptions,
  type Repo,
} from '../../projects/projects-query'
import { RepoActivity } from '../../projects/repo-activity'
import { RepoForm } from '../../projects/repo-form'
import { FormSheet } from '../../ui/form-sheet'

/**
 * One project: what it is, and the repos registered on it.
 *
 * BOTH READS ARE LOADED IN PARALLEL AND BOTH MAY REJECT, the shape `/tasks`
 * already takes. A 404 on the project and a 403 on a project somebody linked
 * you into both land at `_app`'s boundary as `ApiErrorPanel`, which is the only
 * surface outside a form that renders `correlation_id` — and whose 403 branch
 * says what to ask for rather than inviting a retry that cannot succeed.
 *
 * THE GRANTS SECTION IS HERE because the API is project-nested and a grant is a
 * property of a project — and it is CONDITIONAL, because every one of its
 * endpoints (the list included) enforces `manage`. `canManageProject` is asked
 * once, here, so the decision is visible beside the screen rather than buried
 * in the section's own render.
 */
export const Route = createFileRoute('/_app/repos/$projectId')({
  loader: ({ context, params }) =>
    Promise.all([
      context.queryClient.ensureQueryData(projectQueryOptions(params.projectId)),
      context.queryClient.ensureQueryData(projectReposQueryOptions(params.projectId)),
    ]),
  component: ProjectDetail,
})

function ProjectDetail() {
  const { projectId } = Route.useParams()
  const { data: project } = useSuspenseQuery(projectQueryOptions(projectId))
  const { data: repos } = useSuspenseQuery(projectReposQueryOptions(projectId))
  // `useMe` suspends on a query `_app`'s loader has already awaited on every
  // protected route, so it resolves from the cache and adds no request here.
  const canManage = canManageProject(useMe(), projectId)

  return (
    <div className="flex flex-col gap-6">
      <ProjectHeader project={project} />
      <RepoSection projectId={projectId} repos={repos} />
      {canManage && <GrantsSection projectId={projectId} />}
    </div>
  )
}

/**
 * The project's own identity and the two things that can be done to it.
 *
 * DELETE NAVIGATES AWAY ON SUCCESS, because the screen it is on ceases to
 * describe anything real. A refusal stays put: `DeleteProject` answers
 * `ClassConflict` "project still has repos" (`assets/projects.go`) for a
 * project that is not empty, and the operator's next move is to look at the
 * list right below — which is still there.
 */
function ProjectHeader(props: { project: Project }) {
  const { project } = props
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const [editing, setEditing] = useState(false)

  const remove = useMutation({
    mutationFn: async () =>
      unwrap(await api.DELETE('/api/v1/projects/{id}', { params: { path: { id: project.id } } })),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ['projects'] })
      await navigate({ to: '/repos' })
    },
  })

  return (
    <section className="flex flex-col gap-2">
      <h2 className="text-text text-lg font-medium">{project.name}</h2>
      {project.description !== '' && (
        <p className="text-text-muted text-sm">{project.description}</p>
      )}
      <div className="flex gap-2">
        <button type="button" onClick={() => setEditing(true)} className={ACTION_CLASS}>
          Edit project
        </button>
        <button
          type="button"
          onClick={() => remove.mutate()}
          disabled={remove.isPending}
          className={ACTION_CLASS}
        >
          Delete project
        </button>
      </div>
      {/* `ErrorSurface` RATHER THAN A SENTENCE OF THIS SCREEN'S OWN: its
          `ApiError` arm prints the server's own detail plus the correlation id,
          and its null arm is the no-status case where there is neither. A 409
          therefore reads as "project still has repos" and a dropped connection
          reads as a dropped connection, which is the whole distinction this
          control needs to make. */}
      {remove.isError && <ErrorSurface failure={asApiError(remove.error)} />}
      <FormSheet
        open={editing}
        onOpenChange={setEditing}
        title="Edit project"
        description="Rename the project or change what it says it is for."
      >
        <ProjectForm project={project} onDone={() => setEditing(false)} />
      </FormSheet>
    </section>
  )
}

/**
 * The repos, in BOTH projections at once.
 *
 * The locked mobile-first standard is card-first lists with desktop table
 * projections, and this is the first list in the tree with columns worth
 * lining up — slug, forge, default branch. So the cards and the table are two
 * renderings of ONE array, in one component tree, with one fetch behind them;
 * Tailwind hides whichever is not the current width.
 *
 * BOTH ARE IN THE DOM AT ONCE, AND IN A TEST BOTH ARE VISIBLE. jsdom loads no
 * stylesheet, so `hidden` never becomes `display:none` there and every slug,
 * every button and every link exists twice — which is why both projections
 * carry a `data-testid` and every assertion in the suite scopes through one.
 * The shell has the same shape and the same scar: the phone tab bar and the
 * desktop rail are both mounted, which is where the one-`<nav>`-landmark rule
 * comes from. Nothing here adds a landmark.
 *
 * THE DIALOGS, THE ERROR AND THE ACTIVITY PANEL LIVE HERE, NOT IN A ROW. Two
 * copies of a row means two copies of everything inside it, and a sheet
 * rendered per row would put two dialogs in the document for one repo. One
 * `editing` value beside the list — set by whichever copy of the button was
 * pressed — keeps a single dialog and a single error surface, and `expanded`
 * does the same job for the one open repo's activity.
 *
 * ONE OPEN REPO AT A TIME, WHICH IS A FETCH DECISION AS MUCH AS A LAYOUT ONE.
 * Activity is a runs query per repo (`['runs', {repoId}]`); rendering it inline
 * under every row would fire one per repo on first paint. React-query folds the
 * two projections onto one key, but not twenty repos onto one request.
 *
 * REPO WRITES STAY UNGATED, deliberately, even though the ladder is now spelled
 * out in `grants-section.tsx` and could be consulted here. `manage` is what the
 * server requires for them, and a client-side gate would only duplicate that
 * check where it can drift; a 403 from any of these buttons reaches
 * `ApiErrorPanel`'s grant branch, which already says what to ask for. The
 * grants section is gated for a different reason — see the route component —
 * and the difference is that its READ is forbidden too, so an ungated render
 * there produces an error panel rather than a working screen.
 */
function RepoSection(props: { projectId: string; repos: readonly Repo[] }) {
  const queryClient = useQueryClient()
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<Repo | undefined>(undefined)
  const [expanded, setExpanded] = useState<Repo | undefined>(undefined)

  const remove = useMutation({
    mutationFn: async (repo: Repo) =>
      unwrap(await api.DELETE('/api/v1/repos/{id}', { params: { path: { id: repo.id } } })),
    onSuccess: () =>
      queryClient.invalidateQueries({ queryKey: ['project-repos', props.projectId] }),
  })

  const rowProps = {
    onEdit: setEditing,
    onDelete: (repo: Repo) => remove.mutate(repo),
    // A SECOND PRESS ON THE OPEN ROW CLOSES IT. Compared by id rather than by
    // identity: `repos` is a fresh array after any refetch, so the object the
    // button hands back is not the one in state.
    onToggleActivity: (repo: Repo) =>
      setExpanded((open) => (open?.id === repo.id ? undefined : repo)),
  }

  return (
    <section className="flex flex-col gap-3">
      <h3 className="text-text font-medium">Repos</h3>
      {props.repos.length === 0 ? (
        /* DISTINCT FROM THE PROJECT LIST'S TWO SENTENCES, and it has to be:
           this one is reached by somebody who can already see the project, so
           "nothing is shared with you" would be false and "create the first
           project" would be about the wrong thing. */
        <p className="text-text-muted text-sm">No repos are registered on this project yet.</p>
      ) : (
        <>
          <RepoCards repos={props.repos} {...rowProps} />
          <RepoTable repos={props.repos} {...rowProps} />
        </>
      )}
      {/* ONCE, BELOW BOTH PROJECTIONS — the `editing` sheet's argument applied
          to a panel. Rendered inside the rows it would exist twice per repo in
          jsdom, and on a real desktop it would have to be built twice: as a
          nested list in the cards and as a full-width `<tr>` in the table. */}
      {expanded !== undefined && <RepoActivity repo={expanded} />}
      {remove.isError && <ErrorSurface failure={asApiError(remove.error)} />}
      <button type="button" onClick={() => setCreating(true)} className={ACTION_CLASS}>
        New repo
      </button>
      <FormSheet
        open={creating}
        onOpenChange={setCreating}
        title="New repo"
        description="Register a repository on this project. The forge cannot be changed afterwards."
      >
        <RepoForm projectId={props.projectId} onDone={() => setCreating(false)} />
      </FormSheet>
      <FormSheet
        open={editing !== undefined}
        onOpenChange={(open) => {
          if (!open) setEditing(undefined)
        }}
        title="Edit repo"
        description="The forge was fixed when the repo was registered."
      >
        {/* MOUNTED ONLY WHILE A REPO IS BEING EDITED, so `RepoForm` gets its
            defaults from that repo on the render that opens the sheet. Kept
            mounted across the closing, it would hold the previous repo's slug
            in an input the next repo's sheet then displayed. */}
        {editing !== undefined && (
          <RepoForm
            projectId={props.projectId}
            repo={editing}
            onDone={() => setEditing(undefined)}
          />
        )}
      </FormSheet>
    </section>
  )
}

type RowActions = {
  onEdit: (repo: Repo) => void
  onDelete: (repo: Repo) => void
  onToggleActivity: (repo: Repo) => void
}

/** The phone projection. Hidden from `md:` up, where the table takes over. */
function RepoCards(props: { repos: readonly Repo[] } & RowActions) {
  return (
    <ul data-testid="repo-cards" className="flex flex-col gap-2 md:hidden">
      {props.repos.map((repo) => (
        <li
          key={repo.id}
          className="border-border bg-surface flex flex-col gap-2 rounded-md border p-3"
        >
          <p className="text-text font-medium">{repo.slug}</p>
          <p className="text-text-muted font-mono text-xs">
            {repo.forge} · {repo.default_branch}
          </p>
          <RowButtons repo={repo} {...props} />
        </li>
      ))}
    </ul>
  )
}

/** The desktop projection. Absent below `md:`, where the cards take over. */
function RepoTable(props: { repos: readonly Repo[] } & RowActions) {
  return (
    <table
      data-testid="repo-table"
      className="border-border hidden w-full border-collapse text-left text-sm md:table"
    >
      <thead>
        <tr className="text-text-muted">
          <th className="border-border border-b p-2 font-medium">Slug</th>
          <th className="border-border border-b p-2 font-medium">Forge</th>
          <th className="border-border border-b p-2 font-medium">Default branch</th>
          {/* A HEADER THE EYE DOES NOT NEED AND A SCREEN READER DOES. An empty
              `<th>` leaves the actions column unnamed, so the buttons in it are
              announced with no indication of what the column is. */}
          <th className="border-border border-b p-2 font-medium">
            <span className="sr-only">Actions</span>
          </th>
        </tr>
      </thead>
      <tbody>
        {props.repos.map((repo) => (
          <tr key={repo.id} className="text-text">
            <td className="border-border border-b p-2">{repo.slug}</td>
            <td className="border-border border-b p-2">{repo.forge}</td>
            <td className="border-border border-b p-2">{repo.default_branch}</td>
            <td className="border-border border-b p-2">
              <RowButtons repo={repo} {...props} />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

/**
 * The per-row controls, named with the repo in them.
 *
 * "Edit giiorg/caesar", not "Edit". The accessible name of a button repeated
 * once per row has to say which row it belongs to, and it is also what lets a
 * test reach one repo's Delete without counting siblings.
 */
function RowButtons(props: { repo: Repo } & RowActions) {
  return (
    <div className="flex gap-2">
      <button type="button" onClick={() => props.onEdit(props.repo)} className={ACTION_CLASS}>
        Edit {props.repo.slug}
      </button>
      <button type="button" onClick={() => props.onDelete(props.repo)} className={ACTION_CLASS}>
        Delete {props.repo.slug}
      </button>
      <button
        type="button"
        onClick={() => props.onToggleActivity(props.repo)}
        className={ACTION_CLASS}
      >
        Activity for {props.repo.slug}
      </button>
    </div>
  )
}

/**
 * The secondary-action button, spelled once.
 *
 * `py-2 text-sm` lands under the 44px floor on its own; `styles/app.css` states
 * that minimum once for `button`, so these are covered by the rule rather than
 * by a size utility repeated eleven times.
 */
const ACTION_CLASS =
  'border-border text-text self-start rounded-md border px-3 py-2 text-sm font-medium'
