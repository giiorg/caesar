import type { ReactElement } from 'react'
import { type Artifact, readPatchDiff, readPlan, readVerdict } from '../approvals/artifact-query'
import { PlanProse, VerdictProse } from '../approvals/payload-prose'
import { decodePatch } from '../patch/decode-patch'
import { FileAccordion } from '../patch/file-accordion'

/**
 * ONE ARTIFACT'S PAYLOAD, RENDERED BY WHAT IT IS.
 *
 * A GENERIC JSON DUMP IS THE THING THIS EXISTS INSTEAD OF, and the difference
 * is not cosmetic. A `patch` payload is base64 — dumped, it is a wall of
 * characters with a diff hidden inside it; a `plan` is the thing a gate
 * approved, and its steps are the reason somebody opened this screen. So the
 * three contract-bearing kinds go through the SAME readers and the SAME
 * presentation the approval surface uses (`approvals/artifact-query.ts`,
 * `approvals/payload-prose.tsx`, `patch/file-accordion.tsx`): a plan that reads
 * one way under a gate and another way here is a plan nobody can check.
 *
 * IT TAKES THE ARTIFACT AND FETCHES NOTHING. The route holds the query; this
 * holds the rendering. That is what lets every case below be an ordinary
 * `render()` with no client and no server, and it is what will let Task 9 mount
 * this beside a row the run already has.
 */
export function ArtifactKindView(props: { artifact: Artifact }): ReactElement {
  const { artifact } = props
  if (!isKnownKind(artifact.type)) return <UnknownKind type={artifact.type} />
  return <KnownKindView kind={artifact.type} artifact={artifact} />
}

/**
 * The seven kinds `runs/compile.go:19-28` names, as a LOCAL union over a wire
 * type that is a bare string.
 *
 * The same trade `PublicationPanel` records: this list buys exactly one thing —
 * the switch below is exhaustive over it, so deleting an arm is a compile error
 * HERE. It cannot be a compile error for an eighth kind added SERVER-side; that
 * one crosses the wire as a string this file has never heard of, and
 * `isKnownKind` sends it to a view that says so rather than to a blank frame.
 */
const KINDS = [
  'patch',
  'plan',
  'review_verdict',
  'gate_note',
  'publication',
  'log_ref',
  'failure_snapshot',
] as const
type ArtifactKind = (typeof KINDS)[number]
const KNOWN: ReadonlySet<string> = new Set<string>(KINDS)
const isKnownKind = (type: string): type is ArtifactKind => KNOWN.has(type)

/**
 * The seven arms, with exhaustiveness carried by the RETURN TYPE rather than by
 * a default case: with `strict` on, a switch that stops covering its union
 * leaves an implicit path returning `undefined`, which `ReactElement` does not
 * include, so dropping an arm is TS2366 and needs no unreachable branch.
 */
function KnownKindView(props: { kind: ArtifactKind; artifact: Artifact }): ReactElement {
  const { artifact } = props
  switch (props.kind) {
    case 'patch':
      return <PatchView artifact={artifact} />
    case 'plan':
      // `readPlan` answers null for a payload with no string `summary` — the
      // contract's key (`api/payload_dto.go:14`) and not a guess made here.
      return <PlanProse plan={readPlan(artifact)} />
    case 'review_verdict':
      return <VerdictArtifactView artifact={artifact} />
    case 'gate_note':
      return <GateNoteView payload={artifact.payload} />
    case 'publication':
      return <PublicationArtifactView payload={artifact.payload} />
    case 'log_ref':
      return <LogRefView payload={artifact.payload} />
    case 'failure_snapshot':
      return <FailureSnapshotView payload={artifact.payload} />
  }
}

/**
 * A patch, decoded ONCE and never re-encoded — the base64 is the contract
 * (`patch/decode-patch.ts`), not an encoding choice.
 *
 * `readPatchDiff` rather than `payload.diff`, because `decodePatch('')` answers
 * with zero files: a view that read the key straight through would render a
 * complete-looking, empty accordion for the three payload shapes the server
 * itself refuses to stage (`runner/execute.go:413-419`).
 */
function PatchView(props: { artifact: Artifact }) {
  const diff = readPatchDiff(props.artifact)
  if (diff === null) return <p className="text-text-muted text-sm">This patch could not be read.</p>
  return <FileAccordion parsed={decodePatch(diff)} />
}

