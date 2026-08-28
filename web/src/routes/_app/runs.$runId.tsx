import { useQuery, useSuspenseQuery } from '@tanstack/react-query'
import { createFileRoute, Link } from '@tanstack/react-router'
import { z } from 'zod'
import { ApiError } from '../../api/errors'
import { ApprovalSurface } from '../../approvals/approval-surface'
import { ApiErrorPanel } from '../../errors/api-error-panel'
import { asApiError, ErrorSurface } from '../../errors/error-surface'
import { GateDecision } from '../../gates/gate-decision'
import {
  newestPhasePerNode,
  type Phase,
  phaseForSelector,
  priorAttemptOf,
} from '../../runs/phase-spine'
import { phaseStatusLine } from '../../runs/phase-status'
import { PublicationPanel } from '../../runs/publication-panel'
import { useRetryRun } from '../../runs/retry-run'
import { type RunEvent, runEventsQueryOptions } from '../../runs/run-events-query'
import { type Run, runQueryOptions } from '../../runs/run-query'

/**
 * WHICH PHASE IS OPEN IS URL STATE. Home's gate cards link here with `?phase=`
 * already set (Task 13), so tapping a card body lands on the gate open rather
 * than at the top of a timeline somebody has to scroll — and the state a person
 * is looking at is the state they can send to somebody else. That is the
 * constitution's URL-as-state rule and the reason this is not `useState`.
 *
 * `safeParse` rather than `parse`, and the failing case is not exotic.
 * TanStack Router's default search parser runs `JSON.parse` over each value and
 * keeps the raw string when that throws, so `?phase=gate` arrives as a string
 * while `?phase=42` arrives as the NUMBER 42 — MEASURED, and pinned by
 * "survives a phase param that is not even a string" in this route's suite.
 * `parse` would throw on it and take the whole run screen down over a link
 * somebody mistyped.
 *
 * WHAT `safeParse` BUYS IS "DOES NOT THROW", AND NOTHING ELSE — corrected in
 * Task 13 after measuring it. Rejecting a value does NOT remove it: a route's
 * search is its ancestors' search with its own validated result merged over it,
 * and nothing between `/` and here declares `validateSearch`, so the ancestors
 * contribute the whole raw object. MEASURED on the sibling list route
 * (`runs.index.tsx`, which records the transcript): `validateSearch` returned
 * `{}` and both `loaderDeps` and `useSearch()` still received the number. So
 * `openPhase` below really can hold a non-string, and `phase?: string` is a
 * type the runtime is free to violate. It lands on the timeline anyway — every
 * comparison against a node id is false — which is the same place a param
 * naming a node this run does not have lands. A future reader adding a
 * `phase.startsWith(...)` or handing it to a template is the one this warns.
 *
 * `generation` AND `attempt` NARROW `phase` TO ONE ATTEMPT, and they arrive
 * together or not at all. `?phase=gate` opens the node's newest attempt,
 * because the timeline is one row per node — which is the right answer for a
 * rail row and the wrong one for a `gate.decided` event, whose whole content is
 * a decision made on ONE attempt. The pair is what the event carries, so it is
 * what the link carries; `phaseForSelector` is where the triple is resolved and
 * where the pair's meaning is argued.
 *
 * PLAIN `z.number()`, NOT `z.coerce.number()`, and the reason is the measured
 * parser behaviour recorded above — `JSON.parse` per value, so a numeric param
 * ARRIVES AS A NUMBER. `coerce` therefore buys nothing on the happy path and
 * loses information on the unhappy one. MEASURED, both spellings, against
 * `?phase=gate&generation=true`:
 * `z.coerce.number()` reads the boolean as 1 and silently targets a real
 * attempt, while `z.number()` rejects the object, `validateSearch` answers
 * `{}`, the raw boolean survives the ancestor merge and the read-site narrow
 * below sends it to the node's newest — which is where every other unusable
 * value on this route already lands.
 */
