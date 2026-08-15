import { useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { api, unwrap } from '../api/client'
import { createCacheBridge } from './cache-bridge'
import type { ConnectionState } from './connection-state'
import { openAttentionStream } from './sse-connection'
import type { StreamStatus } from './stream-status'

/**
 * ONE STREAM PER TAB. `AppLayout` is the only caller, and everything below it
 * reads `useStreamStatus()` instead — calling this hook a second time opens a
 * second connection, which is why it is not the thing routes import.
 *
 * THE INITIAL STATE IS 'live', WHICH IS SILENCE AND NOT A CLAIM. The plan
 * started at 'reconnecting', and that puts "this view may be out of date" on
 * screen from the first paint of every single page load, before the transport
 * has said anything at all — while the data underneath it was just fetched by
 * the route loader and is as fresh as it will ever be. The indicator's own
 * argument is the one that decides this: a banner that is always present
 * trains people to stop reading it, and the banner exists to be read on the
 * rare occasion it is true.
 *
 * The exposure that buys is bounded and small. A server that REFUSES the
 * stream fails the fetch and `reconnect()` announces 'reconnecting' in
 * milliseconds; the only case silence covers is a server that accepts the
 * connection and then says nothing, which the transport's own opening deadline
 * (OPEN_TIMEOUT_MS, 20s) converts into the same announcement. Throughout that
 * window REST remains the source of truth and the bounded refresh floor keeps
 * running, so what is on screen is not stale — it is merely not being pushed
 * to.
 */
export function useAttentionStream(): StreamStatus {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [connection, setConnection] = useState<ConnectionState>('live')
  const [scopeEmpty, setScopeEmpty] = useState(false)

  useEffect(() => {
    const bridge = createCacheBridge(qc)
    const conn = openAttentionStream({
      probeMe: async () => unwrap(await api.GET('/api/v1/auth/me')),
      onEvent: (e) => {
        switch (e.type) {
          case 'frame':
            // The frame is a NUDGE. What it says never reaches the cache; see
            // cache-bridge.ts.
            bridge.onFrame(e.frame)
            break
          case 'state':
            setConnection(e.state)
            break
          case 'scope-lost':
            // Sticky by construction, because the transport treats it as
            // terminal: there is no later event that would set it back.
            setScopeEmpty(true)
            break
          case 'session-lost':
            void navigate({ to: '/login' })
            break
        }
      },
    })
    return () => conn.close()
    // `navigate` is in the dependency list because the effect closes over it,
    // and it is only safe there because TanStack Router hands back a stable
    // reference — an unstable one would tear down and reopen the stream on
    // every state change this hook itself causes, i.e. on every connection
    // announcement, forever. That is not an assumption: `_app.test.tsx` counts
    // the connections a mounted app makes across a state change.
  }, [qc, navigate])

  return { connection, scopeEmpty }
}