function VerdictArtifactView(props: { artifact: Artifact }) {
  const verdict = readVerdict(props.artifact)
  if (verdict === null) {
    return <p className="text-text-muted text-sm">This review verdict could not be read.</p>
  }
  return (
    <section className="border-border bg-surface rounded-md border p-3">
      <VerdictProse verdict={verdict} />
    </section>
  )
}

/**
 * The note a gate's request-changes decision seeded the next round with
 * (`runs/revisions.go:262-270`).
 *
 * The note is PROSE and the rest is fields, and the split is what the payload
 * is: `note` is the sentence a person wrote for another person to act on, and
 * `decision`, `acted_by`, `acted_at`, `gate_node_id`, `examined_artifact_id`
 * and `generation` are the record of when and against what. `whitespace-pre-wrap`
 * because the writer's line breaks are part of what they wrote.
 *
 * `decision` renders as its raw token rather than through a sentence map. It is
 * always `request_changes` today — nothing else seeds a revision — and a third
 * copy of the approve/request_changes/reject vocabulary is a third place for it
 * to drift.
 */
function GateNoteView(props: { payload: Record<string, unknown> | null }) {
  const note = props.payload?.note
  return (
    <section className="flex flex-col gap-3">
      {typeof note === 'string' && note !== '' && (
        <p className="text-text text-sm whitespace-pre-wrap">{note}</p>
      )}
      <PayloadFields payload={props.payload} omit={['note']} />
    </section>
  )
}

/**
 * What a publish node recorded (`runs/publish.go:1741-1750`).
 *
 * THE PR LINE HAS A NO-PR BRANCH BECAUSE `pr_url` IS ROUTINELY EMPTY.
 * `"pr_url": derefText(intent.PrUrl)` writes `""` for a nil pointer, and a
 * publication artifact exists from the moment the branch is pushed — before a
 * pull request does. An `<a href="">` sends the reader to the page they are on,
 * which is the one thing worse than saying there is nothing to open.
 *
 * The fields follow UNFILTERED, `pr_url` included. The link is what a reader
 * clicks; the raw URL is what they copy into a message, and the branch and the
 * three hashes are what they go looking for when there is no PR at all.
 */
function PublicationArtifactView(props: { payload: Record<string, unknown> | null }) {
  const url = props.payload?.pr_url
  const number = props.payload?.pr_number
  return (
    <section className="flex flex-col gap-3">
      {typeof url === 'string' && url !== '' ? (
        <p className="text-text text-sm">
          Published as{' '}
          <a className="text-accent underline" href={url}>
            {typeof number === 'number' ? `#${number}` : url}
          </a>
        </p>
      ) : (
        <p className="text-text-muted text-sm">
          No pull request is recorded on this publication — the branch may be pushed and unopened,
          or the publish may have halted.
        </p>
      )}
      <PayloadFields payload={props.payload} />
    </section>
  )
}

/**
 * WHERE THE LOGS ARE, NEVER WHAT THEY SAY.
 *
 * §10.4 defers serving an agent's log, so this artifact is a pointer and this
 * view fetches nothing: the payload names paths on the runner's disk
 * (`runner/collect.go:310-325`) and the egress meter's totals, and that is all
 * there is to show. The sentence is not decoration — a screen listing two log
 * paths and no explanation reads as a log viewer that failed to load, and a
 * reader would keep waiting for bytes that are never coming.
 */
function LogRefView(props: { payload: Record<string, unknown> | null }) {
  return (
    <section className="flex flex-col gap-3">
      <p className="text-text-muted text-sm">
        These streams live on the runner's disk and are not served — §10.4's deferral. What follows
        is the pointer to them, not their contents.
      </p>
      <PayloadFields payload={props.payload} />
    </section>
  )
}

/**
 * How an attempt died, and the last of what it said.
 *
 * `log_tail` IS CONTENT AND IS RENDERED AS SUCH (sofo's ruling, session 89),
 * against the plan's own preamble, which groups `failure_snapshot` with
 * `log_ref` as "not displayable content". It is the redacted tail of the agent
 * log — `buildFailureSnapshot` runs every string in the snapshot through
 * `redactAll` (`runner/snapshot.go:42-49`) for exactly this exposure — and on a
 * failed run it is the one field an operator opened this screen for.
 *
 * THREE STATES, NOT TWO, and each is a shape the runner really writes:
 *   absent — the withheld-credential variant, which is `containerMetadata`
 *            alone (`runner/execute.go:341-345`): no log was scanned, so none
 *            is claimed.
 *   empty  — `logTail` returns "" when the file cannot be read as an ordinary
 *            file or holds nothing (`snapshot.go:71-81`, `readTail` :85-105).
 *   text   — the tail itself.
 * An empty <pre> for either of the first two would read as an agent that ran
 * and said nothing, which is a claim this screen cannot support.
 */
