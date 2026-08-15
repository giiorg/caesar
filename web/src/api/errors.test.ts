import { describe, expect, it } from 'vitest'
import { ApiError } from './errors'

function problem(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/problem+json' },
  })
}

describe('ApiError.fromResponse', () => {
  it('carries the whole envelope, including the correlation id', async () => {
    const err = await ApiError.fromResponse(
      problem(400, {
        type: 'https://caesar.dev/problems/invalid',
        title: 'Invalid request',
        status: 400,
        detail: 'validation failed',
        correlation_id: 'abc123',
        errors: [{ field: 'email', message: 'must be an email' }],
      }),
    )
    expect(err.status).toBe(400)
    expect(err.title).toBe('Invalid request')
    expect(err.detail).toBe('validation failed')
    expect(err.correlationId).toBe('abc123')
    expect(err.fields).toEqual([{ field: 'email', message: 'must be an email' }])
  })

  it('takes the status from the RESPONSE, not the body', async () => {
    // The document declares only 200 and 400 on these endpoints while login
    // really answers 401. A client that trusted the body — or the generated
    // union — would have no branch for the most common login outcome.
    const err = await ApiError.fromResponse(problem(401, { title: 'Authentication required' }))
    expect(err.status).toBe(401)
  })

  it('survives a body that is not problem+json at all', async () => {
    const err = await ApiError.fromResponse(new Response('<html>502</html>', { status: 502 }))
    expect(err.status).toBe(502)
    expect(err.title).toBe('Request failed')
    expect(err.fields).toEqual([])
  })

  it('takes a pre-parsed body from a caller that already drained the stream', async () => {
    // openapi-fetch reads `await response.text()` on every non-ok response, so
    // by the time unwrap gets there `res.clone()` throws and the envelope is
    // unreachable from the Response alone. Without the second parameter the
    // correlation id is empty on EVERY error surface.
    const res = problem(401, { title: 'Authentication required', correlation_id: 'cid-drained' })
    const drained = await res.text()
    const err = await ApiError.fromResponse(res, JSON.parse(drained))
    expect(err.status).toBe(401)
    expect(err.correlationId).toBe('cid-drained')
  })

  it('degrades to the status alone when the stream is drained and nothing is handed back', async () => {
    const res = problem(500, { title: 'Boom', correlation_id: 'cid-lost' })
    await res.text()
    const err = await ApiError.fromResponse(res)
    expect(err.status).toBe(500)
    expect(err.title).toBe('Request failed')
    expect(err.correlationId).toBe('')
  })

  // The header fallback is the ONLY correlation-id source on every error
  // surface that carries no problem document, and those are not exotic: the
  // SPA-fallback 404, a gateway 502 and a 401 with Content-Length: 0 all reach
  // here with no body at all. `httpx.WithCorrelationID`
  // (backend/internal/platform/httpx/middleware.go:25,43) sets
  // `X-Correlation-ID` on EVERY response, body or no body, so the header is
  // the thread that survives when the envelope does not.
  it('falls back to the X-Correlation-ID header when there is no body', async () => {
    const err = await ApiError.fromResponse(
      new Response(null, { status: 500, headers: { 'X-Correlation-ID': 'hdr-cid' } }),
    )
    expect(err.status).toBe(500)
    expect(err.correlationId).toBe('hdr-cid')
  })

  it('falls back to the X-Correlation-ID header when the body is not a problem document', async () => {
    const err = await ApiError.fromResponse(
      new Response('<html>502</html>', {
        status: 502,
        headers: { 'X-Correlation-ID': 'hdr-502' },
      }),
    )
    expect(err.status).toBe(502)
    expect(err.title).toBe('Request failed')
    expect(err.correlationId).toBe('hdr-502')
  })

  it('prefers the envelope over the header when both carry an id', async () => {
    // Order matters and is otherwise unfalsifiable: the body id is the one the
    // handler logged alongside the failure, and the header is only ever a
    // fallback for responses that have no envelope at all.
    const res = problem(500, { title: 'Boom', correlation_id: 'body-cid' })
    const withHeader = new Response(res.body, {
      status: 500,
      headers: { 'content-type': 'application/problem+json', 'X-Correlation-ID': 'hdr-cid' },
    })
    const err = await ApiError.fromResponse(withHeader)
    expect(err.correlationId).toBe('body-cid')
  })

  it.each([
    ['a drained non-JSON body handed back as text', '<html>502</html>'],
    ['an explicitly empty body', ''],
  ])('does not throw on %s', async (_name, given) => {
    const err = await ApiError.fromResponse(new Response(null, { status: 502 }), given)
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(502)
    expect(err.title).toBe('Request failed')
    expect(err.fields).toEqual([])
  })

  // The case above only covers UNPARSEABLE bodies, which the try/catch already
  // handles. These are the ones that PARSE and still are not problem
  // documents — the gap a catch block cannot see. Each threw a TypeError
  // before the shape guards existed, which does not merely lose the
  // correlation id: it escapes fromResponse entirely, so unwrap never throws
  // an ApiError and the 401 interceptor and error panel are both bypassed.
  it.each([
    ['null', null],
    ['an array', []],
    ['a bare string', 'nope'],
    ['a number', 7],
    ['errors as an object', { title: 'x', errors: {} }],
    ['errors as a string', { title: 'x', errors: 'none' }],
    ['a null entry in errors', { title: 'x', errors: [null] }],
  ])('does not throw on %s', async (_name, body) => {
    const err = await ApiError.fromResponse(problem(500, body))
    expect(err).toBeInstanceOf(ApiError)
    expect(err.status).toBe(500)
    expect(err.fields).toEqual([])
  })
})
