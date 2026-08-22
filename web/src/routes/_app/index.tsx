import { useQuery } from '@tanstack/react-query'
import { createFileRoute, Link } from '@tanstack/react-router'
import type { components } from '../../api/schema'
import { approvalsQueryOptions } from '../../approvals/approvals-query'
import { InboxEmpty } from '../../approvals/inbox-empty'
import { QueryFailure } from '../../errors/query-failure'
import { GateControls } from '../../gates/gate-controls'
import { useStreamStatus } from '../../realtime/stream-status'
import { RunCard, runCardTime } from '../../runs/run-card'
import { type RunSummary, runsQueryOptions } from '../../runs/runs-query'

type Approval = components['schemas']['ApiApprovalResponse']

/**
 * Home, STATE-FIRST (decisions §7, Home A): a status line, "Needs you" gate
 * cards with the decision inline, "Running" cards with live phase dots, then
 * recent activity — in that order, because the order is the design. A person
 * opening this on a phone should be able to answer "is anything blocked on me"
 * without scrolling and without tapping through to a second screen.
 *
 * The assistant-generated résumé and the persistent ask-bar are row 13 (§10.6).
 * Until then the status line is COMPUTED CLIENT-SIDE from counts already on
 * screen. A stub never fakes success, and a plausible one-line narrative is the
 * most convincing fake available on this screen.
 *
 * HOME MUST ALWAYS RENDER SOMETHING, WHICH IS WHY NOTHING HERE SUSPENDS OR
 * THROWS — and that is not a preference, it is a fix for a hang this screen
 * caused the moment it grew a loader. `/` is where `/login`'s guard sends every
 * authenticated visitor. With `ensureQueryData` + `useSuspenseQuery`, an
 * endpoint answering 401 while `/auth/me` stays healthy sends Home's loader
 * into `_app`'s error boundary, the boundary navigates to `/login`, the guard
 * proves the session and sends the visitor back to `/`, and the cycle never
 * ends. MEASURED: `approvals.test.tsx`'s "routes a 401 from THIS route's
 * loader" ran the worker out of memory — 44 seconds, then
 * `Ineffective mark-compacts near heap limit`.
 *
 * So each section reads its own query's state and says what it could not read,
 * with the correlation id. That is also the better screen: a failing inbox
 * endpoint has no business hiding the run list, and `_app.tsx` already reasons
 * this way about the nav badge.
 */
export const Route = createFileRoute('/_app/')({
  loader: ({ context }) =>
    // BOTH, IN PARALLEL, AND NEITHER MAY REJECT. Home's three sections come
    // from two queries and nothing else; a card that had to fetch per row is
    // the N+1 GET /runs exists to remove. `allSettled` rather than `all`
    // because what a loader buys here is a router-level HEAD START — the
    // fetches begin with the route rather than with its first render, and
    // `defaultPreload: 'intent'` can start them on hover. A rejection is not a
    // reason to take the screen down; the components below render it.
    Promise.allSettled([
      context.queryClient.ensureQueryData(approvalsQueryOptions),
      context.queryClient.ensureQueryData(runsQueryOptions()),
    ]),
  component: Home,
})

/**
 * The run statuses "Running" claims. NOT `status === 'running'`: a run parked
 * at a gate is `waiting` and a run whose first phase has not been claimed is
 * `pending` (`dto.go:153`), and both are live work a person is waiting on. The
 * remaining four — succeeded, failed, superseded, dead — are terminal and are
 * what "Recent" is.
 */
const LIVE = new Set(['pending', 'running', 'waiting'])

/** Recent is capped, and the cap is stated on screen whenever it drops a row. */
const RECENT_CAP = 10

