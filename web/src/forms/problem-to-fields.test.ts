import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api/errors'
import { applyProblemToForm } from './problem-to-fields'

const err = (init: Partial<ConstructorParameters<typeof ApiError>[0]>) =>
  new ApiError({
    type: '',
    title: 'Invalid request',
    status: 400,
    detail: '',
    correlationId: 'cid',
    fields: [],
    ...init,
  })

describe('applyProblemToForm', () => {
  it('maps field errors by FLAT NAME, not by JSON pointer', () => {
    // xerrors.FieldError is {field, message} and the name is a plain
    // identifier. A pointer-shaped mapper ("/email") would match nothing on
    // every real validation failure the backend produces.
    const setError = vi.fn()
    const out = applyProblemToForm(
      err({ fields: [{ field: 'email', message: 'must be an email' }] }),
      setError,
      ['email', 'password'],
    )
    expect(setError).toHaveBeenCalledWith('email', { type: 'server', message: 'must be an email' })
    expect(out.formError).toBeNull()
  })

  it('falls back to a form-level error when the problem names no field', () => {
    // The 401 login really returns for bad credentials carries NO field at
    // all, so this is the primary path, not the edge.
    const setError = vi.fn()
    const out = applyProblemToForm(
      err({ status: 401, detail: 'invalid email or password' }),
      setError,
      ['email', 'password'],
    )
    expect(setError).not.toHaveBeenCalled()
    expect(out.formError).toBe('invalid email or password')
  })

  it('routes an unknown field name to the form level rather than dropping it', () => {
    const setError = vi.fn()
    const out = applyProblemToForm(
      err({ fields: [{ field: 'tenant', message: 'no such tenant' }] }),
      setError,
      ['email', 'password'],
    )
    expect(setError).not.toHaveBeenCalled()
    expect(out.formError).toContain('no such tenant')
  })

  // The three tests above are the plan's. The three below exist because the
  // plan's own implementation has branches none of them can distinguish — see
  // the notes on each.

  it('routes what it can AND still surfaces what it cannot, in one problem', () => {
    // The mixed problem is the branch the plan's three tests never reach: every
    // one of them has fields that are either all routable or all unroutable.
    // Without this, moving the unroutable early-return below the `routed` check
    // — which silently drops the unplaceable message whenever anything else
    // routed — passes the whole suite.
    const setError = vi.fn()
    const out = applyProblemToForm(
      err({
        fields: [
          { field: 'email', message: 'must be an email' },
          { field: 'tenant', message: 'no such tenant' },
        ],
      }),
      setError,
      ['email', 'password'],
    )
    expect(setError).toHaveBeenCalledWith('email', { type: 'server', message: 'must be an email' })
    expect(out.formError).toContain('no such tenant')
  })

  it('joins several unplaceable messages instead of showing only one', () => {
    const setError = vi.fn()
    const out = applyProblemToForm(
      err({
        fields: [
          { field: 'tenant', message: 'no such tenant' },
          { field: 'region', message: 'no such region' },
        ],
      }),
      setError,
      ['email', 'password'],
    )
    expect(setError).not.toHaveBeenCalled()
    expect(out.formError).toBe('no such tenant; no such region')
  })

  it('falls back to the title when the fieldless problem carries no detail', () => {
    // `detail !== '' ? detail : title` — with detail always populated in the
    // plan's fieldless test, `formError: err.detail` passes it unchanged and
    // the user gets an empty error box on every problem the server sends
    // without a detail.
    const setError = vi.fn()
    const out = applyProblemToForm(
      err({ status: 500, title: 'Server error', detail: '' }),
      setError,
      ['email', 'password'],
    )
    expect(setError).not.toHaveBeenCalled()
    expect(out.formError).toBe('Server error')
  })
})
