import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import type { components } from '../api/schema'
import { renderApp } from '../test/render-app'

type HarnessRow = components['schemas']['ApiHarnessResponse']
type CredentialRow = components['schemas']['ApiCredentialResponse']

/**
 * Annotated with the GENERATED row types, so a renamed field on
 * `HarnessResponse` or `CredentialResponse` reddens `tsc --noEmit` here rather
 * than arriving at the surface as a missing property.
 */
const harness = (over: Partial<HarnessRow> & { id: string; name: string }): HarnessRow => ({
  kind: 'claude-code',
  image_ref: 'ghcr.io/example/claude:1',
  provider_endpoint: 'https://api.anthropic.com',
  credential_id: 'c-sub',
  defaults: {},
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
  ...over,
})

const credential = (
  over: Partial<CredentialRow> & { id: string; name: string; kind: string },
): CredentialRow => ({
  key_id: 'k1',
  secret_version: 1,
  provided_at: '2026-08-01T00:00:00Z',
  fingerprint: null,
  last_used_at: null,
  last_auth_expired_at: null,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
  ...over,
})

/**
 * ALL FOUR CREDENTIAL KINDS, which is what makes the picker's filter
 * falsifiable rather than merely exercised.
 *
 * The pairing table (`assets/harnesses.go`) gives claude-code
 * {subscription_token, model_key} and codex {agent_auth_file, model_key}. Those
 * two sets OVERLAP at `model_key` and each holds one kind the other does not,
 * so a fixture missing `model_key` could not tell "filter by kind" from "show
 * exactly one kind", and a fixture missing either exclusive kind could not tell
 * the two filters apart from each other. `forge_token` is in NEITHER row — it
 * is the repo form's kind — so it is the row that catches a picker that
 * forgot to filter at all.
 */
const forge = credential({ id: 'c-forge', name: 'github-pat', kind: 'forge_token' })
const model = credential({ id: 'c-model', name: 'anthropic-key', kind: 'model_key' })
const sub = credential({ id: 'c-sub', name: 'claude-max', kind: 'subscription_token' })
const authFile = credential({ id: 'c-auth', name: 'codex-sub', kind: 'agent_auth_file' })

const credentials = (...rows: CredentialRow[]) =>
  http.get('/api/v1/credentials', () => HttpResponse.json({ credentials: rows }))

const harnesses = (...rows: HarnessRow[]) =>
  http.get('/api/v1/harnesses', () => HttpResponse.json({ harnesses: rows }))

const allCredentials = () => credentials(forge, model, sub, authFile)

const problem = (status: number, title: string, detail: string) =>
  HttpResponse.json(
    { title, status, detail, correlation_id: 'cid-harness' },
    { status, headers: { 'content-type': 'application/problem+json' } },
  )

/** The kinds a `<select>` is offering, in order. */
const optionValues = (select: HTMLElement) =>
  within(select)
    .getAllByRole('option')
    .map((o) => (o as HTMLOptionElement).value)
    .filter((v) => v !== '')

const openRegisterForm = async () => {
  await userEvent.click(await screen.findByRole('button', { name: 'Register harness' }))
  return screen.findByLabelText('Credential')
}

