import { useQuery } from '@tanstack/react-query'
import { asApiError, ErrorSurface } from '../errors/error-surface'
import { RunCard } from '../runs/run-card'
import { runsQueryOptions } from '../runs/runs-query'
import type { Repo } from './projects-query'

/**
 * How many runs "recent activity" is.
 *
 * `GET /runs` answers one page and this surface is a glance, not the runs list
 * — which exists, is linked from every card here, and is where somebody who
 * wants the rest goes. The cap is stated as a constant because the slice below
 * is the only thing standing between a repo with a thousand runs and a
 * project detail that renders all of them.
 */
const RECENT = 5

/**
 * The runs that have touched one repo — the locked page map's "Recent activity
 * per repo" (`01-app-skeleton.html`, the Repos area).
 *
 * IT WAS DEFERRED ON A MISSING FILTER, AND TASK 15 BUILT IT. 12b's design
 * departure 8 deferred this surface "on the same missing runs-list filter as
 * departure 7"; `GET /runs` now takes `repo_id`, so the deferral is spent
 * rather than still standing. (The plan cites §10.8 for this. §10.8 is the task
 * lifecycle — `draft`/`ready`/`in review` having no data source — and has
 * nothing to do with repo activity. The citation is corrected here rather than
 * copied, because a wrong provenance in a comment outlives the task that wrote
 * it.)
 *
 * `RunCard` AS IT ALREADY IS, which is what the plan asks for and also all the
 * contract allows. The card renders the phase dots and the run's own status,
 * both of which are on `RunSummaryResponse`. Task 6's status line is NOT
 * available here and could not be added: it needs `max_attempts` and `outcome`,
 * and `PhaseSummaryResponse` carries neither — it is `node_id`, `node_type`,
 * `status`, `attempt`, `generation`. That line lives on run DETAIL, where
 * `PhaseResponse` does carry the outcome.
 */
export function RepoActivity(props: { repo: Repo }) {
  const runs = useQuery(runsQueryOptions({ repoId: props.repo.id }))

  return (
    <section data-testid="repo-activity" className="flex flex-col gap-2">
      <h4 className="text-text-muted text-sm">Recent activity · {props.repo.slug}</h4>
      {runs.isError ? (
        <ErrorSurface failure={asApiError(runs.error)} />
      ) : runs.data === undefined ? (
        <p className="text-text-muted text-sm">Reading activity…</p>
      ) : runs.data.length === 0 ? (
        <p className="text-text-muted text-sm">No runs have touched this repo.</p>
      ) : (
        runs.data.slice(0, RECENT).map((run) => <RunCard key={run.id} run={run} />)
      )}
    </section>
  )
}