function Home() {
  const approvalsQuery = useQuery(approvalsQueryOptions)
  const runsQuery = useQuery(runsQueryOptions())
  // Read from context, never by calling `useAttentionStream()` again: that hook
  // opens a connection and a second caller would open a second stream per tab.
  const { scopeEmpty } = useStreamStatus()

  const approvals = approvalsQuery.data
  const runs = runsQuery.data

  return (
    <div className="flex flex-col gap-6">
      <StatusLine
        running={runs === undefined ? null : runs.filter((r) => LIVE.has(r.status)).length}
        waiting={approvals?.length ?? null}
      />

      <section className="flex flex-col gap-3">
        <h2 className="text-text text-lg font-medium">Needs you</h2>
        {approvals === undefined ? (
          <Unread query={approvalsQuery} what="your inbox" />
        ) : approvals.length === 0 ? (
          <InboxEmpty scopeEmpty={scopeEmpty} />
        ) : (
          <ul className="flex flex-col gap-3">
            {approvals.map((a) => (
              // Keyed by the identity of the WAITING GATE, not by run: one run
              // can hold several gates, and a retry gives the same node a new
              // attempt.
              <li key={`${a.run_id}:${a.node_id}:${a.attempt}`}>
                <GateCard approval={a} />
              </li>
            ))}
          </ul>
        )}
      </section>

      {runs === undefined ? (
        <Unread query={runsQuery} what="the run list" />
      ) : runs.length === 0 ? (
        /* ONE SENTENCE, NOT TWO. With no runs at all, "nothing is running" and
           "nothing has finished" are the same fact said twice — and neither is
           what a new user needs to hear. The distinct pair this screen owes the
           constitution is this sentence against the inbox's: one is a statement
           about runs, the other about what is waiting on you, and they answer
           different questions. */
        <p className="text-text-muted text-sm">No runs yet.</p>
      ) : (
        <RunSections runs={runs} />
      )}
    </div>
  )
}

/**
 * The two run sections, split out so that "the list arrived" is stated ONCE, by
 * the caller. Computing `live` and `recent` beside the query would leave them
 * `T[] | undefined` in a branch that has already established the list is there,
 * and every use would need a `?? []` that no input can reach — defensive code
 * with no failing case, which is indistinguishable from a real guard to the
 * next reader.
 */
function RunSections(props: { runs: RunSummary[] }) {
  const live = props.runs.filter((r) => LIVE.has(r.status))
  // NEWEST BY THE TIMESTAMP THE CARD ITSELF SHOWS. The server orders the page
  // by `created_at` — the cursor's own sort key (`runlist.go:85-93`), i.e. when
  // a run was REQUESTED — so a run started yesterday and finished a minute ago
  // would sit at the bottom of a section captioned "Recent". Sorting by
  // `runCardTime` makes the order one a reader can check against the dates on
  // the cards. It reorders ONE PAGE and claims nothing about the runs after it,
  // which is why the cap below says how many of how many it is showing.
  const done = props.runs
    .filter((r) => !LIVE.has(r.status))
    .sort((a, b) => runCardTime(b).localeCompare(runCardTime(a)))
  const recent = done.slice(0, RECENT_CAP)

  return (
    <>
      <section className="flex flex-col gap-3">
        <h2 className="text-text text-lg font-medium">Running</h2>
        {live.length === 0 ? (
          <p className="text-text-muted text-sm">Nothing is running.</p>
        ) : (
          <RunCards runs={live} testid="running-runs" />
        )}
      </section>

      <section className="flex flex-col gap-3">
        <h2 className="text-text text-lg font-medium">Recent</h2>
        {recent.length === 0 ? (
          <p className="text-text-muted text-sm">Nothing has finished yet.</p>
        ) : (
          <RunCards runs={recent} testid="recent-runs" />
        )}
        {/* `log()` nothing and hide nothing: a list that quietly stops at ten
            reads as "this is everything". The caption appears only when the cap
            actually dropped a row, so it is never a claim about a truncation
            that did not happen. */}
        {done.length > recent.length && (
          <p className="text-text-muted text-sm">
            Showing the {RECENT_CAP} most recent of {done.length}.
          </p>
        )}
      </section>
    </>
  )
}

/**
 * A section whose query has no data yet — still in flight, or finished badly.
 *
 * The two must be different sentences and neither may be an empty block:
 * "nothing is waiting on you" rendered over a failed request is the stub
 * faking success, and it is the most convincing version of that mistake
 * because it is what the screen says on the happy path.
 */