const searchSchema = z.object({
  phase: z.string().optional(),
  generation: z.number().int().positive().optional(),
  attempt: z.number().int().positive().optional(),
})

/**
 * One half of the attempt selector — a positive integer ordinal, or nothing.
 *
 * TWO CALLERS, ONE PREDICATE: the search params below, re-narrowed at the read
 * site, and the `gate.decided` payload the link is BUILT from, which is
 * `unknown` all the way down (`EventDestination`). A link written from a
 * payload the emitter changed shape on must not be written at all, and it is
 * the same question in both places — is this a usable ordinal?
 *
 * `searchSchema` is a claim the runtime is free to violate — rejecting a value
 * does not remove it, which that schema's own comment measures at length — so
 * `generation` really can hold a boolean, a string, or a fractional number by
 * the time it reaches `useSearch()`. It re-asserts what the schema asserts,
 * rather than merely `typeof === 'number'`: a `1.5` the schema rejected must
 * land where a `true` the schema rejected lands, and a looser narrow would
 * instead route it to the "not in this run's phase list" sentence, which reads
 * as a claim about the RUN rather than about the URL.
 */
function attemptOrdinal(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isInteger(value) && value > 0 ? value : undefined
}

export const Route = createFileRoute('/_app/runs/$runId')({
  validateSearch: (search: Record<string, unknown>) => {
    const parsed = searchSchema.safeParse(search)
    return parsed.success ? parsed.data : {}
  },
  loader: ({ context, params }) =>
    context.queryClient.ensureQueryData(runQueryOptions(params.runId)),
  component: RunDetail,
})

function RunDetail() {
  const { runId } = Route.useParams()
  const { phase: openPhase, generation, attempt } = Route.useSearch()
  const navigate = Route.useNavigate()
  const { data: run } = useSuspenseQuery(runQueryOptions(runId))
  const spine = newestPhasePerNode(run)

  // THE OPEN ROW IS RESOLVED ONCE, HERE, and handed down — not resolved again
  // inside whichever component happens to need it. The spine is rendered by two
  // hosts (see below) and a second resolution is how one of them comes to show
  // attempt 1's body under attempt 3's heading. `typeof === 'string'` because
  // `phase` is one more value the runtime can violate; a non-string names no
  // node, which is where a `?phase=42` already lands.
  const selected =
    typeof openPhase === 'string'
      ? phaseForSelector(run, openPhase, attemptOrdinal(generation), attemptOrdinal(attempt))
      : null

  const toggle = (nodeId: string) => {
    // The updater form returns the WHOLE next search object, so closing a phase
    // drops the key rather than setting it to undefined — which
    // `exactOptionalPropertyTypes` would refuse and which would leave a bare
    // `?phase=` in the address bar.
    void navigate({ search: (prev) => (prev.phase === nodeId ? {} : { phase: nodeId }) })
  }

  return (
    <section className="flex flex-col gap-4">
      <RunHeader run={run} />
      {/* THE TWO PRESENTATIONS OF ONE TIMELINE (decisions §7, run detail A):
          phone reads top to bottom with each phase expanding in place; wide
          viewports get the same spine as a left rail with the open phase's
          detail beside it. CSS chooses, exactly as `shell/nav.tsx` does — jsdom
          applies no media queries, so both are present in every component test
          and every test that could otherwise match across them is scoped by
          testid.

          WHAT IS DUPLICATED IS THE SUMMARY, NEVER THE BODY. The rail restates
          each phase's one-line summary (`PhaseSummary`, rendered by both hosts
          so the two cannot drift); the expanded body — which mounts
          `<ApprovalSurface>` — renders once, inside the pane. See the "once"
          assertion in this route's suite for why that is a hard constraint and
          not a preference. */}
      <div className="md:flex md:items-start md:gap-4">
        <PhaseRail run={run} spine={spine} openPhase={openPhase} />
        <div data-testid="phase-detail" className="min-w-0 md:flex-1">
          <PhaseTimeline
            run={run}
            spine={spine}
            openPhase={openPhase}
            selected={selected}
            onToggle={toggle}
          />
          {spine.length > 0 && !spine.some((p) => p.node_id === openPhase) && (
            /* `md:` ONLY: on a phone a closed timeline is the resting state
               and needs no caption, while on a wide viewport the pane beside
               the rail would otherwise be an unexplained empty column. The
               `spine.length` guard is not redundant with the `some`: with no
               phases at all `some` is false too, and `PhaseTimeline` is
               already saying that this run has materialized nothing — two
               messages for one condition, on the desktop surface only. */
            <p className="text-text-muted hidden pt-3 text-sm md:block">
              Pick a phase to see what it did.
            </p>
          )}
        </div>
      </div>
      <EventLog runId={runId} />
    </section>
  )
}

