import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import type { components } from '../api/schema'
import { renderApp } from '../test/render-app'

type CredentialRow = components['schemas']['ApiCredentialResponse']

const credential = (
  over: Partial<CredentialRow> & { id: string; name: string; kind: string },
): CredentialRow => ({
  key_id: 'k1',
  secret_version: 1,
  provided_at: '2026-08-10T00:00:00Z',
  fingerprint: null,
  last_used_at: null,
  last_auth_expired_at: null,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-10T00:00:00Z',
  ...over,
})

/** 64 hex characters, the width `authFileFingerprint` produces (sha256). */
const FINGERPRINT = 'a'.repeat(64)

/**
 * ALL FOUR KINDS, AND ONLY ONE OF THEM CARRIES A FINGERPRINT. A fixture holding
 * just the agent auth file and one other kind would let "n/a for everything
 * that is not agent_auth_file" be implemented as "n/a for forge tokens"; three
 * non-fingerprinted kinds is what makes the rule the rule.
 */
const forge = credential({ id: 'c-forge', name: 'github-pat', kind: 'forge_token' })
const model = credential({ id: 'c-model', name: 'anthropic-key', kind: 'model_key' })
const sub = credential({ id: 'c-sub', name: 'claude-max', kind: 'subscription_token' })
const authFile = credential({
  id: 'c-auth',
  name: 'codex-sub',
  kind: 'agent_auth_file',
  fingerprint: FINGERPRINT,
})

const credentials = (...rows: CredentialRow[]) =>
  http.get('/api/v1/credentials', () => HttpResponse.json({ credentials: rows }))

const allKinds = () => credentials(forge, model, sub, authFile)

const problem = (status: number, title: string, detail: string) =>
  HttpResponse.json(
    { title, status, detail, correlation_id: 'cid-cred' },
    { status, headers: { 'content-type': 'application/problem+json' } },
  )

const row = async (id: string) => within(await screen.findByTestId(`credential-${id}`))

/**
 * `userEvent.type` reads `{` and `[` as the openers of its key-descriptor
 * syntax, and doubling each is how the library spells a literal one. A Codex
 * auth.json is nothing but braces, so typing one raw makes the PARSER throw
 * before the field is touched — a failure that looks like the form rejecting
 * the secret and is not.
 */