function Unread(props: { query: { isError: boolean; error: unknown }; what: string }) {
  if (props.query.isError) {
    return <QueryFailure error={props.query.error} fallback={`${props.what} could not be read.`} />
  }
  return <p className="text-text-muted text-sm">Reading {props.what}…</p>
}

function RunCards(props: { runs: RunSummary[]; testid: string }) {
  return (
    <ul data-testid={props.testid} className="flex flex-col gap-2">
      {props.runs.map((run) => (
        <li key={run.id}>
          <RunCard run={run} />
        </li>
      ))}
    </ul>
  )
}

/**
 * The counts, and NOTHING THIS SCREEN CANNOT KNOW.
 *
 * Both numbers are read off the two lists already in hand — no third query, and
 * no sentence about what they mean. `null` is a count that could not be taken,
 * and it becomes a clause saying so rather than a zero: "0 waiting on you" over
 * an inbox that failed to load is the one lie on this line that a reader would
 * act on.
 *
 * The all-zero case says so in words rather than rendering "0 running · 0
 * waiting on you", which reads as a dashboard that failed to load — and it says
 * it in words that are neither empty state's, so a reader is never told the
 * same thing twice in two different voices.
 */
function StatusLine(props: { running: number | null; waiting: number | null }) {
  const text =
    props.running === null && props.waiting === null
      ? 'Neither the run list nor your inbox could be read.'
      : props.running === 0 && props.waiting === 0
        ? 'Nothing is running, and nothing needs a decision from you.'
        : `${props.running === null ? 'the run list could not be read' : `${props.running} running`} · ${
            props.waiting === null
              ? 'your inbox could not be read'
              : `${props.waiting} waiting on you`
          }`
  return (
    <p data-testid="status-line" className="text-text">
      {text}
    </p>
  )
}

/**
 * One waiting gate, with the decision ON THE CARD.
 *
 * §10.2 records a deviation this plan proposed and withdrew: navigating to a
 * screen is not examining what is on it either, `examined_artifact_id` is a
 * server-selected evidence binding rather than a record of human examination,
 * and no reachable design closes that gap. Whether the affordance gets used is
 * a taste question, and the wave-1 operator checkpoint is where it gets
 * answered. So: ship the locked affordance.
 *
 * THE TITLE IS THE LINK, NOT THE CARD — the one place this differs from
 * `ApprovalList` and from `RunCard`, both of which make the whole card a
 * target. `GateControls` renders three buttons, and a <button> inside an <a>
 * is invalid HTML and unoperable by keyboard. The link's destination is the run
 * with this gate ALREADY OPEN (`?phase=<node_id>`), which is locked §3.2: the
 * top of a timeline the reader then has to find the gate in is a scroll and a
 * second tap for the screen they were already looking at.
 */
function GateCard(props: { approval: Approval }) {
  const a = props.approval
  return (
    <article
      data-testid="gate-card"
      className="border-border bg-surface flex flex-col gap-3 rounded-md border p-3"
    >
      <div className="flex flex-col gap-1">
        <Link
          to="/runs/$runId"
          params={{ runId: a.run_id }}
          search={{ phase: a.node_id }}
          className="text-text font-medium"
        >
          {a.task_title}
        </Link>
        {/* WHAT IS BEING DECIDED, before the buttons that decide it. This is
            why `examined_artifact_type` rides on the inbox row and not only on
            the run: a card offering three decisions without saying whether the
            thing under them is a plan or a patch asks for a judgement the
            reader has no basis for. */}
        <p className="text-text-muted font-mono text-xs">
          {a.node_id} · {a.evidence.examined_artifact_type} · attempt {a.attempt} · waiting since{' '}
          <time dateTime={a.waiting_since}>{a.waiting_since}</time>
        </p>
      </div>
      {/* `evidence` straight off the row: GateControls decides for itself
          whether the three controls render at all, and re-deriving that here
          would be the host-local reimplementation §5 exists to prevent. */}
      <GateControls runId={a.run_id} nodeId={a.node_id} evidence={a.evidence} />
    </article>
  )
}