/**
 * THE RAIL IS THE SPINE RESTATED AS NAVIGATION, and the change of control is
 * deliberate. A phone row is a disclosure — it owns the thing it opens, so it
 * is a `<button>` with `aria-expanded` that toggles. A rail row selects what
 * fills a pane somewhere else on the screen: that is a destination, so it is a
 * `<Link>` with an href somebody can copy, open in a new tab, or land on from
 * Home's gate cards. It SETS `?phase=` rather than toggling it, because
 * clicking the row you are already reading must not empty the pane you are
 * reading it in.
 *
 * Every `to`/`search` here is checked by the compiler through Task 1's
 * `Register` augmentation, the same property `shell/nav.tsx` leans on.
 *
 * WHICH ROW READS AS CURRENT IS THE ROUTER'S ANSWER, NOT THIS FILE'S, and that
 * is measured rather than assumed. `<Link>` writes `aria-current="page"` on the
 * link it considers active, and `activeOptions.includeSearch` defaults TRUE —
 * so with every row pointing at the same route, the marked row is the one whose
 * `?phase=` matches the address bar, which is exactly the open phase. MEASURED
 * twice: an explicit `aria-current={open ? 'true' : undefined}` here rendered
 * `"page"` on the open row and nothing on the others — i.e. the router
 * overrode the prop — and deleting the prop changed neither. It is left out
 * because a prop with no effect on the DOM is a claim the code does not make;
 * "drives both presentations from the one ?phase= param" is what pins the
 * default, and it fails loudly if a router upgrade ever stops including search.
 */
function PhaseRail(props: { run: Run; spine: Phase[]; openPhase: string | undefined }) {
  if (props.spine.length === 0) return null
  return (
    <ol
      data-testid="phase-rail"
      aria-label="Phases"
      className="border-border hidden w-64 shrink-0 flex-col rounded-md border md:flex"
    >
      {props.spine.map((phase) => {
        const open = props.openPhase === phase.node_id
        return (
          <li key={phase.node_id} className="border-border border-b last:border-b-0">
            <Link
              to="/runs/$runId"
              params={{ runId: props.run.id }}
              search={{ phase: phase.node_id }}
              className={`flex flex-col gap-1 px-3 py-2 text-sm ${open ? 'bg-surface text-text' : 'text-text-muted'}`}
            >
              <PhaseSummary run={props.run} phase={phase} />
            </Link>
          </li>
        )
      })}
    </ol>
  )
}

/**
 * One phase in one line, rendered by BOTH hosts. Two copies of these three
 * fields would drift the first time one gains a field, and the surface that
 * lost it would be whichever one the author was not looking at.
 *
 * THE THIRD FIELD IS NO LONGER A COUNTER. `attempt N/M` lies on every row that
 * has ended — it promised retries under an exhausted node, under a permanently
 * failed one, and under a gate somebody had just rejected. `phaseStatusLine`
 * derives the line from the phase's status first and its outcome class second;
 * what it may say, and why, is that function's business and is argued there.
 *
 * IT TAKES `run` RATHER THAN A RESOLVED PRIOR ATTEMPT, which is the reason
 * `PhaseRail` now receives the run instead of a bare `runId`. The line needs
 * the node's previous attempt for one case (the wait reason behind
 * "resumable"), and a `priorAttempt` prop would put that lookup at BOTH call
 * sites — where one host could pass it and the other forget, and the rail and
 * the timeline would disagree about the same row. That is exactly the drift the
 * paragraph above exists to prevent, so the lookup lives here and neither host
 * decides anything.
 */
