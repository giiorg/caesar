import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { StreamStatusProvider, useStreamStatus } from './stream-status'

function Probe() {
  const { connection, scopeEmpty } = useStreamStatus()
  return <p>{`${connection}/${String(scopeEmpty)}`}</p>
}

describe('useStreamStatus outside a provider', () => {
  it('never claims the scope is empty, and never claims the stream is broken', () => {
    // The defaults are a DECISION, not a formality, and this is the only test
    // that can reach them: every real consumer renders inside the provider, so
    // the values below are otherwise unfalsifiable — a context defaulting to
    // `scopeEmpty: true` would tell a user with a perfectly good project that
    // nothing is shared with them, and no other test in the tree would notice.
    //
    // 'live' means "nothing has been said yet" here for the same reason it does
    // in `useAttentionStream`: it renders no banner. The two must agree, or a
    // component reads a different not-yet-known from the context than from the
    // hook.
    render(<Probe />)
    expect(screen.getByText('live/false')).toBeInTheDocument()
  })

  it('publishes what the provider is given', () => {
    render(
      <StreamStatusProvider value={{ connection: 'stopped', scopeEmpty: true }}>
        <Probe />
      </StreamStatusProvider>,
    )
    expect(screen.getByText('stopped/true')).toBeInTheDocument()
  })
})
