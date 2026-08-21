import { QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it } from 'vitest'
import { queryClient } from '../api/query-client'
import type { GateEvidence } from '../gates/decide-gate'
import { expectDecisionRules } from '../gates/decision-rules.testkit'
import { server } from '../test/msw'
import { ApprovalSurface } from './approval-surface'

const b64 = (bytes: Uint8Array) => btoa(String.fromCharCode(...bytes))
const b64text = (s: string) => b64(new TextEncoder().encode(s))

/**
 * The SAME patch `decode-patch.test.ts` parses, byte for byte, and copied
 * rather than imported: a `.test.ts` file exports nothing, and re-exporting a
 * fixture from one would register that file's own `describe` blocks here
 * (vitest.config.ts:61 collects `.test.ts`, and a collected file that is also
 * imported runs twice). What the copy buys is that this suite's `2 files`,
 * `+3` and `−2` are the numbers Task 10 already proved decodePatch computes
 * from these bytes, instead of a second patch dialect invented here.
 */
const TWO_FILES = `diff --git a/src/auth.ts b/src/auth.ts
index 111..222 100644
--- a/src/auth.ts
+++ b/src/auth.ts
@@ -1,4 +1,5 @@
 import { db } from './db'
-export function login() {}
+export function login(): void {}
+export function logout(): void {}
 // tail
diff --git a/README.md b/README.md
--- a/README.md
+++ b/README.md
@@ -1 +1 @@
-old
+new
`

const TWO_FILES_B64 = b64text(TWO_FILES)

/** A latin-1 `é` (0xE9) inside an otherwise ordinary patch — not valid UTF-8. */
const LATIN1_B64 = b64(
  new Uint8Array([
    ...new TextEncoder().encode('--- a/legacy.txt\n+++ b/legacy.txt\n@@ -1 +1 @@\n-caf'),
    0xe9,
    ...new TextEncoder().encode('\n+cafe\n'),
  ]),
)

/** A gate this caller may decide, examining a patch, with a verdict beside it. */
const decidable: GateEvidence = {
  examined_artifact_id: 'a1',
  examined_artifact_type: 'patch',
  summary_artifact_id: 's1',
  can_decide: true,
  cannot_decide_reason: '',
  wall_deadline: '2099-01-01T00:00:00Z',
}

/**
 * An artifact row as `GET /runs/{id}/artifacts/{artifact_id}` returns it.
 *
 * `payload` is deliberately `unknown` here and not `ApiArtifactPayloadResponse`.
 * The wire really carries `tests: null` (`payload_dto.go:31-47` — required by
 * the validator, nullable by design), while the generated type renders that
 * property `tests?: ApiTestOutcome`: optional and NON-nullable, because this
 * toolchain gives a `$ref` property no null branch. Typing the fixture to the
 * generated shape would make the one payload this surface must survive
 * unwritable, and `as any` would delete the very narrowing under test.
 */
const artifact = (id: string, type: string, payload: unknown) =>
  HttpResponse.json({
    id,
    run_id: 'r1',
    node_id: 'gate',
    type,
    attempt: 1,
    generation: 1,
    parent_ids: [],
    payload,
  })

const notFound = () =>
  HttpResponse.json(
    { title: 'Not Found', status: 404, detail: 'no such artifact', correlation_id: 'cid-404' },
    { status: 404, headers: { 'content-type': 'application/problem+json' } },
  )

/** What `s1` carries when a case does not say. An approve with nothing to report. */
const DEFAULT_VERDICT = { verdict: 'approve', findings: [], tests: null }

type Opts = {
  evidence?: GateEvidence
  /** The `s1` payload. Omitted means an approve with nothing to report. */
  verdict?: unknown
  /** The `a1` payload when the gate examines a patch. */
  patch?: string
  /** The `a1` payload when the gate examines a plan. */
  plan?: unknown
  /** A raw `a1` payload, for the malformed cases. */
  examined?: unknown
  executionUsedMs?: number
}

/**
 * The surface with a real query client and a real network.
 *
 * No router: this component navigates nowhere, and the route that hosts it is
 * `routes/_app/approvals.test.tsx`'s subject rather than this file's. The query
 * client IS real, because the two fetches and their independent failure are
 * half of what is asserted here.
 *
 * The handler answers by artifact id rather than by call order, so the two
 * queries can resolve in either order and a test that kills one of them says
 * which one it killed.
 */