function PhaseSummary(props: { run: Run; phase: Phase }) {
  const { run, phase } = props
  return (
    <>
      <span className="font-mono">{phase.node_id}</span>
      <span className="text-text-muted font-mono text-xs">{phase.status}</span>
      <span className="text-text-muted font-mono text-xs">
        {phaseStatusLine(phase, priorAttemptOf(run, phase))}
      </span>
    </>
  )
}

/**
 * The sticky header: what this run is, and no controls at all.
 *
 * NO CANCEL AND NO RETRY, which is a decision about the API rather than about
 * the layout. Cancel does not exist — `runs/tasks.go:394` refuses a rerun of a
 * live run with "run is in flight; cancel arrives in Plan 09" — and whole-task
 * retry (rerunTask) is deferred with it, so a button here would either lie or
 * do something the reader did not ask for. decisions §7's locked mockup puts
 * both in this header; they arrive when the endpoint does.
 *
 * ATTEMPT COMES OFF THE NEWEST PHASE AND GENERATION OFF THE RUN, and neither
 * substitutes for the other. `attempt` is a PER-NODE execution ordinal — a
 * rematerialized node continues it rather than restarting
 * (`store/queries.sql:719-723`) — so the newest phase's attempt says which try
 * that one node is on and nothing at all about how many revision rounds the run
 * has been through. `run.generation` is that round (`dto.go:66-68`), bumped by
 * `openRevision` (`runs/revisions.go:30`), and it is the only counter on this
 * header that moves when a gate sends work back.
 */
function RunHeader(props: { run: Run }) {
  const { run } = props
  // The last of `run.phases` — the raw list, NOT the collapsed spine.
  // `store/queries.sql:226` orders by `created_at, node_id, attempt`, so the
  // last row is the phase row created most recently; the spine's last row is
  // instead the node that first appeared last, which a retry of an earlier
  // node does not move. The header wants the run's current attempt, so it
  // wants the newer row.
  const newest = (run.phases ?? []).at(-1)
  // UNCONDITIONAL, on a header that renders for every status. The mutation is
  // inert until `.mutate()`, so the cost of holding one on a succeeded run is
  // nothing — and the alternative, a child component mounted only on `failed`,
  // buys a rule-of-hooks guarantee this file does not need and costs the button
  // its error surface, which belongs under the meta line rather than inside the
  // title row.
  const retry = useRetryRun(run.task_id)
  return (
    <header
      data-testid="run-header"
      className="bg-ground border-border sticky top-0 z-10 border-b py-3"
    >
      <div className="flex items-start justify-between gap-3">
        <h1 className="text-text font-medium">{run.task_title}</h1>
        {/* RETRY, AND ONLY ON `failed` — sofo's checkpoint ruling. A rejected
            run ends `failed` too (`gates.go:241-247` fails the run in the same
            transaction that finishes the rejected gate), so this one comparison
            covers a rejection as well.

            The other terminal states are deliberately silent. `succeeded` has
            nothing to retry; `superseded` means a re-run already happened and
            offering a second one invites a third; `dead` is the reaper's
            verdict. Widening this is a design decision, not a bug fix — the
            suite's `NOT_FAILED` row is where it would have to be argued. */}
        {run.status === 'failed' && (
          <button
            type="button"
            onClick={() => retry.mutate()}
            disabled={retry.isPending}
            className="border-border text-text shrink-0 rounded-md border px-3 py-1 text-sm font-medium"
          >
            Retry
          </button>
        )}
      </div>
      <p className="text-text-muted flex flex-wrap gap-2 font-mono text-xs">
        <span>{run.workflow_name}</span>
        <span>· {run.repo_slug}</span>
        <span>· {run.status}</span>
        {newest !== undefined && <span>· attempt {newest.attempt}</span>}
        <span>· generation {run.generation}</span>
      </p>
      {/* IN THE STICKY HEADER, beside the button that produced it. A 409 here
          is a normal answer — somebody else's re-run of this task won the race
          (`runs/tasks.go:401`) — and the operator reading it is looking at the
          button, not at the bottom of a timeline they would have to scroll to.
          It is rendered only on a failure, so the header's height is unchanged
          on every screen that has not clicked. */}
      {retry.error !== null && (
        <div className="pt-2">
          <ErrorSurface failure={asApiError(retry.error)} />
        </div>
      )}
    </header>
  )
}