describe('the harnesses section', () => {
  it('renders the registry against the default list, which is null', async () => {
    /**
     * NO `/api/v1/harnesses` OVERRIDE — the `harnesses: null` default in
     * `render-app.tsx` is the fixture. That is deliberate and it is what keeps
     * `harnessesQueryOptions`' `?? []` on a live path: without the coalesce
     * this dies on `null.length` rather than passing anyway.
     */
    renderApp('/admin?section=harnesses')

    expect(await screen.findByText(/no harnesses are registered yet/i)).toBeInTheDocument()
  })

  it('shows each harness with the credential it binds, resolved to a name', async () => {
    renderApp('/admin?section=harnesses', [
      harnesses(
        harness({ id: 'h1', name: 'claude-main' }),
        harness({
          id: 'h2',
          name: 'codex-main',
          kind: 'codex',
          image_ref: 'ghcr.io/example/codex:2',
          provider_endpoint: 'https://api.openai.com',
          credential_id: 'c-auth',
        }),
      ),
      allCredentials(),
    ])

    const rows = within(await screen.findByTestId('harness-rows'))
    const first = within(await screen.findByTestId('harness-h1'))
    expect(first.getByText('claude-main')).toBeInTheDocument()
    expect(first.getByText('claude-code')).toBeInTheDocument()
    expect(first.getByText('ghcr.io/example/claude:1')).toBeInTheDocument()
    expect(first.getByText('https://api.anthropic.com')).toBeInTheDocument()
    /**
     * THE JOIN, and the two rows disagree on it. `credential_id` is all the
     * wire carries, so a row that printed the id would still show something —
     * and a section that resolved every row against the FIRST credential would
     * pass any single-row fixture.
     */
    expect(first.getByText('claude-max')).toBeInTheDocument()
    expect(
      within(await screen.findByTestId('harness-h2')).getByText('codex-sub'),
    ).toBeInTheDocument()
    expect(rows.queryByText('c-sub')).not.toBeInTheDocument()
  })

  it('narrows the credential picker to the kinds the selected harness kind can bind', async () => {
    renderApp('/admin?section=harnesses', [harnesses(), allCredentials()])

    const picker = await openRegisterForm()
    await userEvent.selectOptions(await screen.findByLabelText('Kind'), 'claude-code')
    expect(optionValues(picker)).toEqual(['c-model', 'c-sub'])

    await userEvent.selectOptions(screen.getByLabelText('Kind'), 'codex')
    /**
     * THE SET CHANGES AND THE OVERLAP STAYS. `model_key` is bindable by both
     * kinds, so it must survive the switch; `subscription_token` must leave and
     * `agent_auth_file` must arrive. A picker that simply re-filtered to one
     * kind, or that never re-filtered at all, disagrees with one of the three.
     */
    expect(optionValues(picker)).toEqual(['c-model', 'c-auth'])
    // The forge token is in neither row of the pairing table and belongs to the
    // repo form; it must never be offered here for either kind.
    expect(optionValues(picker)).not.toContain('c-forge')
  })

  it('clears a credential that the new kind cannot bind', async () => {
    const posted = vi.fn()
    renderApp('/admin?section=harnesses', [
      harnesses(),
      allCredentials(),
      http.post('/api/v1/harnesses', async ({ request }) => {
        posted(await request.json())
        return HttpResponse.json(harness({ id: 'h9', name: 'x' }), { status: 201 })
      }),
    ])

    const picker = await openRegisterForm()
    await userEvent.selectOptions(await screen.findByLabelText('Kind'), 'claude-code')
    await userEvent.selectOptions(picker, 'c-sub')
    expect(picker).toHaveValue('c-sub')

    await userEvent.selectOptions(screen.getByLabelText('Kind'), 'codex')

    /**
     * ASSERTED BY SUBMITTING, NOT BY READING THE CONTROL — and that distinction
     * is MEASURED rather than stylistic. `expect(picker).toHaveValue('')` here
     * passes whether or not the form clears anything: a native `<select>` whose
     * value is not among its options reports `''` all by itself, so the DOM
     * hides exactly the bug this control exists to prevent. Removing the
     * `onSelect` clearing left that assertion green.
     *
     * What the clearing actually protects is the FORM STATE. Without it
     * react-hook-form still holds `c-sub` — a subscription token, which cannot
     * authenticate a codex container — the schema is satisfied, and the request
     * goes out to be refused by the server on a field whose control looks
     * blank. With it, the value is empty, `.min(1)` rejects it, and the message
     * lands under the picker before anything is sent.
     */
    await userEvent.type(screen.getByLabelText('Name'), 'codex-main')
    await userEvent.type(screen.getByLabelText('Image reference'), 'ghcr.io/example/codex:2')
    await userEvent.type(screen.getByLabelText('Provider endpoint'), 'https://api.openai.com')
    await userEvent.click(screen.getByRole('button', { name: 'Register harness' }))

    expect(await screen.findByText('pick a credential')).toBeInTheDocument()
    expect(posted).not.toHaveBeenCalled()
  })

  it('registers a harness with its defaults parsed, and refreshes the list', async () => {
    const posted = vi.fn()
    let listed = 0
    renderApp('/admin?section=harnesses', [
      allCredentials(),
      http.post('/api/v1/harnesses', async ({ request }) => {
        posted(await request.json())
        return HttpResponse.json(harness({ id: 'h9', name: 'codex-main' }), { status: 201 })
      }),
      http.get('/api/v1/harnesses', () => {
        listed += 1
        return HttpResponse.json({
          harnesses:
            listed === 1
              ? [harness({ id: 'h1', name: 'claude-main' })]
              : [
                  harness({ id: 'h1', name: 'claude-main' }),
                  harness({ id: 'h9', name: 'codex-main' }),
                ],
        })
      }),
    ])

    await openRegisterForm()
    await userEvent.type(await screen.findByLabelText('Name'), 'codex-main')
    await userEvent.selectOptions(screen.getByLabelText('Kind'), 'codex')
    await userEvent.type(screen.getByLabelText('Image reference'), 'ghcr.io/example/codex:2')
    await userEvent.type(screen.getByLabelText('Provider endpoint'), 'https://api.openai.com')
    await userEvent.selectOptions(screen.getByLabelText('Credential'), 'c-auth')
    const defaults = screen.getByLabelText('Defaults (JSON)')
    await userEvent.clear(defaults)
    await userEvent.type(defaults, '{{"model":"o4"}')
    await userEvent.click(screen.getByRole('button', { name: 'Register harness' }))

    /**
     * `defaults` ARRIVES AS AN OBJECT, not as the string that was typed. The
     * wire field is a `map[string]any`, so a form that sent the raw text would
     * be refused by the transport before the handler saw it.
     */
    await expect
      .poll(() => posted.mock.calls[0]?.[0])
      .toEqual({
        name: 'codex-main',
        kind: 'codex',
        image_ref: 'ghcr.io/example/codex:2',
        provider_endpoint: 'https://api.openai.com',
        credential_id: 'c-auth',
        defaults: { model: 'o4' },
      })
    // The invalidation, asserted through the rendered list rather than a request
    // count: the second row only appears if `['harnesses']` was refetched.
    expect(
      await within(await screen.findByTestId('harness-rows')).findByText('codex-main'),
    ).toBeInTheDocument()
  })

  it('refuses defaults that are not a JSON object, without asking the server', async () => {
    const posted = vi.fn()
    renderApp('/admin?section=harnesses', [
      harnesses(),
      allCredentials(),
      http.post('/api/v1/harnesses', () => {
        posted()
        return HttpResponse.json(harness({ id: 'h9', name: 'x' }), { status: 201 })
      }),
    ])

    await openRegisterForm()
    await userEvent.type(await screen.findByLabelText('Name'), 'codex-main')
    await userEvent.selectOptions(screen.getByLabelText('Kind'), 'codex')
    await userEvent.type(screen.getByLabelText('Image reference'), 'ghcr.io/example/codex:2')
    await userEvent.type(screen.getByLabelText('Provider endpoint'), 'https://api.openai.com')
    await userEvent.selectOptions(screen.getByLabelText('Credential'), 'c-auth')

    const defaults = screen.getByLabelText('Defaults (JSON)')
    await userEvent.clear(defaults)
    await userEvent.type(defaults, 'not json at all')
    await userEvent.click(screen.getByRole('button', { name: 'Register harness' }))
    expect(await screen.findByText('defaults must be valid JSON')).toBeInTheDocument()

    /**
     * THE SECOND ARM IS THE ONE `JSON.parse` ALONE MISSES. `[1,2]` is perfectly
     * valid JSON and still cannot decode into a `map[string]any`, so a form
     * that only tried to parse would send it and get back a transport-level
     * complaint about a malformed body — a message about the request rather
     * than about this field.
     */
    await userEvent.clear(defaults)
    // `[[` is user-event's escape for a literal `[` — a bare one opens a key
    // descriptor and the parser rejects the comma.
    await userEvent.type(defaults, '[[1,2]')
    await userEvent.click(screen.getByRole('button', { name: 'Register harness' }))
    expect(await screen.findByText('defaults must be a JSON object, like {}')).toBeInTheDocument()

    expect(posted).not.toHaveBeenCalled()
  })

  it('refuses a provider endpoint the server would refuse, and allows http on localhost', async () => {
    renderApp('/admin?section=harnesses', [harnesses(), allCredentials()])

    await openRegisterForm()
    const endpoint = await screen.findByLabelText('Provider endpoint')
    await userEvent.type(await screen.findByLabelText('Name'), 'h')
    await userEvent.type(endpoint, 'http://api.example.com')
    await userEvent.click(screen.getByRole('button', { name: 'Register harness' }))
    expect(
      await screen.findByText('must be an https URL (http allowed for localhost only)'),
    ).toBeInTheDocument()

    /**
     * THE LOCALHOST ARM, which is why this is not a `startsWith('https://')`.
     * The server allows plain http for localhost and 127.0.0.1 (the dev case),
     * so a client stricter than that would refuse a configuration the
     * deployment actually uses.
     */
    await userEvent.clear(endpoint)
    await userEvent.type(endpoint, 'http://localhost:8080')
    await userEvent.click(screen.getByRole('button', { name: 'Register harness' }))
    await expect
      .poll(() => screen.queryByText('must be an https URL (http allowed for localhost only)'))
      .toBeNull()
  })

  it('edits a harness without offering to change its kind', async () => {
    const patched = vi.fn()
    renderApp('/admin?section=harnesses', [
      harnesses(harness({ id: 'h2', name: 'codex-main', kind: 'codex', credential_id: 'c-auth' })),
      allCredentials(),
      http.patch('/api/v1/harnesses/:id', async ({ request }) => {
        patched(await request.json())
        return HttpResponse.json(harness({ id: 'h2', name: 'codex-renamed', kind: 'codex' }))
      }),
    ])

    await userEvent.click(await screen.findByRole('button', { name: 'Edit codex-main' }))
    const name = await screen.findByLabelText('Name')

    /**
     * KIND IS STATIC TEXT, NOT A DISABLED CONTROL. `HarnessUpdate` carries no
     * kind at all, so there is no request that could change it — and a disabled
     * select reads as a control that is unavailable right now, something
     * another field might unlock. `queryByLabelText` is what tells the two
     * apart: a disabled select is still a labelled form control.
     */
    const sheet = within(screen.getByRole('dialog'))
    expect(sheet.queryByLabelText('Kind')).not.toBeInTheDocument()
    expect(sheet.getByText('codex')).toBeInTheDocument()

    /**
     * AND THE PICKER IS STILL FILTERED — by the ROW's kind, since there is no
     * control to read one from. This is the server's own rule on an update
     * ("Kind is immutable, so the row's own kind picks the set").
     */
    expect(optionValues(sheet.getByLabelText('Credential'))).toEqual(['c-model', 'c-auth'])

    await userEvent.clear(name)
    await userEvent.type(name, 'codex-renamed')
    await userEvent.click(screen.getByRole('button', { name: 'Save harness' }))

    await expect
      .poll(() => patched.mock.calls[0]?.[0])
      .toEqual({
        name: 'codex-renamed',
        image_ref: 'ghcr.io/example/claude:1',
        provider_endpoint: 'https://api.anthropic.com',
        credential_id: 'c-auth',
        defaults: {},
      })
    // No `kind` on the PATCH body — asserted by the toEqual above, which is
    // exact, and stated here because it is the point of the test.
    expect(Object.keys(patched.mock.calls[0]?.[0] as object)).not.toContain('kind')
  })

  it('answers a member with the server’s own refusal, not the project-grant sentence', async () => {
    renderApp('/admin?section=harnesses', [
      http.get('/api/v1/harnesses', () => problem(403, 'Forbidden', 'admin role required')),
    ])

    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText('admin role required')).toBeInTheDocument()
    expect(within(alert).getByText('cid-harness')).toBeInTheDocument()
    /**
     * THE ASSERTION THAT KILLS THE MUTANT. Routing this through `ApiErrorPanel`
     * renders a refusal too — one that tells a member to ask an admin for a
     * project GRANT when what they lack is the admin role. A test asserting
     * only that some alert appeared passes either way.
     */
    expect(alert.textContent).not.toMatch(/grant/i)
  })

  it('surfaces a delete refusal in the server’s words', async () => {
    renderApp('/admin?section=harnesses', [
      harnesses(harness({ id: 'h1', name: 'claude-main' })),
      allCredentials(),
      http.delete('/api/v1/harnesses/:id', () => problem(404, 'Not Found', 'no such harness')),
    ])

    await userEvent.click(await screen.findByRole('button', { name: 'Delete claude-main' }))
    /**
     * A 404, NOT A 409 — and that is the schema's shape rather than a shortcut.
     * Nothing in the database references `harnesses`, so `DeleteHarness`
     * classifies only the missing row; there is no in-use refusal for a harness
     * the way there is for a credential, and a test asserting one would assert
     * a response no deployment can produce.
     */
    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText('no such harness')).toBeInTheDocument()
  })
})