function FailureSnapshotView(props: { payload: Record<string, unknown> | null }) {
  const tail = props.payload?.log_tail
  return (
    <section className="flex flex-col gap-3">
      <PayloadFields payload={props.payload} omit={['log_tail']} />
      {typeof tail !== 'string' ? (
        <p className="text-text-muted text-sm">
          This snapshot carries no agent output — only the container's own metadata was collected.
        </p>
      ) : tail === '' ? (
        <p className="text-text-muted text-sm">
          The agent's log was empty or could not be read when this snapshot was taken.
        </p>
      ) : (
        <div className="flex flex-col gap-1">
          <p className="text-text-muted text-sm">
            The last of what the agent said, redacted by the runner.
          </p>
          <pre
            data-testid="log-tail"
            className="border-border bg-surface text-text overflow-x-auto rounded-md border p-3 font-mono text-xs whitespace-pre-wrap"
          >
            {tail}
          </pre>
        </div>
      )}
    </section>
  )
}

function UnknownKind(props: { type: string }) {
  return (
    <p className="text-text-muted text-sm">
      This run recorded an artifact of kind{' '}
      <span className="text-text font-mono">{props.type}</span>, and this screen has no renderer for
      that kind.
    </p>
  )
}

/**
 * A payload's fields as a definition list — NESTED VALUES INCLUDED, which is the
 * whole reason this walks rather than listing `Object.entries` of scalars.
 *
 * MEASURED AGAINST THE REAL PAYLOADS, not against a fixture. A `log_ref` has
 * ZERO top-level scalars: everything it carries is one or two levels down
 * (`{streams: {agent: {path, bytes, truncated}, …}, egress: {…}}` —
 * `runner/collect.go:310-325`). A `failure_snapshot`'s `workspace` goes further
 * still and holds an ARRAY of `{path, bytes}` objects (`snapshot.go:156-162`).
 * A scalars-only list renders both of those as an empty screen while passing
 * against any flat fixture somebody invents for it — the fake-empty render
 * `artifact-query.ts`'s own doc comment forbids, and one that would be green in
 * every test and blank on every real artifact.
 *
 * The empty case says so rather than rendering an empty `<dl>`, for the same
 * reason.
 */
function PayloadFields(props: {
  payload: Record<string, unknown> | null
  omit?: readonly string[]
}) {
  const omit = props.omit ?? []
  const entries = Object.entries(props.payload ?? {}).filter(([key]) => !omit.includes(key))
  if (entries.length === 0) {
    return <p className="text-text-muted text-sm">No fields to list on this payload.</p>
  }
  return (
    <dl className="flex flex-col gap-1 text-sm">
      {entries.map(([key, value]) => (
        <div key={key} className="flex flex-wrap items-baseline gap-2">
          <dt className="text-text-muted font-mono text-xs">{key}</dt>
          <dd className="text-text min-w-0">
            <PayloadValue value={value} />
          </dd>
        </div>
      ))}
    </dl>
  )
}

/** One JSON value: a nested record, a list, or a scalar rendered as its text. */
function PayloadValue(props: { value: unknown }): ReactElement {
  const { value } = props
  if (Array.isArray(value)) {
    if (value.length === 0) return <span className="text-text-muted">none</span>
    return (
      <ul className="flex flex-col gap-1">
        {value.map((item, i) => (
          // A payload's list elements carry no identity of their own and this
          // list is one read of one immutable artifact — never reordered,
          // never filtered.
          // biome-ignore lint/suspicious/noArrayIndexKey: no identity in a raw payload
          <li key={i}>
            <PayloadValue value={item} />
          </li>
        ))}
      </ul>
    )
  }
  // `typeof null === 'object'` and an array is an object too; the array is
  // handled above and null falls through to the scalar line, where it prints as
  // `null` rather than vanishing — a key whose value is null is a statement.
  if (typeof value === 'object' && value !== null) {
    const record = value as Record<string, unknown>
    // `none`, not `PayloadFields`' empty sentence: that sentence is about the
    // whole payload and would be false said about one key inside it.
    if (Object.keys(record).length === 0) return <span className="text-text-muted">none</span>
    return <PayloadFields payload={record} />
  }
  return <span className="font-mono">{value === null ? 'null' : String(value)}</span>
}
