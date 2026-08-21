import { queryOptions } from '@tanstack/react-query'
import { api, unwrap } from '../api/client'
import type { components } from '../api/schema'

export type Artifact = components['schemas']['ApiArtifactPayloadResponse']
type PlanStep = components['schemas']['ApiPlanStep']
type ReviewFinding = components['schemas']['ApiReviewFinding']
type TestOutcome = components['schemas']['ApiTestOutcome']

/**
 * One artifact's payload, keyed by ['artifact', runId, artifactId].
 *
 * ARTIFACTS ARE IMMUTABLE, so this is the one query in 12a that does NOT take
 * AUTH_SCOPED_QUERY_OPTIONS' refresh interval: an artifact row is written once
 * and never updated, and re-polling a patch every sixty seconds would be
 * bandwidth spent to learn it is unchanged. A revision produces a NEW artifact
 * with a new id, which is a new cache entry.
 *
 * The AUTHORIZATION on it can still narrow, and that is covered where it
 * matters: the list and the run both carry the refresh floor, so a gate a user
 * may no longer see leaves the screen within the stated interval.
 *
 * `enabled` is what a caller with no artifact to fetch gets instead of a
 * request for the empty id — a gate whose `examined_artifact_id` is `''`
 * examines an artifact the run never produced, and `summary_artifact_id` is
 * legitimately null on a gate that consumes no verdict at all
 * (`workflows/validate.go:212` caps verdicts at one and permits zero). A
 * disabled query never fetches, so its `status` stays 'pending' forever while
 * its `fetchStatus` is 'idle'; `isLoading` is the pair of them together and is
 * the flag a caller should branch on.
 */
export function artifactQueryOptions(runId: string, artifactId: string) {
  return queryOptions({
    queryKey: ['artifact', runId, artifactId],
    queryFn: async () =>
      unwrap(
        await api.GET('/api/v1/runs/{id}/artifacts/{artifact_id}', {
          params: { path: { id: runId, artifact_id: artifactId } },
        }),
      ),
    staleTime: Number.POSITIVE_INFINITY,
    enabled: artifactId !== '',
  })
}

/**
 * THE READERS BELOW EXIST BECAUSE `payload` IS UNTYPED ON PURPOSE.
 *
 * One endpoint serves plan, review_verdict, patch and log_ref alike
 * (`contracts/contracts.go:178`), so its payload is reflected as a bare map and
 * reaches this client as `{ [key: string]: unknown } | null`. The published
 * component schemas — ApiPlanPayload, ApiReviewVerdictPayload — describe what a
 * payload of a given TYPE contains, and nothing in the generated types connects
 * the two. A cast would assert that connection without checking it, on data an
 * executor produced and a validator judged at a different schema version.
 *
 * So each reader answers a payload with a value or with `null`, and `null` is
 * the surface's cue to say the artifact could not be read. That is the whole
 * point: `decodePatch('')` answers with zero files and zero counts, and a
 * reader that returned a default instead of null would render a complete-looking
 * screen with nothing on it — the fake-empty render a stub must never produce.
 */
function payloadOf(artifact: Artifact | undefined): Record<string, unknown> | null {
  // `payload` is typed `| null` and today's service normalizes it to an object
  // before it leaves the boundary, so the null arm is the contract's, not the
  // server's. Reading it as though the type were a promise is how a screen dies
  // on the first payload written by something other than today's writer.
  return artifact?.payload ?? null
}

/**
 * A patch's base64 diff, or null when there is not one.
 *
 * Empty is unreadable rather than "no changes", and that judgement is the
 * server's own: `runner/execute.go:413-419` refuses to STAGE a patch input
 * whose `diff` is missing, non-string or empty, because a phase opened at the
 * run base would make its own patch read as a revert of its predecessor. The
 * same three shapes are the three this returns null for.
 */
export function readPatchDiff(artifact: Artifact | undefined): string | null {
  const diff = payloadOf(artifact)?.diff
  return typeof diff === 'string' && diff !== '' ? diff : null
}

export type PlanView = { summary: string; steps: PlanStep[] }

/**
 * A plan payload as prose, or null.
 *
 * `steps` is required AND nullable in the contract (`payload_dto.go:15` — a Go
 * nil slice marshals to null), and an empty plan is legal, so a missing or null
 * `steps` reads as no steps. A `steps` that is present but not an array, or one
 * carrying an element that is not a step, is a payload this renderer cannot
 * describe — and dropping the bad elements silently would show a person a
 * shorter plan than the one the gate examines.
 */
