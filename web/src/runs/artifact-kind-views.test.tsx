import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import type { Artifact } from '../approvals/artifact-query'
import { ArtifactKindView } from './artifact-kind-views'

/**
 * NO QueryClientProvider AND NO MSW IN THIS FILE, and that is the subject's
 * design rather than the test's convenience: `ArtifactKindView` takes the
 * artifact it renders. The route fetches; the view renders. A component that
 * looked its own payload up would put a request inside every case below and
 * would make the run-detail host (Task 9) fetch twice for one row.
 */
const AT = '2026-08-26T10:00:00Z'

const payloadOf = (type: string, payload: Record<string, unknown> | null): Artifact => ({
  id: 'a1',
  run_id: 'r1',
  node_id: 'impl',
  attempt: 1,
  generation: 1,
  type,
  parent_ids: [],
  created_at: AT,
  payload,
})

const b64text = (s: string) => btoa(String.fromCharCode(...new TextEncoder().encode(s)))

/**
 * The SAME patch `decode-patch.test.ts` parses, byte for byte, and copied for
 * the reason `approval-surface.test.tsx` already records: a `.test.ts` file
 * exports nothing, and re-exporting a fixture from one would register that
 * file's own `describe` blocks here.
 */
const TWO_FILES_B64 = b64text(`diff --git a/src/auth.ts b/src/auth.ts
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
`)

/**
 * A `log_ref` PAYLOAD AS THE RUNNER ACTUALLY WRITES IT
 * (`runner/collect.go:310-325` for `streams`, `:359-370` for `egress`).
 *
 * It has ZERO top-level scalars: every leaf is one or two objects down. A
 * renderer that listed only the payload's own scalar fields would render this —
 * every real log_ref artifact — as an empty screen, while passing happily
 * against a flat invented `{ path: '/var/log/x' }` fixture. That is the
 * fake-empty render `artifact-query.ts`'s own doc comment forbids, so the
 * fixture is the real shape and the assertions reach the nested leaves.
 */
const LOG_REF = {
  streams: {
    agent: { path: 'runs/r1/impl/1/agent.log', bytes: 4096, truncated: false },
    gateway: { path: 'runs/r1/impl/1/gateway.log', bytes: 128, truncated: true },
  },
  egress: {
    requests: 12,
    denied: 3,
    request_bytes: 900,
    response_bytes: 4096,
    connects: 2,
    connect_bytes: 64,
  },
}

/**
 * A `failure_snapshot` as `buildFailureSnapshot` writes it
 * (`runner/snapshot.go:42-49`): the container metadata, the redacted `log_tail`,
 * and the `workspace` listing whose `entries` is an ARRAY of objects — a third
 * shape again, below the nesting `log_ref` needs.
 */
const SNAPSHOT = {
  exit_code: 137,
  oom_killed: true,
  status: 'exited',
  error: '',
  started_at: '2026-08-26T09:58:00Z',
  finished_at: '2026-08-26T09:59:00Z',
  // MULTI-LINE ON PURPOSE. A log tail rendered as an ordinary metadata row
  // still contains these words, so a one-line fixture cannot tell the two
  // renderings apart — the assertion below reads the <pre> by testid and
  // compares the WHOLE text, which is what "as preformatted content" means.
  log_tail: 'panic: runtime error: index out of range [3]\n\tmain.run()\n\t\t/src/cmd/main.go:41',
  workspace: {
    files: 2,
    bytes: 4200,
    entries: [
      { path: 'cmd/main.go', bytes: 4000 },
      { path: 'go.mod', bytes: 200 },
    ],
    entries_capped: false,
    scan_capped: false,
  },
}

