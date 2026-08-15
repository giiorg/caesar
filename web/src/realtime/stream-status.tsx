import { createContext, useContext } from 'react'
import type { ConnectionState } from './connection-state'

export type StreamStatus = { connection: ConnectionState; scopeEmpty: boolean }

/**
 * THE DEFAULTS DESCRIBE A TAB THAT HAS NOT CONNECTED YET, NOT AN ERROR.
 *
 * `scopeEmpty: false` is the load-bearing half: "your scope is empty" is a
 * claim about the user's grants, and a consumer rendered outside the provider
 * has no business making it. Defaulting the other way would tell someone with
 * a perfectly good project that nothing is shared with them.
 *
 * `connection: 'live'` matches `useAttentionStream`'s initial state and means
 * the same thing there — nothing has been said yet, so say nothing. See the
 * hook for why silence is the honest rendering of that and not a lie. The two
 * values must agree: a component that reads a different "not yet known" from
 * the context than from the hook is reading two answers to one question.
 */
const StreamStatusContext = createContext<StreamStatus>({
  connection: 'live',
  scopeEmpty: false,
})

export const StreamStatusProvider = StreamStatusContext.Provider
export const useStreamStatus = (): StreamStatus => useContext(StreamStatusContext)