/**
 * THE SPINE. One `<li>` per NODE — its newest attempt — in materialization
 * order, each a disclosure that expands IN PLACE. The timeline is the screen,
 * not an index into other screens, which is what locked run detail (A) decides.
 *
 * ON A WIDE VIEWPORT THIS ELEMENT IS THE DETAIL PANE, and it is the same list
 * either way: `PhaseRail` shows the spine beside it and every closed row here
 * drops out, so what remains in the pane is the open phase's body. The rows do
 * not stop existing — `data-state` and the `phase-*` testids are what the
 * live-update tests read, at both widths.
 *
 * NO LIVE AGENT OUTPUT, anywhere in here. §10.4: no route serves an agent's
 * log. What the run records instead is a `log_ref` artifact
 * (`runs/compile.go:22`) pointing at bytes on the runner's disk, and serving
 * those is a safety decision that deserves its own design pass. A terminal box
 * that stayed empty would be the most convincing lie on this screen.
 *
 * THE OPEN ROW SHOWS `selected`, WHICH IS NOT ALWAYS THE SPINE ROW. The spine
 * is one row per node — its newest attempt — and that is what every closed row
 * and the whole rail mean. A URL naming an attempt — which is what a
 * `gate.decided` event links to — resolves to a
 * different row of the same node, and the open row then follows it: summary,
 * body, AND `data-state`. All three or none — a body from attempt 1 rendered
 * under attempt 3's `data-state` is the same lie in a quieter attribute, and
 * `data-state` is what the live-update tests read.
 */
function PhaseTimeline(props: {
  run: Run
  spine: Phase[]
  openPhase: string | undefined
  selected: Phase | null
  onToggle: (nodeId: string) => void
}) {
  if (props.spine.length === 0) {
    return (
      <p className="text-text-muted text-sm">
        This run has no phases yet — nothing of its graph has been materialized.
      </p>
    )
  }
  return (
    <ol
      data-testid="phase-timeline"
      className="border-border flex flex-col rounded-md border md:rounded-none md:border-0"
    >
      {props.spine.map((phase) => {
        const open = props.openPhase === phase.node_id
        // A CLOSED ROW IS ALWAYS THE SPINE ROW, and an open one is `selected`
        // when there is one. Null here means the URL named an attempt this run
        // does not carry: the row keeps its own summary — the reader still
        // needs to see what the node's newest attempt did — and the body says
        // what happened instead of quietly falling back to it.
        const shown = open && props.selected !== null ? props.selected : phase
        return (
          <li
            key={phase.node_id}
            data-testid={`phase-${phase.node_id}`}
            data-node-id={phase.node_id}
            data-state={shown.status}
            // A CLOSED ROW IS THE RAIL'S JOB ON A WIDE VIEWPORT, so it leaves
            // the pane rather than stacking N empty bordered strips above the
            // one body the pane exists to show. The row is still in the DOM,
            // still carries `data-state`, and is still what the live-update
            // tests read — `md:hidden` is a paint decision.
            className={`border-border border-b last:border-b-0 md:border-b-0 ${open ? '' : 'md:hidden'}`}
          >
            <button
              type="button"
              onClick={() => props.onToggle(phase.node_id)}
              aria-expanded={open}
              className="text-text flex w-full items-center gap-2 px-3 py-3 text-left text-sm md:hidden"
            >
              <PhaseSummary run={props.run} phase={shown} />
            </button>
            {open && (
              <div className="border-border border-t px-3 py-3 md:rounded-md md:border">
                {props.selected === null ? (
                  <p className="text-text-muted text-sm">
                    This attempt is not in the run's phase list.
                  </p>
                ) : (
                  <PhaseBody run={props.run} phase={props.selected} />
                )}
              </div>
            )}
          </li>
        )
      })}
    </ol>
  )
}

