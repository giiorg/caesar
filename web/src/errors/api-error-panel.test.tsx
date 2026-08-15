import { render, screen } from '@testing-library/react'
import { expect, it } from 'vitest'
import { ApiError } from '../api/errors'
import { ApiErrorPanel } from './api-error-panel'

// A 500's `detail` is deliberately vague, so the id is the only thread back to
// a log line — which is exactly why both statuses are asserted, not just 403.
const base = {
  type: '',
  title: 'Something failed',
  detail: 'detail',
  correlationId: 'cid',
  fields: [],
}

it.each([403, 500])('renders the correlation id for a %s', (status) => {
  render(<ApiErrorPanel error={new ApiError({ ...base, status, correlationId: 'cid-42' })} />)
  expect(screen.getByText('cid-42')).toBeInTheDocument()
})

it('explains a 403 as a missing grant, not a failure', () => {
  render(<ApiErrorPanel error={new ApiError({ ...base, status: 403 })} />)
  expect(screen.getByRole('alert')).toHaveTextContent(/grant/i)
})

it('rethrows what it does not own', () => {
  expect(() => render(<ApiErrorPanel error={new TypeError('boom')} />)).toThrow('boom')
})
