import type { components } from './schema'

type Problem = components['schemas']['HttpxProblemDetails']

export type FieldError = { field: string; message: string }

/**
 * The problem+json envelope as a typed error. It has exactly three consumers
 * and they are the whole error design:
 *
 *  1. Forms map `fields` onto fields by FLAT NAME (xerrors.FieldError is
 *     {field, message} and the name is a plain identifier — a pointer-shaped
 *     mapper would match nothing on every real validation failure).
 *  2. A 401 from a call that required an existing session redirects to /login.
 *  3. `correlationId` is rendered on every error surface: it is the only thread
 *     between a user's complaint and a server log line, and a UI that swallows
 *     it severs that thread permanently.
 */
export class ApiError extends Error {
  readonly type: string
  readonly title: string
  readonly status: number
  readonly detail: string
  readonly correlationId: string
  readonly fields: FieldError[]

  constructor(init: {
    type: string
    title: string
    status: number
    detail: string
    correlationId: string
    fields: FieldError[]
  }) {
    super(init.detail !== '' ? init.detail : init.title)
    this.name = 'ApiError'
    this.type = init.type
    this.title = init.title
    this.status = init.status
    this.detail = init.detail
    this.correlationId = init.correlationId
    this.fields = init.fields
  }

  /**
   * Built from the HTTP STATUS, never from the generated response union. The
   * body is best-effort: a proxy 502 or a truncated stream is still an error
   * the UI must render honestly rather than crash on.
   *
   * `parsedBody` exists because a Response body can only be read once and, on
   * every error, openapi-fetch reads it first: `let error = await
   * response.text()` (openapi-fetch@0.17.0 src/index.js:267). `res.clone()`
   * after that throws, so a caller that already drained the stream must hand
   * the body back or the correlation id is lost on EVERY error surface — the
   * one constraint this class exists to uphold. Callers holding an unread
   * Response (the SSE connection, an error boundary) simply omit it.
   */
  static async fromResponse(res: Response, parsedBody?: unknown): Promise<ApiError> {
    // THIS FUNCTION MUST NOT THROW. It is the last thing standing between a
    // malformed response and the user, and every caller — unwrap, the 401
    // interceptor, the error panel — assumes it returns an ApiError. A
    // TypeError escaping here does not merely lose the correlation id; it
    // bypasses the whole error path and surfaces as an unhandled rejection.
    //
    // `JSON.parse` succeeding does NOT mean the body is a problem document.
    // `null`, `[]` and `"text"` are all valid JSON, and `null` in particular
    // survives the try/catch and then throws on the first property read —
    // which is why the check is `typeof === 'object' && !== null` and not a
    // truthiness test.
    let parsed: unknown = parsedBody
    if (parsed === undefined) {
      try {
        // `clone()` itself throws once the body has been read, which is why
        // this whole read — not just the `json()` — is inside the try.
        parsed = await res.clone().json()
      } catch {
        /* not JSON, or already drained; the status still is */
      }
    }
    let body: Partial<Problem> = {}
    if (typeof parsed === 'object' && parsed !== null && !Array.isArray(parsed)) {
      body = parsed as Partial<Problem>
    }
    // Same reasoning one level down, twice. A server that sends `errors: {}`
    // or `errors: "none"` would otherwise reach `.map` on a non-array — and
    // an array is not enough either: `[null]` passes Array.isArray and then
    // yields a `{field: '', message: ''}` entry, an empty field error the
    // form layer would render as a blank bullet. Entries that are not objects
    // are dropped, not defaulted.
    const errors = (Array.isArray(body.errors) ? body.errors : []).filter(
      (e): e is { field?: string; message?: string } => typeof e === 'object' && e !== null,
    )
    return new ApiError({
      type: body.type ?? 'about:blank',
      title: body.title ?? 'Request failed',
      status: res.status,
      detail: body.detail ?? '',
      correlationId: body.correlation_id ?? res.headers.get('X-Correlation-ID') ?? '',
      fields: errors.map((e) => ({ field: e.field ?? '', message: e.message ?? '' })),
    })
  }
}