/**
 * What a phase expands into, by what the phase IS rather than by a node_type —
 * `ApiPhaseResponse` does not carry one.
 *
 * A PUBLISH NODE IS ONE THE RUN HAS A PUBLICATION FOR. `node_id` is the only
 * thing tying `run.publications` to `run.phases` on the wire, and it is a real
 * link rather than a guess: `runResponse` builds both lists from the same
 * RunDetail in one pass (`dto.go:453-491`).
 *
 * A GATE IS ONE CARRYING EVIDENCE OR A DECISION, and evidence is read first
 * because it is the half that can still be acted on. `attachGateEvidence`
 * builds it only for a phase whose status is `waiting_decision`
 * (`runs/tasks.go:717-719`), so a gate somebody has already decided arrives
 * here with a decision and no evidence.
 *
 * `phase.evidence` is narrowed TRUTHY and never `!== undefined`: it is
 * a nullable `$ref`, which this toolchain renders optional-and-non-nullable, so
 * the runtime value can be `null` where the type says it cannot, and
 * `exactOptionalPropertyTypes` (`tsconfig.json:14`) makes `!== undefined`
 * narrow to the object while the value is null.
 */
function PhaseBody(props: { run: Run; phase: Phase }) {
  const { run, phase } = props
  const publication = (run.publications ?? []).find((p) => p.node_id === phase.node_id)
  if (publication !== undefined) return <PublicationPanel publication={publication} />
  if (phase.evidence) {
    // THE SAME COMPONENT the deep approval route mounts, not a second gate
    // renderer. §7 decides twice that a gate is decidable where its evidence is
    // shown — here inline and there full-screen — and building it once is what
    // stops the two decisions drifting.
    return (
      <ApprovalSurface
        runId={run.id}
        nodeId={phase.node_id}
        evidence={phase.evidence}
        phases={run.phases ?? []}
        executionUsedMs={run.execution_used_ms}
      />
    )
  }
  if (phase.decision) return <GateDecision decision={phase.decision} />
  return <PhaseArtifacts run={run} phase={phase} />
}

/**
 * A phase's artifacts, by type and id.
 *
 * SCOPED TO THIS ATTEMPT, not to the node. An artifact row carries its own
 * `attempt` and `generation` (`dto.go:499-502`), and a retried or revised node
 * keeps the rows its superseded attempts wrote — listing those beside the
 * current ones would show a reader a plan the run has already thrown away.
 *
 * THE LINK IS PER ROW, AND THE PHASE CARD IS NOT THE LINK. A single attempt
 * routinely writes several artifacts, and not as an edge case: the success path
 * appends a `log_ref` beside the patch on EVERY attempt
 * (`runner/collect.go:142,159`), and a failed one emits a `failure_snapshot`
 * beside its own log_ref (`runner/execute.go:346-350`). So a destination hung
 * off the phase could only ever be one of them, chosen here rather than by the
 * reader. Each row therefore carries its own
 * `/runs/$runId/artifacts/$artifactId`, which
 * `routes/_app/runs.$runId_.artifacts.$artifactId.tsx` renders through the
 * per-kind views.
 *
 * THE ROW STILL SHOWS THE ID, and not only because it is now an href: the id is
 * what a person quotes when asking somebody else about an artifact, and it is
 * the only thing distinguishing two rows of the same type.
 */
