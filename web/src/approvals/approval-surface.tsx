import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { ApiError } from '../api/errors'
import { ApiErrorPanel } from '../errors/api-error-panel'
import type { GateEvidence } from '../gates/decide-gate'
import { GateControls } from '../gates/gate-controls'
import { type DiffFile, decodePatch, type ParsedPatch } from '../patch/decode-patch'
import {
  artifactQueryOptions,
  type PlanView,
  readPatchDiff,
  readPlan,
  readVerdict,
  type VerdictView,
} from './artifact-query'

/**
 * ONE COMPONENT, TWO HOSTS. This task mounts it full-screen under
 * `/approvals/$runId/$nodeId`; Task 16 mounts it inside the run-detail gate
 * phase. decisions §7 decides twice that a gate is decidable where its evidence
 * is shown, and building the screen once is what stops the two decisions
 * drifting apart — including the §5 rule about the controls, which
 * `decision-rules.testkit.ts` then re-runs against every host.
 *
 * The host supplies position and nothing else: this component takes the
 * evidence it is given rather than looking it up, because the two hosts find it
 * in different places (an inbox row and a run's phase).
 */
export function ApprovalSurface(props: {
  runId: string
  nodeId: string
  evidence: GateEvidence
  /** The run's `execution_used_ms`, for the duration cell. */
  executionUsedMs: number
}) {
  const { evidence } = props
  const summary = useQuery(artifactQueryOptions(props.runId, evidence.summary_artifact_id ?? ''))
  // Branched on the TYPE before the fetch, which is the reason
  // `examined_artifact_type` is on the wire at all. A gate examines exactly one
  // plan or patch (`workflows/validate.go:209`), so a third value is not
  // reachable through a validated definition — and asking for it anyway would
  // spend a request to learn what the evidence already said.
  const examinable = evidence.examined_artifact_type === 'patch' || isPlan(evidence)
  const examined = useQuery(
    artifactQueryOptions(props.runId, examinable ? evidence.examined_artifact_id : ''),
  )

  // BOTH QUERIES, ONE GATE, and it is about ORDER rather than about coupling.
  // The locked order is summary-then-facts-then-evidence, and a surface that
  // revealed each block as its own query landed would put the facts row on
  // screen first whenever the diff came back first. Failure stays independent:
  // `isLoading` is `isPending && isFetching` (query-core
  // `queryObserver.js:310`), so a query that has ERRORED is ready and a query
  // that is DISABLED — no artifact id, therefore pending forever and never
  // fetching — is ready too. Each block below then renders from its own
  // query's state, and a 404 on one leaves the other on screen.
  if (summary.isLoading || examined.isLoading) {
    return <p className="text-text-muted text-sm">Reading this gate's evidence…</p>
  }

  const verdict = readVerdict(summary.data)
  const patch = evidence.examined_artifact_type === 'patch' ? readPatchDiff(examined.data) : null
  // DECODED EXACTLY ONCE, here, and never re-encoded: a patch is arbitrary
  // bytes and the base64 is the contract, not an encoding choice.
  const parsed = patch === null ? null : decodePatch(patch)
  const plan = isPlan(evidence) ? readPlan(examined.data) : null

  return (
    <div className="flex flex-col gap-4">
      <SummaryBlock evidence={evidence} query={summary} verdict={verdict} />
      <HardFacts
        evidence={evidence}
        summaryFailed={evidence.summary_artifact_id !== null && verdict === null}
        verdict={verdict}
        parsed={parsed}
        executionUsedMs={props.executionUsedMs}
      />
      <ExaminedArtifact evidence={evidence} query={examined} parsed={parsed} plan={plan} />
      {/* The thumb zone the locked design asks for. GateControls supplies the
          controls and this supplies the position — the split its own doc
          comment states. `bg-ground` rather than a transparent bar: the diff
          scrolls underneath it. */}
      <div className="bg-ground border-border sticky bottom-0 border-t py-3">
        <GateControls runId={props.runId} nodeId={props.nodeId} evidence={evidence} />
      </div>
    </div>
  )
}

const isPlan = (evidence: GateEvidence) => evidence.examined_artifact_type === 'plan'

