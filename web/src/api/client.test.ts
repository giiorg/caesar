import { HttpResponse, http } from 'msw'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { server } from '../test/msw'
import { api, onUnauthenticated, unwrap } from './client'
import { ApiError } from './errors'

const problem = (status: number, body: Record<string, unknown>) =>
  HttpResponse.json(body, { status, headers: { 'content-type': 'application/problem+json' } })

describe('the generated client', () => {
  const redirect = vi.fn()
  beforeEach(() => {
    redirect.mockClear()
    onUnauthenticated(redirect)
  })

  it('returns typed data on success', async () => {
    server.use(
      http.get('/api/v1/auth/me', () =>
        HttpResponse.json({
          user: {
            id: 'u1',
            email: 'a@b.c',
            role: 'admin',
            active: true,
            created_at: '',
            updated_at: '',
          },
          grants: [],
        }),
      ),
    )
    const me = await unwrap(await api.GET('/api/v1/auth/me'))
    expect(me.user.email).toBe('a@b.c')
  })

  it('treats a documented 204 as success rather than a missing body', async () => {
    // POST /auth/logout answers 204 and openapi-fetch reports it as
    // `{ data: undefined }`. An `unwrap` that read "no data" as "failure" would
    // throw a fabricated ApiError on the only outcome logout ever produces, and
    // the shell's sign-out would look broken while the session really was gone.
    server.use(http.post('/api/v1/auth/logout', () => new HttpResponse(null, { status: 204 })))
    await expect(unwrap(await api.POST('/api/v1/auth/logout'))).resolves.toBeUndefined()
    expect(redirect).not.toHaveBeenCalled()
  })

  it('throws ApiError and redirects on a 401 from a session-requiring call', async () => {
    server.use(
      http.get('/api/v1/approvals', () =>
        problem(401, { title: 'Authentication required', correlation_id: 'cid-1' }),
      ),
    )
    const err = await unwrap(await api.GET('/api/v1/approvals')).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    // Asserted on this call too, not only on login: openapi-fetch drains the
    // body of EVERY non-ok response, so a client that does not forward the
    // parsed body loses the correlation id on every error surface at once.
    expect((err as ApiError).correlationId).toBe('cid-1')
    expect(redirect).toHaveBeenCalledTimes(1)
  })

  it('DOES NOT redirect on a 401 from login itself', async () => {
    // POST /auth/login answers 401 for bad credentials. A blanket redirect
    // would fire on the most common login outcome, remount the route and erase
    // the form-level error §5 requires it to show. The exemption is a property
    // of the REQUEST, not a check inferred from the current route.
    server.use(
      http.post('/api/v1/auth/login', () =>
        problem(401, {
          title: 'Authentication required',
          detail: 'invalid email or password',
          correlation_id: 'cid-2',
        }),
      ),
    )
    const err = await unwrap(
      await api.POST('/api/v1/auth/login', { body: { email: 'a@b.c', password: 'nope' } }),
    ).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).correlationId).toBe('cid-2')
    expect(redirect).not.toHaveBeenCalled()
  })

  it('DOES NOT redirect on a 401 from the change-password call', async () => {
    // PUT /api/v1/auth/password runs behind `a.authenticated(...)`
    // (backend/internal/identity/api/api.go:69), so the session is already
    // proven good before the handler runs — and the handler still answers 401:
    // `xerrors.E(ClassUnauthenticated, "current password is incorrect")`
    // (backend/internal/identity/session.go:188), which
    // httpx/problem.go maps to 401. A blanket redirect would bounce a
    // signed-in user to /login for a typo and erase the form-level error, the
    // exact failure this exemption list exists to prevent, one endpoint over.
    // The endpoint declares no `Errors:` entry either, so the generated types
    // give no hint that 401 is a real answer here; only this list catches it.
    server.use(
      http.put('/api/v1/auth/password', () =>
        problem(401, {
          title: 'Authentication required',
          detail: 'current password is incorrect',
          correlation_id: 'cid-pw',
        }),
      ),
    )
    const err = await unwrap(
      await api.PUT('/api/v1/auth/password', {
        body: { current_password: 'wrong', new_password: 'hunter2hunter2' },
      }),
    ).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).detail).toBe('current password is incorrect')
    expect(redirect).not.toHaveBeenCalled()
  })

  it('keeps the correlation id from the header when the error carries no body', async () => {
    // A 401/500 with Content-Length: 0 never reaches openapi-fetch's body
    // read at all (src/index.js:243), and the SPA-fallback 404 and a gateway
    // 502 carry no envelope either. `httpx.WithCorrelationID` sets the header
    // on every response, so end-to-end this is the only surviving thread.
    server.use(
      http.get(
        '/api/v1/approvals',
        () => new HttpResponse(null, { status: 500, headers: { 'X-Correlation-ID': 'hdr-e2e' } }),
      ),
    )
    const err = await unwrap(await api.GET('/api/v1/approvals')).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).correlationId).toBe('hdr-e2e')
  })

  it('keeps the correlation id from the header when the error body is not JSON', async () => {
    // Here openapi-fetch DOES drain the body and hands back the raw string, so
    // the pre-parsed value is a `string` rather than a problem document. The
    // header is what is left.
    server.use(
      http.get(
        '/api/v1/approvals',
        () =>
          new HttpResponse('<html>502 Bad Gateway</html>', {
            status: 502,
            headers: { 'content-type': 'text/html', 'X-Correlation-ID': 'hdr-502-e2e' },
          }),
      ),
    )
    const err = await unwrap(await api.GET('/api/v1/approvals')).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).title).toBe('Request failed')
    expect((err as ApiError).correlationId).toBe('hdr-502-e2e')
  })

  it('does not redirect on a 401 from /auth/me either', async () => {
    // The session bootstrap ASKS whether a session exists. A redirect here
    // would be the guard fighting its own probe.
    server.use(
      http.get('/api/v1/auth/me', () => problem(401, { title: 'Authentication required' })),
    )
    await expect(unwrap(await api.GET('/api/v1/auth/me'))).rejects.toBeInstanceOf(ApiError)
    expect(redirect).not.toHaveBeenCalled()
  })
})