function PhaseArtifacts(props: { run: Run; phase: Phase }) {
  const { phase } = props
  const rows = (props.run.artifacts ?? []).filter(
    (a) =>
      a.node_id === phase.node_id &&
      a.attempt === phase.attempt &&
      a.generation === phase.generation,
  )
  if (rows.length === 0) {
    return <p className="text-text-muted text-sm">This attempt produced no artifacts.</p>
  }
  return (
    <ul className="flex flex-col gap-1">
      {rows.map((a) => (
        <li key={a.id}>
          <Link
            to="/runs/$runId/artifacts/$artifactId"
            params={{ runId: props.run.id, artifactId: a.id }}
            className="flex gap-2"
          >
            <span className="text-accent font-mono text-sm underline">{a.type}</span>
            <span className="text-text-muted font-mono text-xs">{a.id}</span>
          </Link>
        </li>
      ))}
    </ul>
  )
}

/**
 * THE EVENT LOG IS FLAT AND CHRONOLOGICAL, and that is a contract fact rather
 * than a layout preference. `ApiRunEventResponse` is `{id, kind, payload,
 * occurred_at}` with NO node_id column — the node lives inside the untyped
 * payload — so grouping events under their phase would depend on a field
 * `contracts:check` cannot police, and would silently mis-place every event
 * whose payload shape changed.
 *
 * A plain `useQuery`, not the route's `useSuspenseQuery`: the log is the least
 * important thing on this screen and a failure reading it must not take the
 * timeline down with it. The two empty states below are distinct for the same
 * reason the inbox's are — "no events" is a statement about this run, and a
 * failed read is a statement about the server.
 */
function EventLog(props: { runId: string }) {
  const events = useQuery(runEventsQueryOptions(props.runId))
  if (events.isLoading) return <p className="text-text-muted text-sm">Reading this run's log…</p>
  if (events.isError) {
    // ApiErrorPanel rethrows anything that is not ours, and this error is not
    // always an ApiError: openapi-fetch propagates fetch's own TypeError from a
    // request that never reached the server.
    if (events.error instanceof ApiError) return <ApiErrorPanel error={events.error} />
    return <p className="text-text-muted text-sm">This run's log could not be read.</p>
  }
  const rows = events.data ?? []
  if (rows.length === 0) {
    return <p className="text-text-muted text-sm">No events have been recorded for this run.</p>
  }
  return (
    <ul data-testid="event-log" className="flex flex-col gap-1">
      {rows.map((e) => (
        <EventRow key={e.id} runId={props.runId} event={e} />
      ))}
    </ul>
  )
}

function EventRow(props: { runId: string; event: RunEvent }) {
  return (
    <li className="text-text-muted flex gap-2 font-mono text-xs">
      <time dateTime={props.event.occurred_at}>{props.event.occurred_at}</time>
      <span className="text-text">{props.event.kind}</span>
      <EventDestination runId={props.runId} event={props.event} />
    </li>
  )
}