/** What a query that finished badly should say, correlation id included. */
function QueryFailure(props: { error: unknown; fallback: string }) {
  // ApiErrorPanel rethrows anything that is not ours, and `error` here is not
  // always an ApiError: openapi-fetch propagates fetch's own TypeError from a
  // request that never reached the server. Narrowing is what keeps a dropped
  // connection from becoming a render-time throw in whichever route is hosting
  // this surface.
  if (props.error instanceof ApiError) return <ApiErrorPanel error={props.error} />
  return <p className="text-text-muted text-sm">{props.fallback}</p>
}

/**
 * 1. THE AGENT SUMMARY, and the block that is absent rather than empty.
 *
 * `summary_artifact_id` is null on a gate that consumes no review verdict, and
 * that is a legal workflow rather than a degenerate one:
 * `workflows/validate.go:212` caps a gate at one verdict and permits zero. A
 * block rendered anyway — a heading over nothing — reads as an agent that had
 * nothing to say, which is a different and false statement.
 */
function SummaryBlock(props: {
  evidence: GateEvidence
  query: { isError: boolean; error: unknown }
  verdict: VerdictView | null
}) {
  if (props.evidence.summary_artifact_id === null) return null
  if (props.query.isError) {
    return (
      <QueryFailure error={props.query.error} fallback="The review summary could not be read." />
    )
  }
  if (props.verdict === null) {
    return <p className="text-text-muted text-sm">The review summary could not be read.</p>
  }
  return (
    <section data-testid="agent-summary" className="border-border bg-surface rounded-md border p-3">
      <p className="text-text font-medium">
        {VERDICT_SENTENCE[props.verdict.verdict] ?? props.verdict.verdict}
      </p>
      {props.verdict.findings.length > 0 && (
        <ul className="mt-2 flex flex-col gap-1">
          {props.verdict.findings.map((f, i) => (
            <li
              // A finding carries no id on the wire (`ApiReviewFinding` is
              // severity, message and an optional file/line), so nothing in it
              // is guaranteed unique. The index is the only key available, and
              // it is stable here: this list is one read of one immutable
              // artifact and is never reordered or filtered.
              // biome-ignore lint/suspicious/noArrayIndexKey: no identity on the wire
              key={i}
              className="text-text-muted text-sm"
            >
              <span className="text-text font-mono text-xs">{f.severity}</span> {f.message}
              {f.file !== undefined && f.file !== '' && (
                <span className="font-mono text-xs">
                  {' '}
                  {f.file}
                  {f.line !== undefined && f.line > 0 ? `:${f.line}` : ''}
                </span>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

/**
 * The agent's verdict as a sentence, keyed by `string` and not by a union: the
 * wire type is `verdict: string`, so a verdict added server-side has to degrade
 * to the raw token rather than render `undefined`. The vocabulary is
 * deliberately the one a human gate decision uses (`payload_dto.go:27-29`), so
 * a reader is not learning two.
 */
const VERDICT_SENTENCE: Record<string, string> = {
  approve: 'The review agent approved this change.',
  request_changes: 'The review agent requested changes.',
  reject: 'The review agent rejected this change.',
}

/**
 * 2. THE HARD FACTS — files · ±lines · tests · duration.
 *
 * COST IS ABSENT BECAUSE IT DOES NOT EXIST. decisions §7's locked Approval (A)
 * reads "files · ±lines · tests · cost", and the fourth of those has no field
 * behind it — MEASURED, and the measurement is one grep: `cost`, `usage`,
 * `token` and `price` appear as a property name nowhere in `api/schema.d.ts`.
 * The only number in this neighbourhood is `execution_used_ms`, which is time.
 * Restoring the cell needs usage accounting nothing in this system performs,
 * and a number invented here would be worse than its absence.
 */
function HardFacts(props: {
  evidence: GateEvidence
  /** A summary artifact was named and could not be read — not the same as none. */
  summaryFailed: boolean
  verdict: VerdictView | null
  parsed: ParsedPatch | null
  executionUsedMs: number
}) {
  const cells: string[] = []
  // The files cell belongs to a PATCH gate. A plan gate has no files and no ±
  // lines, and "0 files" would be a measurement rather than the absence of one.
  if (props.evidence.examined_artifact_type === 'patch') {
    cells.push(
      props.parsed === null
        ? 'files not available'
        : `${props.parsed.files.length} ${props.parsed.files.length === 1 ? 'file' : 'files'}`,
    )
    if (props.parsed !== null) cells.push(`+${props.parsed.added}`, `−${props.parsed.removed}`)
  }
  cells.push(testsCell(props))
  cells.push(formatDuration(props.executionUsedMs))

  return (
    <div data-testid="hard-facts" className="text-text-muted flex flex-wrap gap-2 text-sm">
      {cells.map((cell, i) => (
        // Same reasoning as the findings list: the cells are computed strings
        // with no identity of their own, and the row is never reordered.
        // biome-ignore lint/suspicious/noArrayIndexKey: computed row, fixed order
        <span key={i}>{i === 0 ? cell : `· ${cell}`}</span>
      ))}
    </div>
  )
}

/**
 * NO TICK WHERE THERE IS NO RESULT. `tests: null` means the harness reported
 * nothing, `not_run` means it reported that it ran none, and the two are
 * different sentences (`payload_dto.go:61-64`). A summary artifact that could
 * not be read is a third thing again — nobody said anything about tests
 * because nobody could read what was said.
 */
function testsCell(props: { summaryFailed: boolean; verdict: VerdictView | null }): string {
  if (props.summaryFailed) return 'tests unknown'
  const tests = props.verdict?.tests ?? null
  if (tests === null) return 'tests not reported'
  const counts: string[] = []
  if (tests.passed !== undefined) counts.push(`${tests.passed} passed`)
  if (tests.failed !== undefined) counts.push(`${tests.failed} failed`)
  return counts.length === 0
    ? `tests ${tests.status}`
    : `tests ${tests.status} — ${counts.join(', ')}`
}

/**
 * `execution_used_ms` as a human duration.
 *
 * Rounded to whole seconds because it is a budget reading and not a benchmark,
 * and truncated at the two largest units for the same reason: `2h 5m` is what a
 * person deciding a gate wants, and the trailing seconds are noise at that
 * scale.
 */
function formatDuration(ms: number): string {
  const total = Math.max(0, Math.round(ms / 1000))
  if (total < 60) return `${total}s`
  const minutes = Math.floor(total / 60)
  if (minutes < 60) return `${minutes}m ${total % 60}s`
  return `${Math.floor(minutes / 60)}h ${minutes % 60}m`
}

/** 3. THE EXAMINED ARTIFACT, branched on the type the evidence declared. */
function ExaminedArtifact(props: {
  evidence: GateEvidence
  query: { isError: boolean; error: unknown }
  parsed: ParsedPatch | null
  plan: PlanView | null
}) {
  const { evidence } = props
  // Empty id, or a type this screen has no renderer for. The first is the state
  // `decidability()` reports as `evidence_missing`: the pinned definition binds
  // an artifact the run never produced, and `runs/api/api.go:512-515` records
  // that decideGate answers 500 for it rather than a refusal — undecidable by
  // anybody. The second is unreachable through a validated definition
  // (`workflows/validate.go:209`), which is why both get the one honest
  // sentence rather than a second message nothing can produce.
  if (
    evidence.examined_artifact_id === '' ||
    (evidence.examined_artifact_type !== 'patch' && !isPlan(evidence))
  ) {
    return <p className="text-text-muted text-sm">The evidence for this gate is not available.</p>
  }
  if (props.query.isError) {
    return (
      <QueryFailure error={props.query.error} fallback="The examined artifact could not be read." />
    )
  }
  if (isPlan(evidence)) return <PlanProse plan={props.plan} />
  if (props.parsed === null) {
    return <p className="text-text-muted text-sm">The examined patch could not be read.</p>
  }
  return <FileAccordion parsed={props.parsed} />
}

function PlanProse(props: { plan: PlanView | null }) {
  if (props.plan === null) {
    return <p className="text-text-muted text-sm">The examined plan could not be read.</p>
  }
  return (
    <section className="flex flex-col gap-2">
      <p className="text-text">{props.plan.summary}</p>
      <ol className="flex list-decimal flex-col gap-1 pl-5">
        {props.plan.steps.map((step, i) => (
          // A plan step is a title and an optional detail (`ApiPlanStep`) and
          // nothing else — two steps may legitimately share a title, so the
          // index is the only unique key. Stable for the same reason as the
          // findings list above: one read of one immutable artifact.
          // biome-ignore lint/suspicious/noArrayIndexKey: no identity on the wire
          <li key={i}>
            <p className="text-text text-sm">{step.title}</p>
            {step.detail !== undefined && step.detail !== '' && (
              <p className="text-text-muted text-sm">{step.detail}</p>
            )}
          </li>
        ))}
      </ol>
    </section>
  )
}

/**
 * FILES LISTED, DIFFS ON DEMAND. A gate's patch is routinely thousands of
 * lines, and the locked design opens on the file list so the first thing on
 * screen is what changed rather than how.
 */
function FileAccordion(props: { parsed: ParsedPatch }) {
  const [open, setOpen] = useState<string | null>(null)
  return (
    <div className="flex flex-col gap-2">
      {props.parsed.lossy && (
        // decodePatch decodes twice to learn this rather than hunting for
        // U+FFFD in its own output, so this warning cannot be triggered by a
        // patch that legitimately contains a replacement character.
        <p className="text-warning text-sm">
          Some of these patch bytes were not valid UTF-8, so the text below may not render exactly
          as it was produced.
        </p>
      )}
      <div data-testid="file-accordion" className="border-border rounded-md border">
        {props.parsed.files.map((file, i) => {
          // `path` ALONE IS NOT A KEY. decodePatch opens a file on a bare
          // `--- ` header with no `diff --git` line before it, and such a file
          // keeps `path: ''` until a `+++ ` header names it — so a patch
          // carrying two of them yields two entries with the same empty path.
          // The index disambiguates, and the list is never reordered or
          // filtered: it is one decode of one immutable artifact.
          //
          // Naming it here also puts it out of `noArrayIndexKey`'s reach — the
          // rule matches the index syntactically inside `key=` — so the
          // paragraph above is the only thing guarding this, rather than a
          // suppression a reader can find. It is written where the binding is
          // for that reason.
          const id = `${i}:${file.path}`
          return (
            <FileRow
              key={id}
              file={file}
              open={open === id}
              onToggle={() => setOpen(open === id ? null : id)}
            />
          )
        })}
      </div>
    </div>
  )
}

function FileRow(props: { file: DiffFile; open: boolean; onToggle: () => void }) {
  const { file } = props
  return (
    <div className="border-border border-b last:border-b-0">
      <button
        type="button"
        onClick={props.onToggle}
        aria-expanded={props.open}
        className="text-text flex w-full items-center gap-2 px-3 py-2 text-left text-sm"
      >
        <span className="font-mono">{file.path}</span>
        <span className="text-success text-xs">+{file.added}</span>
        <span className="text-danger text-xs">−{file.removed}</span>
      </button>
      {props.open && (
        <div className="border-border border-t px-3 py-2">
          {file.binary && (
            <p className="text-text-muted text-sm">
              A binary file — there is no text diff to show.
            </p>
          )}
          {file.hunks.map((hunk) => (
            <div key={hunk.header} className="font-mono text-xs">
              <p className="text-text-muted">{hunk.header}</p>
              {hunk.lines.map((line, i) => (
                <p
                  // biome-ignore lint/suspicious/noArrayIndexKey: a hunk's lines have no identity and never reorder
                  key={i}
                  className={LINE_CLASS[line.kind]}
                >
                  {/* THE MARKER IS A SEPARATE SPAN AND NOT PART OF THE TEXT.
                      decodePatch strips the leading +/-/space so that a
                      renderer colouring by `kind` does not also print the
                      character it coloured for, and the line's text is
                      therefore exactly the file's. Rendering the marker beside
                      it — not aria-hidden — is what keeps colour from being
                      the only channel that says add or del, both for a reader
                      who does not distinguish the two colours and for one
                      hearing the line read out. */}
                  <span>{MARKER[line.kind]}</span>
                  <span>{line.text}</span>
                </p>
              ))}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

const LINE_CLASS = {
  add: 'text-success',
  del: 'text-danger',
  context: 'text-text-muted',
} as const

const MARKER = { add: '+', del: '−', context: ' ' } as const