export function readPlan(artifact: Artifact | undefined): PlanView | null {
  const payload = payloadOf(artifact)
  if (payload === null || typeof payload.summary !== 'string') return null
  const raw = payload.steps
  if (raw === null || raw === undefined) return { summary: payload.summary, steps: [] }
  if (!Array.isArray(raw)) return null
  const steps: PlanStep[] = []
  for (const item of raw) {
    const step = readPlanStep(item)
    if (step === null) return null
    steps.push(step)
  }
  return { summary: payload.summary, steps }
}

function readPlanStep(value: unknown): PlanStep | null {
  const obj = asObject(value)
  if (obj === null || typeof obj.title !== 'string') return null
  // Spread conditionally rather than `detail: …` — `web/tsconfig.json:14` sets
  // exactOptionalPropertyTypes, under which an optional property refuses an
  // explicit undefined. Absent and empty mean the same thing to the renderer
  // (`payload_dto.go:20-22`), so a non-string detail is simply dropped.
  return { title: obj.title, ...(typeof obj.detail === 'string' ? { detail: obj.detail } : {}) }
}

export type VerdictView = {
  verdict: string
  findings: ReviewFinding[]
  /**
   * THE ONE FIELD WHERE THE GENERATED TYPE UNDER-DESCRIBES THE WIRE, spelled
   * here as what the wire actually carries.
   *
   * `tests` is required by the validator and nullable by design — null is "this
   * harness did not report test results", which this surface renders as unknown
   * rather than as a tick (`payload_dto.go:31-47`). But its schema is a `$ref`,
   * and this toolchain gives a `$ref` property no null branch, so
   * `ApiReviewVerdictPayload` renders it `tests?: ApiTestOutcome`: optional and
   * NON-nullable. The generated type therefore admits a shape the server does
   * not send (absent) and forbids the one it does (null).
   *
   * `readVerdict` collapses the two into this single nullable field, so nothing
   * downstream has to remember which of the two disagreeing descriptions it is
   * holding.
   */
  tests: TestOutcome | null
}

/** A review verdict payload, or null when it is not one. */
export function readVerdict(artifact: Artifact | undefined): VerdictView | null {
  const payload = payloadOf(artifact)
  if (payload === null || typeof payload.verdict !== 'string') return null
  const findings = readList(payload.findings, readFinding)
  if (findings === null) return null
  // Absent and null alike mean "nothing reported"; anything else that is not a
  // readable outcome makes the whole payload unreadable, because a verdict
  // whose test block is malformed is not a verdict whose tests are unknown.
  const raw = payload.tests
  let tests: TestOutcome | null = null
  if (raw !== null && raw !== undefined) {
    tests = readTestOutcome(raw)
    if (tests === null) return null
  }
  return { verdict: payload.verdict, findings, tests }
}

function readFinding(value: unknown): ReviewFinding | null {
  const obj = asObject(value)
  if (obj === null || typeof obj.severity !== 'string' || typeof obj.message !== 'string') {
    return null
  }
  return {
    severity: obj.severity,
    message: obj.message,
    ...(typeof obj.file === 'string' ? { file: obj.file } : {}),
    ...(typeof obj.line === 'number' ? { line: obj.line } : {}),
  }
}

function readTestOutcome(value: unknown): TestOutcome | null {
  const obj = asObject(value)
  if (obj === null || typeof obj.status !== 'string') return null
  return {
    status: obj.status,
    ...(typeof obj.passed === 'number' ? { passed: obj.passed } : {}),
    ...(typeof obj.failed === 'number' ? { failed: obj.failed } : {}),
    ...(typeof obj.details === 'string' ? { details: obj.details } : {}),
  }
}

/** A required-and-nullable array: null and absent are empty, malformed is null. */
function readList<T>(value: unknown, read: (item: unknown) => T | null): T[] | null {
  if (value === null || value === undefined) return []
  if (!Array.isArray(value)) return null
  const out: T[] = []
  for (const item of value) {
    const one = read(item)
    if (one === null) return null
    out.push(one)
  }
  return out
}

function asObject(value: unknown): Record<string, unknown> | null {
  // `typeof null === 'object'` and an array is an object too; neither is a
  // record with named fields, and both would otherwise reach the `typeof
  // obj.title === 'string'` checks above and answer false in a way that reads
  // as a missing field rather than as the wrong shape.
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return null
  return value as Record<string, unknown>
}
