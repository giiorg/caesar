import { useSuspenseQuery } from '@tanstack/react-query'
import { createFileRoute, Link } from '@tanstack/react-router'
import { artifactQueryOptions } from '../../approvals/artifact-query'
import { ArtifactKindView } from '../../runs/artifact-kind-views'

/**
 * One artifact, full-screen. A URL, not a disclosure.
 *
 * IT HAS TO FETCH, and that is a contract fact rather than a design choice: the
 * rows on `run.artifacts` are `ApiArtifactResponse` — eight fields and NO
 * `payload` (`api/schema.d.ts:745-756`) — so a run-detail row physically cannot
 * render what the artifact says. `GET /runs/{id}/artifacts/{artifact_id}` is
 * the only thing that serves it, and `artifactQueryOptions` is the same query
 * the approval surface reads through, so following a link from a gate whose
 * diff is already on screen spends no second request.
 *
 * THE FILENAME CARRIES A `_` AND THE ROUTE DOES NOT. Named
 * `runs.$runId.artifacts.$artifactId.tsx`, TanStack's flat routing makes this a
 * CHILD of `runs.$runId.tsx` — which renders no `<Outlet />`, so the URL would
 * silently serve the run-detail screen. MEASURED: with that filename a probe
 * component never appeared and the generated tree said
 * `parentRoute: typeof AppRunsRunIdRoute`. The `$runId_` suffix opts out of
 * that nesting; `fullPath` is still `/runs/$runId/artifacts/$artifactId`, which
 * is the string every `<Link to=…>` uses and the one the compiler checks.
 */
export const Route = createFileRoute('/_app/runs/$runId_/artifacts/$artifactId')({
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(artifactQueryOptions(params.runId, params.artifactId)),
  component: ArtifactView,
})

function ArtifactView() {
  const { runId, artifactId } = Route.useParams()
  const { data: artifact } = useSuspenseQuery(artifactQueryOptions(runId, artifactId))

  return (
    <section className="flex flex-col gap-4">
      {/* WHAT IT IS AND WHERE IT CAME FROM, above what it says. An artifact id
          is not a thing a person can situate: the same node produces a new
          artifact per attempt AND per revision generation (`dto.go:499-502`),
          so the pair is what says which of several near-identical patches this
          one is. */}
      <header data-testid="artifact-header">
        <h1 className="text-text font-medium">{artifact.type}</h1>
        <p className="text-text-muted flex flex-wrap gap-2 font-mono text-xs">
          <span>{artifact.node_id}</span>
          <span>· attempt {artifact.attempt}</span>
          <span>· generation {artifact.generation}</span>
          {/* Raw ISO in a <time>, the shape `RunCard` and `GateDecision`
              already use — the machine-readable value is where a reader, and a
              later formatter, expects it. */}
          <span>
            · <time dateTime={artifact.created_at}>{artifact.created_at}</time>
          </span>
          <span>· {artifact.id}</span>
        </p>
        {/* BACK TO THE PHASE, not to the top of the timeline. `?phase=` is run
            detail's URL state, so naming the artifact's own node returns the
            reader to the row they left with it open. */}
        <Link
          to="/runs/$runId"
          params={{ runId }}
          search={{ phase: artifact.node_id }}
          className="text-accent text-sm underline"
        >
          Back to the run
        </Link>
      </header>
      <ArtifactKindView artifact={artifact} />
    </section>
  )
}
