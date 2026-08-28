import { Link } from '@tanstack/react-router'
import type { Project } from './projects-query'

/**
 * The projects, as cards, at every width.
 *
 * CARDS ALL THE WAY UP, and the asymmetry with the repo list on the detail
 * screen is deliberate rather than an oversight. The locked mobile-first
 * standard is "card-first lists with desktop table projections"
 * (`docs/brainstorm/2026-08-01-decisions.md:82`), and a table earns its keep
 * when a row has columns to line up and compare — which the repo list has
 * (slug, forge, branch) and this list has not. A project is a name and a
 * sentence; a two-column table of those is a list with rules drawn on it.
 *
 * NO REPO COUNT, and its absence is the point. There is no unscoped repo
 * collection in the API — the only listing endpoint is
 * `GET /projects/{id}/repos` — so a count on this screen costs one request per
 * card, and a count rendered before those land (or a dash standing in for one
 * that failed) is invented data on the screen whose whole job is to say what
 * really exists.
 *
 * THE WHOLE CARD IS THE LINK, matching `RunCard`, `ApprovalList` and the task
 * rows: on a phone, a target the size of a card is the difference between one
 * tap and an aimed one.
 *
 * TWO EMPTY STATES, AND THE ROLE IS WHAT TELLS THEM APART — not the stream's
 * `scopeEmpty`, which is what `InboxEmpty` and the Tasks screen use. For THIS
 * list the discriminator is exact rather than approximate:
 * `handleListProjects` gives an admin every project and everyone else exactly
 * the projects they hold a grant on (`assets/api/api.go`), and `POST /projects`
 * is admin-only. So an empty list from an admin means the deployment has no
 * projects, and an empty list from anyone else means nothing has been shared
 * with them — there is no third reading, and neither sentence can be shown to
 * the wrong person.
 *
 * "Create the first" WOULD BE A LIE TOLD TO A NON-ADMIN, which is why the two
 * sentences are not one. They would be sent to a form the API answers 403 to,
 * on the screen that was supposed to explain why they cannot see anything.
 */
export function ProjectCards(props: { projects: readonly Project[]; canCreate: boolean }) {
  if (props.projects.length === 0) {
    return (
      <p className="text-text-muted text-sm">
        {props.canCreate
          ? 'No projects yet — create the first.'
          : 'No projects are shared with you yet.'}
      </p>
    )
  }
  return (
    <ul data-testid="project-cards" className="flex flex-col gap-2">
      {props.projects.map((project) => (
        <li key={project.id}>
          <Link
            to="/repos/$projectId"
            params={{ projectId: project.id }}
            className="border-border bg-surface flex flex-col gap-1 rounded-md border p-3"
          >
            <p className="text-text font-medium">{project.name}</p>
            {/* A DESCRIPTION IS OPTIONAL SERVER-SIDE and the empty string is the
                common case — `CreateProject` takes it as a plain string with no
                minimum (`assets/projects.go`). Rendering the paragraph anyway
                would put an empty line under every name and make the cards two
                different heights for no reason a reader could name. */}
            {project.description !== '' && (
              <p className="text-text-muted text-sm">{project.description}</p>
            )}
          </Link>
        </li>
      ))}
    </ul>
  )
}