describe('ArtifactKindView', () => {
  it('renders a patch through the diff renderer, not as JSON', () => {
    render(<ArtifactKindView artifact={payloadOf('patch', { diff: TWO_FILES_B64 })} />)
    expect(screen.getByText('src/auth.ts')).toBeInTheDocument()
    expect(screen.getByText('README.md')).toBeInTheDocument()
    // The whole point of a per-kind contract rather than one generic dump: the
    // base64 blob and its key never reach the screen.
    expect(screen.queryByText(/"diff":/)).toBeNull()
    expect(screen.queryByText(TWO_FILES_B64)).toBeNull()
  })

  it('says a patch payload carrying no diff is unreadable, not empty', () => {
    // `decodePatch('')` answers with zero files, so a view that read
    // `payload.diff` straight through would render a complete-looking, empty
    // accordion. `readPatchDiff` is what makes the three unreadable shapes one
    // null, and reusing it is what keeps this screen's judgement the same as
    // the approval surface's.
    render(<ArtifactKindView artifact={payloadOf('patch', { base_sha: 'abc' })} />)
    expect(screen.getByText(/could not be read/i)).toBeInTheDocument()
    expect(screen.queryByTestId('file-accordion')).toBeNull()
  })

  it('renders a plan as its summary and typed steps', () => {
    render(
      <ArtifactKindView
        artifact={payloadOf('plan', {
          summary: 'March on Rome',
          steps: [{ title: 'Cross the Rubicon', detail: 'At dawn.' }],
        })}
      />,
    )
    expect(screen.getByText('March on Rome')).toBeInTheDocument()
    expect(screen.getByText('Cross the Rubicon')).toBeInTheDocument()
    expect(screen.getByText('At dawn.')).toBeInTheDocument()
  })

  it('says a plan whose payload has no `summary` is unreadable', () => {
    // THE CONTRACT KEY IS `summary` (`api/payload_dto.go:14`), and `readPlan`
    // returns null for anything else — including the `content` key the plan for
    // this task guessed at. Pinned here rather than left to a reviewer: a view
    // that hand-read the payload instead of reusing the reader would render
    // whichever key it happened to reach for, and nothing on this screen would
    // say the plan the gate examines is not the plan it showed.
    render(<ArtifactKindView artifact={payloadOf('plan', { content: 'March on Rome' })} />)
    expect(screen.getByText(/could not be read/i)).toBeInTheDocument()
    expect(screen.queryByText('March on Rome')).toBeNull()
  })

  it('renders a review verdict as its sentence and its findings', () => {
    render(
      <ArtifactKindView
        artifact={payloadOf('review_verdict', {
          verdict: 'request_changes',
          findings: [
            {
              severity: 'blocker',
              message: 'The lease is never released.',
              file: 'lease.go',
              line: 42,
            },
          ],
          tests: null,
        })}
      />,
    )
    expect(screen.getByText(/requested changes/i)).toBeInTheDocument()
    expect(screen.getByText(/The lease is never released\./)).toBeInTheDocument()
  })

  it('renders log_ref as metadata that reaches its NESTED leaves, and fetches no log bytes', () => {
    render(<ArtifactKindView artifact={payloadOf('log_ref', LOG_REF)} />)
    // Two levels down: payload.streams.agent.path.
    expect(screen.getByText('runs/r1/impl/1/agent.log')).toBeInTheDocument()
    expect(screen.getByText('runs/r1/impl/1/gateway.log')).toBeInTheDocument()
    // The second top-level object, so the assertion is not satisfied by a
    // renderer that walks only the first.
    expect(screen.getByText('denied')).toBeInTheDocument()
    // HONEST, NOT EMPTY. §10.4 defers serving the bytes; a screen that showed
    // the paths and said nothing would read as a log viewer that failed to load.
    expect(screen.getByText(/on the runner/i)).toBeInTheDocument()
  })

  it("renders a failure snapshot's log tail as content, beside its metadata", () => {
    // sofo's ruling, session 89: `log_tail` is the run's actual failure output,
    // already redacted by the runner for exactly this exposure
    // (`snapshot.go:46`). On a failed run it is the one field an operator wants,
    // and "metadata-only" — which the plan's preamble groups it under — hides it
    // behind nothing.
    render(<ArtifactKindView artifact={payloadOf('failure_snapshot', SNAPSHOT)} />)
    // `.textContent` and NOT `toHaveTextContent`: that matcher normalizes
    // whitespace, so it reports the tail's newlines and tabs as spaces and
    // passes against a rendering that collapsed them — MEASURED, it failed
    // showing the flattened string. Exact text is the assertion that a log tail
    // reaches the screen as the runner wrote it.
    expect(screen.getByTestId('log-tail').textContent).toBe(SNAPSHOT.log_tail)
    expect(screen.getByText('exit_code')).toBeInTheDocument()
    expect(screen.getByText('137')).toBeInTheDocument()
    // Inside `workspace.entries`, an array of objects.
    expect(screen.getByText('cmd/main.go')).toBeInTheDocument()
  })

  it('renders a withheld failure snapshot without inventing a log tail', () => {
    // The withheld-credential variant is metadata ONLY — `execute.go:341-345`
    // builds it from `containerMetadata` alone, with no `log_tail` and no
    // `workspace`. An empty <pre> here would read as an agent that said nothing.
    render(
      <ArtifactKindView
        artifact={payloadOf('failure_snapshot', {
          exit_code: 1,
          oom_killed: false,
          status: 'exited',
          error: 'credential withheld',
          started_at: '',
          finished_at: '',
        })}
      />,
    )
    expect(screen.getByText('credential withheld')).toBeInTheDocument()
    expect(screen.queryByTestId('log-tail')).toBeNull()
    expect(screen.getByText(/no agent output/i)).toBeInTheDocument()
  })

  it('renders a gate note with its note, decision and actor', () => {
    render(
      <ArtifactKindView
        artifact={payloadOf('gate_note', {
          note: 'Three sentences.',
          decision: 'request_changes',
          gate_node_id: 'gate',
          examined_artifact_id: 'a0',
          acted_by: 'u2',
          acted_at: AT,
          generation: 2,
        })}
      />,
    )
    expect(screen.getByText('Three sentences.')).toBeInTheDocument()
    expect(screen.getByText('request_changes')).toBeInTheDocument()
    expect(screen.getByText('u2')).toBeInTheDocument()
  })

  it('renders publication as the PR reference, linked out', () => {
    render(
      <ArtifactKindView
        artifact={payloadOf('publication', {
          branch: 'caesar/run-1',
          target_branch: 'main',
          base_sha: 'abc123',
          tree_hash: 'def456',
          head_sha: 'ghi789',
          pr_url: 'https://github.com/x/y/pull/2',
          pr_number: 2,
        })}
      />,
    )
    expect(screen.getByRole('link')).toHaveAttribute('href', 'https://github.com/x/y/pull/2')
  })

  it('renders a publication with no PR yet as a sentence, not a link to nowhere', () => {
    // `"pr_url": derefText(intent.PrUrl)` (`runs/publish.go:1743`) — a
    // publication artifact exists before a pull request does, and the empty
    // string is what that looks like on the wire. An <a href=""> takes the
    // reader to the page they are already on.
    render(
      <ArtifactKindView
        artifact={payloadOf('publication', {
          branch: 'caesar/run-1',
          target_branch: 'main',
          base_sha: 'abc123',
          tree_hash: 'def456',
          head_sha: '',
          pr_url: '',
        })}
      />,
    )
    expect(screen.queryByRole('link')).toBeNull()
    expect(screen.getByText(/no pull request/i)).toBeInTheDocument()
    // The branch is still named: what the run pushed is the thing an operator
    // goes looking for when there is no PR to open.
    expect(screen.getByText('caesar/run-1')).toBeInTheDocument()
  })

  it('names a kind it has no renderer for rather than faking success', () => {
    // `type` is a bare string on the wire, so an eighth kind added server-side
    // arrives here as a token this file has never heard of.
    render(<ArtifactKindView artifact={payloadOf('sbom', { components: 4 })} />)
    expect(screen.getByText(/sbom/)).toBeInTheDocument()
    expect(screen.getByText(/no renderer/i)).toBeInTheDocument()
  })

  it('says so when the payload is null rather than rendering a blank kind', () => {
    // `payload` is required AND nullable (`api/schema.d.ts:739-741`), so every
    // arm has to survive it. A view that rendered its frame and no fields would
    // tell a reader this artifact recorded nothing.
    render(<ArtifactKindView artifact={payloadOf('log_ref', null)} />)
    expect(screen.getByText(/no fields/i)).toBeInTheDocument()
  })
})