/**
 * THE EVENT KINDS THAT NAME SOMEWHERE TO GO, and nothing else in the log.
 *
 * §2.4 names three link classes on this screen; the third — a phase's artifacts
 * — belongs to `PhaseArtifacts`, per row, because an attempt writes several.
 * The event log's share is a decision on one gate attempt and a pull request on
 * a forge: both are rows recording a thing that exists elsewhere. Every other
 * kind is a lifecycle transition whose whole content is already the sentence in
 * the row.
 *
 * A PULL REQUEST ARRIVES UNDER TWO KINDS, AND `publish.adopted` IS NOT ALWAYS
 * ONE OF THEM. `openPublication` builds ONE event, fills ONE payload —
 * `node_id`, `branch`, `pr_number`, `pr_url`, `state`, `adopted` — and then
 * chooses its kind: `EventPublishAdopted` when the branch already had a pull
 * request open and this run took it over, `EventPublishPROpened` when the run
 * opened one (`runs/publish.go:993-1017`). Same `pr_url` either way, so linking
 * only `pr_opened` would leave a reader looking at a row that names a PR nobody
 * can reach — indistinguishable from a bug.
 *
 * BUT `publish.adopted` HAS A SECOND EMITTER, and it names no pull request at
 * all: `pushPublication` writes the same kind when the push found the branch
 * already at the wanted head and adopted it instead of pushing again
 * (`runs/publish.go:807-810`) — the crash-after-push recovery. Its payload is
 * `node_id`, `branch`, `head_sha`, `tree_hash`, `created`, with no `pr_url` and
 * no `pr_number`, and the emitter says so itself: "`branch` is what tells the
 * two `publish.adopted` meanings apart" (`runs/publish.go:832-834`).
 *
 * SO THE KIND SAYS A ROW MAY NAME A PULL REQUEST AND THE PAYLOAD SAYS WHETHER
 * IT DOES — which is why the scheme guard below, not the kind test, is what
 * decides that a link appears. A branch-adoption row falls out of it as the
 * plain line it should be. Spec §2.4 named only `pr_opened` because nobody had
 * read the emitters; it was amended to name both kinds, and amended again when
 * the second `adopted` emitter turned up and made the first amendment's reason
 * false.
 *
 * EVERY FIELD IS READ AS `unknown`, and that is the payload's actual type
 * (`api/schema.d.ts:983-985`) rather than caution. There is no typed event
 * payload anywhere in `web/`: the control plane builds each one as a
 * `map[string]any` at the emitter, `contracts:check` cannot police the keys, and
 * a cast here would turn a payload shape change into a blank row or a crash
 * instead of a row with no link on it.
 *
 * THE DECIDED GATE IS THE REASON THIS EXISTS. Two `gate.decided` rows on one
 * run are the ordinary case — a request_changes and then an approve, one
 * revision round apart — and until now they were two identical lines of text
 * pointing nowhere. `?phase=` alone would have sent both to the SAME attempt,
 * the newest, so the row recording somebody's request for changes would have
 * opened somebody else's approval. Hence the whole triple, and hence the
 * attempt in the link's own text: it is what separates the two rows for a
 * reader and for a screen reader.
 */
function EventDestination(props: { runId: string; event: RunEvent }) {
  const payload = props.event.payload
  if (payload === null) return null
  if (props.event.kind === 'gate.decided') {
    const node = payload.node_id
    const generation = attemptOrdinal(payload.generation)
    const attempt = attemptOrdinal(payload.attempt)
    if (typeof node !== 'string' || generation === undefined || attempt === undefined) return null
    return (
      <Link
        to="/runs/$runId"
        params={{ runId: props.runId }}
        search={{ phase: node, generation, attempt }}
        className="text-accent underline"
      >
        {node} attempt {attempt}
      </Link>
    )
  }
  if (props.event.kind === 'publish.pr_opened' || props.event.kind === 'publish.adopted') {
    const url = payload.pr_url
    const number = payload.pr_number
    // A SCHEME CHECK, NOT A STRING CHECK, because this href is built from a
    // value the control plane copied out of a forge's JSON
    // (`runs/publish.go:1016` stores `pr.URL` verbatim) and never parsed.
    //
    // NOT BECAUSE REACT WOULD OTHERWISE RUN IT — MEASURED on 19.2.8: React
    // REPLACES a `javascript:` href with `javascript:throw new Error('React
    // has blocked a javascript: URL as a security precaution.')`, so that one
    // scheme is already dead. What React does not do is anything about the
    // ordinary case: an empty, relative, or `data:` `pr_url` renders a link
    // that goes nowhere or back to the page the reader is on, which is the
    // failure `publication-panel.tsx`'s `prLink` and the publication artifact
    // view each guard separately. Leaning on a framework's block list for one
    // scheme is not a policy; naming the two schemes a pull request can have
    // is. An unusable URL leaves the row exactly as it was before this
    // function existed — a log line with no link.
    if (typeof url !== 'string' || !/^https?:\/\//.test(url)) return null
    return (
      <a className="text-accent underline" href={url}>
        {typeof number === 'number' ? `#${number}` : url}
      </a>
    )
  }
  return null
}
