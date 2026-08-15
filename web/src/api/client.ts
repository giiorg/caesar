import createClient from 'openapi-fetch'
import { ApiError } from './errors'
import type { paths } from './schema'

// Same-origin by construction (§8): no CORS, no reverse proxy, and the
// __Host- session cookie rides every request because there is only one origin.
// The graduation path to a separate deployment is this one string.
//
// It is `location.origin` and not `''` — MEASURED, not assumed. openapi-fetch
// 0.17.0 concatenates baseUrl + path and hands the result straight to
// `new Request(...)` (src/index.js:120, createFinalURL at :639). Only a real
// browser resolves a relative Request against the document; under
// vitest/jsdom the global Request is Node's, and `new Request('/api/v1/auth/me')`
// throws `TypeError: Failed to parse URL`. With `baseUrl: ''` every one of this
// file's tests died before reaching a handler. Naming the current origin keeps
// the request same-origin in the browser AND in the dev server (Vite's /api
// proxy sees the same absolute URL) while giving the test environment a URL it
// can parse.
export const api = createClient<paths>({
  baseUrl: window.location.origin,
  credentials: 'same-origin',
  // Late-bound, and this is load-bearing rather than stylistic. openapi-fetch
  // captures `globalThis.fetch` ONCE, at createClient() time
  // (src/index.js:28, `fetch: baseFetch = globalThis.fetch`), and this module is
  // imported while a test file is being collected — before any `beforeAll`
  // runs. MEASURED: with the default, the client held the pre-MSW fetch and
  // every test in client.test.ts left the process and died with ECONNREFUSED on
  // localhost:3000, i.e. the interceptor was installed and simply not consulted.
  // Resolving the global per call makes the client use whichever fetch is
  // installed at request time, which is also the honest reading of "the client
  // uses the platform's fetch".
  //
  // Spelled BARE, not `globalThis.fetch`. The object-literal key `fetch:` is not
  // a binding in scope, so the identifier inside the arrow resolves to the
  // global and the late binding is identical. What differs is lint: the bare
  // spelling is the one `noRestrictedGlobals` actually sees, so this file
  // genuinely needs its biome.json exemption. With `globalThis.fetch` the
  // exemption was vacuous — MEASURED, `biome ci src/api/client.ts` passed with
  // the override deleted — and this file was teaching the qualified spelling as
  // house style while that spelling was the one hole in the ban.
  fetch: (request) => fetch(request),
})

/**
 * The authentication calls, exempt from the global 401 redirect.
 *
 * EXPLICIT rather than emergent. POST /auth/login answers 401 for bad
 * credentials and GET /auth/me answers 401 to mean "no session" — which is the
 * question the bootstrap is asking. Inferring the exemption from the current
 * route would break the moment a login form is rendered anywhere else.
 *
 * PUT /auth/password is the non-obvious third. It runs behind
 * `a.authenticated(...)` (internal/identity/api/api.go:69), so the session is
 * already proven good when the handler runs, and the handler ITSELF answers
 * `xerrors.E(ClassUnauthenticated, "current password is incorrect")`
 * (internal/identity/session.go:188) → 401. A 401 there means "wrong current
 * password", not "no session". The endpoint carries no `Errors:` entry, so the
 * generated types give no hint that 401 is a real answer; this list is the only
 * thing standing between a typo and a redirect that erases the form error.
 *
 * All three are ambiguous in principle — an expired cookie also yields 401 on
 * any of them — and the exemption is the right side of that ambiguity: a
 * missing redirect leaves a rendered error and the next authorization-scoped
 * query redirects anyway, while a spurious redirect destroys what the user
 * typed and the message explaining why.
 */
const AUTH_EXEMPT: ReadonlySet<string> = new Set([
  '/api/v1/auth/login',
  '/api/v1/auth/me',
  '/api/v1/auth/password',
])

let redirectToLogin: () => void = () => {}

/** Registered by the router in Task 7; a bare module cannot navigate. */
export function onUnauthenticated(handler: () => void): void {
  redirectToLogin = handler
}

export async function unwrap<T>(result: {
  data?: T
  error?: unknown
  response: Response
}): Promise<T> {
  const { data, error, response } = result as { data?: T; error?: unknown; response: Response }
  // Success is `response.ok`, NOT `data !== undefined`. POST /auth/logout
  // answers 204 (backend/api/openapi.json) and openapi-fetch returns
  // `{ data: undefined, response }` for it (src/index.js:245), so an
  // undefined-means-failure test would throw a fabricated ApiError on the one
  // outcome logout ever produces. It also cannot mask a broken success body:
  // on an ok response openapi-fetch calls `response.json()` directly and lets a
  // parse error REJECT, so `ok && data === undefined` means the body was
  // genuinely empty (204, HEAD, Content-Length: 0) and nothing else.
  if (response.ok) return data as T

  // `error` is openapi-fetch's already-parsed body. It must be forwarded: the
  // library drains the stream on every non-ok response, so ApiError could not
  // re-read it and would silently produce an empty correlation id.
  const err = await ApiError.fromResponse(response, error)
  if (err.status === 401 && !AUTH_EXEMPT.has(new URL(response.url, 'http://x').pathname)) {
    redirectToLogin()
  }
  throw err
}
