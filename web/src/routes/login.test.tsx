import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { server } from '../test/msw'
import { LoginForm } from './login'

const problem = (status: number, body: Record<string, unknown>) =>
  HttpResponse.json(body, { status, headers: { 'content-type': 'application/problem+json' } })

describe('the login form', () => {
  it('rejects a malformed email before any request is made', async () => {
    // The handler is a TRIPWIRE, not a stub. Without it "before any request is
    // made" is only implied: msw's onUnhandledRequest:'error' would fail the
    // test for a submitted request, but somewhere far from this assertion and
    // for a reason that reads like a missing fixture. `posted` says it plainly.
    let posted = 0
    server.use(
      http.post('/api/v1/auth/login', () => {
        posted++
        return problem(500, { title: 'the client-side schema let this through' })
      }),
    )
    render(<LoginForm onSuccess={vi.fn()} />)
    // The two attribute assertions are this test's own PRECONDITION, and
    // without them it silently stops testing what it claims to. Reaching the
    // alert at all depends on `noValidate`: an <input type="email"> holding
    // 'not-an-email' fails native interactive validation, the submit event is
    // never fired, and react-hook-form never runs — so the zod message is
    // unreachable and the user gets the browser's native bubble instead.
    // MEASURED: hard-coding `type="text"` in TextField left this whole suite
    // 6/6 green, and the composite of that plus reverting
    // `<form {...form.formProps}>` to `<form onSubmit={form.submit}>` — i.e.
    // dropping noValidate entirely — was ALSO 6/6 green. `type` is checked
    // here rather than in a TextField unit test because it is here that it is
    // load-bearing. The password assertion is a different guard on the same
    // line: a one-token edit in the shared registry would render a typed
    // password in cleartext, and nothing else in the project would notice.
    expect(screen.getByLabelText(/email/i)).toHaveAttribute('type', 'email')
    expect(screen.getByLabelText(/password/i)).toHaveAttribute('type', 'password')
    // Same class of defect, same element, found by the same sweep: MEASURED,
    // deleting `autoComplete={props.autoComplete}` from TextField altogether,
    // and separately changing this form's `current-password` to `off`, each
    // left the whole suite green. These two tokens are the entire contract
    // with a password manager — get them wrong and it stops offering to fill
    // or to save the credential, which is invisible to every other check here.
    expect(screen.getByLabelText(/email/i)).toHaveAttribute('autocomplete', 'username')
    expect(screen.getByLabelText(/password/i)).toHaveAttribute('autocomplete', 'current-password')
    await userEvent.type(screen.getByLabelText(/email/i), 'not-an-email')
    await userEvent.type(screen.getByLabelText(/password/i), 'hunter2hunter2')
    await userEvent.click(screen.getByRole('button', { name: /sign in/i }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/email/i)
    expect(posted).toBe(0)
  })

  it('shows the problem detail and correlation id on bad credentials, and does NOT navigate', async () => {
    // This is the single most common outcome of this endpoint. A blanket 401
    // redirect would remount the route and erase exactly this message.
    server.use(
      http.post('/api/v1/auth/login', () =>
        problem(401, {
          title: 'Authentication required',
          detail: 'invalid email or password',
          correlation_id: 'cid-login',
        }),
      ),
    )
    const onSuccess = vi.fn()
    render(<LoginForm onSuccess={onSuccess} />)
    await userEvent.type(screen.getByLabelText(/email/i), 'user@example.com')
    await userEvent.type(screen.getByLabelText(/password/i), 'wrongpassword')
    await userEvent.click(screen.getByRole('button', { name: /sign in/i }))

    // Queried by ROLE, not by text. This is the only place in the project that
    // holds FormError's `role="alert"` — the banner is what a screen-reader
    // user is told about a rejected login, and a plain <div> would say nothing.
    // MEASURED: with `findByText` here, deleting `role="alert"` from FormError
    // left all 12 tests green; test 1's alert is the FIELD's <p role="alert">,
    // not the banner's, so it does not cover this. This test renders exactly
    // one alert — the credentials are schema-valid and the 401 names no field,
    // so there is no field error — which is what makes the role query
    // unambiguous here and not in the tests above.
    const banner = await screen.findByRole('alert')
    expect(banner).toHaveTextContent('invalid email or password')
    // Inside the alert, not merely somewhere on the page: the correlation id is
    // the only thread between this user's complaint and a server log line, and
    // it is worth nothing if it is not announced with the failure.
    expect(banner).toHaveTextContent('reference: cid-login')
    expect(onSuccess).not.toHaveBeenCalled()
    expect(screen.getByLabelText(/email/i)).toHaveValue('user@example.com')
  })

  it('surfaces lockout as its own message rather than as bad credentials', async () => {
    server.use(
      http.post('/api/v1/auth/login', () =>
        problem(429, {
          title: 'Rate limited',
          detail: 'too many failed attempts, try again later',
          correlation_id: 'cid-lock',
        }),
      ),
    )
    render(<LoginForm onSuccess={vi.fn()} />)
    await userEvent.type(screen.getByLabelText(/email/i), 'user@example.com')
    await userEvent.type(screen.getByLabelText(/password/i), 'wrongpassword')
    await userEvent.click(screen.getByRole('button', { name: /sign in/i }))
    expect(await screen.findByText(/too many failed attempts/i)).toBeInTheDocument()
  })

  it('puts a server field error on that field, and still shows the reference', async () => {
    // The end-to-end proof that `knownFields` really is
    // Object.keys(defaultValues): nothing else in the suite makes the mapper
    // route a name. It also pins the FormError branch its own comment argues
    // for — a routed 400 leaves formError null, and returning null on a null
    // MESSAGE would drop the correlation id in the commonest server rejection
    // there is.
    server.use(
      http.post('/api/v1/auth/login', () =>
        problem(400, {
          title: 'Invalid request',
          correlation_id: 'cid-400',
          errors: [{ field: 'password', message: 'password is too short' }],
        }),
      ),
    )
    render(<LoginForm onSuccess={vi.fn()} />)
    await userEvent.type(screen.getByLabelText(/email/i), 'user@example.com')
    await userEvent.type(screen.getByLabelText(/password/i), 'short')
    await userEvent.click(screen.getByRole('button', { name: /sign in/i }))

    expect(await screen.findByText('password is too short')).toBeInTheDocument()
    // Not just "the text is somewhere on the page": this is the label/htmlFor
    // and aria-describedby wiring, i.e. what a screen reader would announce.
    expect(screen.getByLabelText(/password/i)).toHaveAccessibleDescription('password is too short')
    expect(screen.getByLabelText(/password/i)).toBeInvalid()
    expect(screen.getByText(/cid-400/)).toBeInTheDocument()
  })

  it('tells the user the server is unreachable rather than doing nothing at all', async () => {
    // The commonest failure on a single-user self-hosted deployment is the
    // backend being down or mid-restart, and /login is where the user meets it.
    // A transport failure produces no Response and therefore no ApiError, so
    // rethrowing it — as the plan's `if (!(e instanceof ApiError)) throw e`
    // does — throws inside handleSubmit's async callback, which React never
    // awaits. MEASURED against that version: zero alerts, the button
    // re-enabled, and one unhandled rejection ('TypeError: Failed to fetch').
    // The user is left with a form that silently refuses.
    server.use(http.post('/api/v1/auth/login', () => HttpResponse.error()))
    const onSuccess = vi.fn()
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {})
    render(<LoginForm onSuccess={onSuccess} />)
    await userEvent.type(screen.getByLabelText(/email/i), 'user@example.com')
    await userEvent.type(screen.getByLabelText(/password/i), 'correcthorsebattery')
    await userEvent.click(screen.getByRole('button', { name: /sign in/i }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/could not be completed/i)
    expect(onSuccess).not.toHaveBeenCalled()
    // The user-facing message is deliberately generic, so the ONLY place the
    // real cause survives is the console. Dropping this line would leave a
    // TypeError from a genuine bug in this app indistinguishable, to whoever
    // has to debug it, from the backend being down.
    expect(consoleError).toHaveBeenCalledWith(expect.any(Error))
  })

  it('calls onSuccess on a 200', async () => {
    server.use(
      http.post('/api/v1/auth/login', () =>
        // Shape taken from schema.d.ts components['schemas']['ApiUserResponse'],
        // where all six members are required.
        HttpResponse.json({
          id: 'u1',
          email: 'user@example.com',
          role: 'admin',
          active: true,
          created_at: '',
          updated_at: '',
        }),
      ),
    )
    const onSuccess = vi.fn()
    render(<LoginForm onSuccess={onSuccess} />)
    await userEvent.type(screen.getByLabelText(/email/i), 'user@example.com')
    await userEvent.type(screen.getByLabelText(/password/i), 'correcthorsebattery')
    await userEvent.click(screen.getByRole('button', { name: /sign in/i }))
    await vi.waitFor(() => expect(onSuccess).toHaveBeenCalledTimes(1))
  })

  it('sends exactly the credentials that were typed, as the generated request body', async () => {
    // openapi-fetch shapes the request, so this is the only assertion in the
    // suite that the endpoint receives ApiLoginRequest at all. A form that
    // posted `{}` — or posted to the wrong path — passes every test above
    // except this one, because a 200 is a 200 whatever was sent.
    let body: unknown
    server.use(
      http.post('/api/v1/auth/login', async ({ request }) => {
        body = await request.json()
        return HttpResponse.json({
          id: 'u1',
          email: 'user@example.com',
          role: 'admin',
          active: true,
          created_at: '',
          updated_at: '',
        })
      }),
    )
    const onSuccess = vi.fn()
    render(<LoginForm onSuccess={onSuccess} />)
    await userEvent.type(screen.getByLabelText(/email/i), 'user@example.com')
    await userEvent.type(screen.getByLabelText(/password/i), 'correcthorsebattery')
    await userEvent.click(screen.getByRole('button', { name: /sign in/i }))
    await vi.waitFor(() => expect(onSuccess).toHaveBeenCalledTimes(1))
    expect(body).toEqual({ email: 'user@example.com', password: 'correcthorsebattery' })
  })
})