function renderSurface(opts: Opts) {
  const evidence = opts.evidence ?? decidable
  queryClient.clear()
  server.use(
    // Keyed on the two LITERAL ids the fixture uses, so a case that points
    // evidence at some other id gets a real 404 out of the same handler —
    // which is how the independent-failure case below is written.
    http.get('/api/v1/runs/:id/artifacts/:artifactId', ({ params }) => {
      if (params.artifactId === 's1') {
        return artifact('s1', 'review_verdict', opts.verdict ?? DEFAULT_VERDICT)
      }
      if (params.artifactId !== 'a1') return notFound()
      if (opts.examined !== undefined) return artifact('a1', 'patch', opts.examined)
      if (opts.plan !== undefined) return artifact('a1', 'plan', opts.plan)
      if (opts.patch !== undefined) return artifact('a1', 'patch', { diff: opts.patch })
      return notFound()
    }),
  )
  render(
    <QueryClientProvider client={queryClient}>
      <ApprovalSurface
        runId="r1"
        nodeId="gate"
        evidence={evidence}
        executionUsedMs={opts.executionUsedMs ?? 0}
      />
    </QueryClientProvider>,
  )
}

describe('the approval surface', () => {
  it('leads with the agent summary', async () => {
    renderSurface({
      verdict: { verdict: 'approve', findings: [], tests: null },
      patch: TWO_FILES_B64,
    })
    const summary = await screen.findByTestId('agent-summary')
    // Summary-FIRST is the locked decision, so its position is asserted, not
    // merely its presence: a surface that renders the diff first and the
    // summary below it is a different screen (mockup B) that was not chosen.
    expect(summary.compareDocumentPosition(screen.getByTestId('hard-facts'))).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    )
  })

  it('renders NO summary block at all when the gate has no verdict', async () => {
    // validate.go:212 permits a gate to consume ZERO review verdicts, so this
    // is a legal workflow and a production state. An empty block pretending to
    // be a summary is the failure: a stub never fakes success.
    renderSurface({ evidence: { ...decidable, summary_artifact_id: null }, patch: TWO_FILES_B64 })
    await screen.findByTestId('hard-facts')
    expect(screen.queryByTestId('agent-summary')).toBeNull()
    // And the hard facts it CAN still compute are there.
    expect(screen.getByTestId('hard-facts')).toHaveTextContent('2 files')
  })

  it('computes the hard facts from the decoded patch', async () => {
    renderSurface({ patch: TWO_FILES_B64, executionUsedMs: 62_000 })
    const row = await screen.findByTestId('hard-facts')
    expect(row).toHaveTextContent('2 files')
    expect(row).toHaveTextContent('+3')
    expect(row).toHaveTextContent('−2')
    expect(row).toHaveTextContent(/1m 2s/)
  })

  it('says tests are UNKNOWN rather than showing a tick', async () => {
    renderSurface({
      verdict: { verdict: 'approve', findings: [], tests: null },
      patch: TWO_FILES_B64,
    })
    const row = await screen.findByTestId('hard-facts')
    expect(row).toHaveTextContent(/tests .*not reported/i)
    expect(row).not.toHaveTextContent('✓')
  })

  it('treats an ABSENT tests key exactly as it treats an explicit null', async () => {
    // The generated type says `tests?: ApiTestOutcome` — optional and
    // non-nullable — while the wire carries an explicit `tests: null`
    // (payload_dto.go:31-47). Both shapes really occur, and a narrowing that
    // only handles the one the compiler admits would read `undefined.status`
    // on the other. Same assertion as the case above, from the other side.
    renderSurface({ verdict: { verdict: 'approve', findings: [] }, patch: TWO_FILES_B64 })
    expect(await screen.findByTestId('hard-facts')).toHaveTextContent(/tests .*not reported/i)
  })

  it('shows a real test outcome when one is reported', async () => {
    renderSurface({
      verdict: {
        verdict: 'approve',
        findings: [],
        tests: { status: 'failed', passed: 9, failed: 2 },
      },
      patch: TWO_FILES_B64,
    })
    expect(await screen.findByTestId('hard-facts')).toHaveTextContent(/9 passed, 2 failed/)
  })

  it('never shows a cost', async () => {
    // §10.1: there is no cost, usage, token or price field in the API or the
    // schema. The locked mockup has one; restoring it needs usage accounting
    // that does not exist, and a fabricated number here would be worse than
    // its absence.
    renderSurface({ patch: TWO_FILES_B64 })
    await screen.findByTestId('hard-facts')
    expect(screen.queryByText(/\$/)).toBeNull()
  })

  it('is an accordion: files listed, diffs on demand', async () => {
    renderSurface({ patch: TWO_FILES_B64 })
    expect(await screen.findByRole('button', { name: /src\/auth\.ts/ })).toBeInTheDocument()
    expect(screen.queryByText(/export function logout/)).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: /src\/auth\.ts/ }))
    expect(await screen.findByText(/export function logout/)).toBeInTheDocument()
  })

  it('renders a PLAN gate as prose, not as a diff', async () => {
    // examined_artifact_type is on the wire precisely so this decision is made
    // before the payload is fetched. A plan rendered through the diff parser
    // is an empty file list and a person told there is nothing to review.
    renderSurface({
      evidence: { ...decidable, examined_artifact_type: 'plan' },
      plan: { summary: 'Adds revocation.', steps: [{ title: 'Add the table' }] },
    })
    expect(await screen.findByText('Add the table')).toBeInTheDocument()
    expect(screen.queryByTestId('file-accordion')).toBeNull()
  })

  it('says so when the examined artifact is missing rather than showing an empty diff', async () => {
    // A zero examined_artifact_id means the pinned definition binds an
    // artifact the run never produced — the state decideGate also refuses.
    renderSurface({ evidence: { ...decidable, examined_artifact_id: '' } })
    expect(await screen.findByText(/evidence.*not available/i)).toBeInTheDocument()
  })

  it('warns when the patch bytes were not valid UTF-8', async () => {
    renderSurface({ patch: LATIN1_B64 })
    expect(await screen.findByText(/not valid UTF-8|may not render exactly/i)).toBeInTheDocument()
  })

  it('says a patch payload carrying no diff is unreadable, not empty', async () => {
    // `decodePatch('')` answers with zero files and zero counts, so a surface
    // that fetched the payload and read `payload.diff` straight through would
    // render a complete-looking accordion with nothing in it — a person told
    // there is nothing to review. The server refuses to STAGE such a payload
    // (execute.go:413-419 rejects a missing, non-string or empty diff), which
    // is the same judgement made one layer down.
    renderSurface({ examined: { base_sha: 'abc' } })
    expect(await screen.findByText(/could not be read/i)).toBeInTheDocument()
    expect(screen.queryByTestId('file-accordion')).toBeNull()
  })

  it('says a malformed verdict is unreadable rather than rendering an empty summary', async () => {
    renderSurface({ verdict: { verdict: 42 }, patch: TWO_FILES_B64 })
    expect(await screen.findByText(/summary could not be read/i)).toBeInTheDocument()
    expect(screen.queryByTestId('agent-summary')).toBeNull()
  })

  it('keeps the diff when the verdict 404s, and the verdict when the diff 404s', async () => {
    // Two fetches, two failure paths. The half that arrived is still worth
    // showing: a person who can see the diff can still decide the gate, and a
    // surface that took itself down because the OTHER query failed would turn
    // one missing artifact into a screen nobody can act on.
    renderSurface({ evidence: { ...decidable, summary_artifact_id: 'gone' }, patch: TWO_FILES_B64 })
    const row = await screen.findByTestId('hard-facts')
    expect(row).toHaveTextContent('2 files')
    expect(screen.queryByTestId('agent-summary')).toBeNull()
    // A THIRD state, and not the "not reported" of the cases above: nobody
    // said anything about tests here because nobody could read what was said.
    // Collapsing the two would attribute a claim to an agent whose verdict
    // never arrived.
    expect(row).toHaveTextContent(/tests unknown/i)
    // `cid-404` is the correlation id of the 404 above: an error surface that
    // dropped it would leave a user with nothing to quote.
    expect(screen.getByText('cid-404')).toBeInTheDocument()
  })

  it('carries the decision rules of every host', async () => {
    // The SAME assertions Task 11 exports, run against this host. §7 asks for
    // them on all three surfaces because §5's all-or-nothing rule is what a
    // card is likeliest to get wrong, and a host-local reimplementation is how
    // one host quietly gets a weaker version.
    //
    // LAST in this file's `it` bodies by necessity, not by taste: the kit's
    // trailing `cleanup()` unmounts every container in RTL's registry, this
    // surface's included.
    await expectDecisionRules((evidence) => renderSurface({ evidence, patch: TWO_FILES_B64 }))
  })
})