const typeText = (field: HTMLElement, text: string) =>
  userEvent.type(
    field,
    text.replace(/[{[]/g, (c) => c + c),
  )

describe('the credentials section', () => {
  it('renders the list against the default, which is null', async () => {
    /**
     * NO OVERRIDE — `credentials: null` is `render-app.tsx`'s default, and this
     * is what keeps `credentialsQueryOptions`' `?? []` on a live path from this
     * suite as well as from the repo form's.
     */
    renderApp('/admin?section=credentials')

    expect(await screen.findByText(/no credentials are stored yet/i)).toBeInTheDocument()
  })

  it('shows a fingerprint only for the kind that has one, and says why for the rest', async () => {
    renderApp('/admin?section=credentials', [allKinds()])

    expect((await row('c-auth')).getByText(FINGERPRINT)).toBeInTheDocument()
    for (const id of ['c-forge', 'c-model', 'c-sub']) {
      expect(
        (await row(id)).getByText(/n\/a — only agent auth files carry one/i),
      ).toBeInTheDocument()
    }
    /**
     * AND THE SENTENCE IS NOT ON THE ROW THAT HAS ONE. Rendering both would
     * pass every assertion above while telling the reader that the credential
     * showing a 64-hex string does not carry a fingerprint.
     */
    expect((await row('c-auth')).queryByText(/n\/a/i)).not.toBeInTheDocument()
  })

  it('distinguishes an auth expiry that still stands from one already answered', async () => {
    /**
     * THE ROWS DIFFER ONLY IN THE COMPARISON THIS TEST IS ABOUT. Both are
     * agent auth files, both were supplied on the 10th, both have a non-null
     * `last_auth_expired_at` — so a screen that rendered the sentence whenever
     * the field is set cannot tell them apart, and neither can one that never
     * rendered it. Nothing clears `last_auth_expired_at` (rotation advances
     * `secret_version` and `provided_at` and leaves the stamp), so the second
     * row is not a contrived state: it is what EVERY expired credential looks
     * like after the operator has done exactly what the first row asks.
     */
    renderApp('/admin?section=credentials', [
      credentials(
        credential({
          id: 'c-live',
          name: 'still-broken',
          kind: 'agent_auth_file',
          fingerprint: FINGERPRINT,
          provided_at: '2026-08-10T00:00:00Z',
          last_auth_expired_at: '2026-08-20T00:00:00Z',
        }),
        credential({
          id: 'c-fixed',
          name: 'already-rotated',
          kind: 'agent_auth_file',
          fingerprint: FINGERPRINT,
          provided_at: '2026-08-10T00:00:00Z',
          last_auth_expired_at: '2026-08-05T00:00:00Z',
          secret_version: 2,
        }),
      ),
    ])

    const live = await row('c-live')
    expect(live.getByText(/auth expired/i)).toBeInTheDocument()
    expect(live.getByText(/2026-08-20T00:00:00Z/)).toBeInTheDocument()
    expect(live.getByText(/rotate the secret/i)).toBeInTheDocument()

    const fixed = await row('c-fixed')
    // The expiry is still SHOWN — it is history worth reading — but the call to
    // action is gone, because the thing it asks for has already happened.
    expect(fixed.getByText(/auth expired/i)).toBeInTheDocument()
    expect(fixed.getByText(/a secret has been supplied since/i)).toBeInTheDocument()
    expect(fixed.queryByText(/rotate the secret/i)).not.toBeInTheDocument()
  })

  it('says nothing about expiry for a credential whose auth has never died', async () => {
    renderApp('/admin?section=credentials', [credentials(sub)])

    expect((await row('c-sub')).queryByText(/auth expired/i)).not.toBeInTheDocument()
  })

  it('keeps the call to action when a stamp cannot be read at all', async () => {
    /**
     * THE FAIL-SAFE DIRECTION, which is otherwise unfalsifiable: every other
     * fixture in this file carries two parseable timestamps, so the NaN guard
     * in `expiryState` sits on a branch no test reaches and could be deleted —
     * or inverted to `resolved` — without reddening anything.
     *
     * It matters which way it fails. `Date.parse` answers NaN and every
     * comparison against NaN is false, so the arithmetic's own instinct is to
     * report `resolved` — to say the problem is fixed. A credential whose auth
     * has died and whose row says nothing is invisible until a run fails.
     */
    renderApp('/admin?section=credentials', [
      credentials(
        credential({
          id: 'c-odd',
          name: 'unparseable',
          kind: 'agent_auth_file',
          fingerprint: FINGERPRINT,
          provided_at: 'not a timestamp',
          last_auth_expired_at: '2026-08-20T00:00:00Z',
        }),
      ),
    ])

    const odd = await row('c-odd')
    expect(odd.getByText(/rotate the secret/i)).toBeInTheDocument()
    expect(odd.queryByText(/a secret has been supplied since/i)).not.toBeInTheDocument()
  })

  it('renders the secret version and the last-used stamp, including its absence', async () => {
    renderApp('/admin?section=credentials', [
      credentials(
        credential({
          id: 'c-used',
          name: 'used',
          kind: 'model_key',
          secret_version: 3,
          last_used_at: '2026-08-21T09:00:00Z',
        }),
        credential({ id: 'c-fresh', name: 'fresh', kind: 'model_key' }),
      ),
    ])

    const used = await row('c-used')
    expect(used.getByText(/secret version 3/i)).toBeInTheDocument()
    expect(used.getByText(/2026-08-21T09:00:00Z/)).toBeInTheDocument()
    /**
     * "Never used" IS RENDERED rather than the line being dropped. A row with
     * no stamp and no line is indistinguishable from a row whose line failed —
     * and a credential that has never been used is a real finding, the
     * signature of one stored against the wrong repo.
     */
    expect((await row('c-fresh')).getByText(/never used/i)).toBeInTheDocument()
  })

  it('stores a credential without ever showing the secret back', async () => {
    const posted = vi.fn()
    let listed = 0
    const SECRET = 'sk-ant-oat01-super-secret-value'
    renderApp('/admin?section=credentials', [
      http.post('/api/v1/credentials', async ({ request }) => {
        posted(await request.json())
        return HttpResponse.json(credential({ id: 'c-new', name: 'new-key', kind: 'model_key' }), {
          status: 201,
        })
      }),
      http.get('/api/v1/credentials', () => {
        listed += 1
        return HttpResponse.json({
          credentials:
            listed === 1
              ? [sub]
              : [sub, credential({ id: 'c-new', name: 'new-key', kind: 'model_key' })],
        })
      }),
    ])

    await userEvent.click(await screen.findByRole('button', { name: 'Add credential' }))
    await userEvent.type(await screen.findByLabelText('Name'), 'new-key')
    /**
     * NO DEFAULT KIND, asserted before one is picked. The four kinds are not
     * interchangeable and kind is immutable afterwards, so an unattended submit
     * must not store a secret under a kind nobody chose.
     */
    expect(screen.getByLabelText('Kind')).toHaveValue('')
    await userEvent.selectOptions(screen.getByLabelText('Kind'), 'model_key')
    await userEvent.type(screen.getByLabelText('Secret'), SECRET)
    await userEvent.click(screen.getByRole('button', { name: 'Store credential' }))

    await expect
      .poll(() => posted.mock.calls[0]?.[0])
      .toEqual({ name: 'new-key', kind: 'model_key', secret: SECRET })
    // The invalidation, read off the rendered list rather than a request count.
    expect(
      await within(await screen.findByTestId('credential-rows')).findByText('new-key'),
    ).toBeInTheDocument()

    /**
     * THE SECRET IS GONE FROM THE DOCUMENT. The response cannot carry it —
     * `CredentialResponse` has no such field — so what this catches is the FORM
     * echoing what was typed: a sheet left mounted after a successful submit
     * keeps the secret on screen behind a dialog nobody closed. Asserted after
     * the list has refreshed, so the sheet has had every chance to still be up.
     */
    await expect.poll(() => document.body.textContent).not.toContain(SECRET)
  })

  it('rotates a secret and refreshes the list', async () => {
    const put = vi.fn()
    let listed = 0
    const ROTATED = '{"tokens":{"refresh_token":"rt-2"}}'
    renderApp('/admin?section=credentials', [
      http.put('/api/v1/credentials/:id/secret', async ({ params, request }) => {
        put(params.id, await request.json())
        return HttpResponse.json(
          credential({
            id: 'c-auth',
            name: 'codex-sub',
            kind: 'agent_auth_file',
            secret_version: 2,
          }),
        )
      }),
      http.get('/api/v1/credentials', () => {
        listed += 1
        return HttpResponse.json({
          credentials: [
            credential({
              id: 'c-auth',
              name: 'codex-sub',
              kind: 'agent_auth_file',
              fingerprint: FINGERPRINT,
              secret_version: listed === 1 ? 1 : 2,
            }),
          ],
        })
      }),
    ])

    await userEvent.click(await screen.findByRole('button', { name: 'Rotate codex-sub' }))
    await typeText(await screen.findByLabelText(/new secret/i), ROTATED)
    await userEvent.click(screen.getByRole('button', { name: 'Replace secret' }))

    await expect.poll(() => put.mock.calls[0]).toEqual(['c-auth', { secret: ROTATED }])
    /**
     * THE INVALIDATION IS READ OFF THE VERSION, which is the field a rotation
     * actually changes. Asserting the row still exists would pass without any
     * refetch at all.
     */
    expect(await (await row('c-auth')).findByText(/secret version 2/i)).toBeInTheDocument()
    await expect.poll(() => document.body.textContent).not.toContain(ROTATED)
  })

  it('renders a blocked delete in the server’s words, not the project-grant sentence', async () => {
    renderApp('/admin?section=credentials', [
      credentials(sub),
      http.delete('/api/v1/credentials/:id', () =>
        problem(409, 'Conflict', 'credential is referenced by a repo or harness'),
      ),
    ])

    await userEvent.click(await screen.findByRole('button', { name: 'Delete claude-max' }))

    const alert = await screen.findByRole('alert')
    expect(
      within(alert).getByText('credential is referenced by a repo or harness'),
    ).toBeInTheDocument()
    expect(within(alert).getByText('cid-cred')).toBeInTheDocument()
    /**
     * THE MUTANT THIS KILLS routes the refusal through `ApiErrorPanel`, whose
     * 403 sentence is about asking an admin for a project grant. It renders an
     * alert too, so a test that only asserted one appeared passes either way.
     */
    expect(alert.textContent).not.toMatch(/grant/i)
  })

  it('answers a member with the refusal the server actually sent', async () => {
    renderApp('/admin?section=credentials', [
      http.get('/api/v1/credentials', () => problem(403, 'Forbidden', 'admin role required')),
    ])

    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText('admin role required')).toBeInTheDocument()
    expect(alert.textContent).not.toMatch(/grant/i)
  })
})
