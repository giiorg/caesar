import type { ReactNode } from 'react'
import type { components } from '../api/schema'
import type { ConnectionState } from '../realtime/connection-state'
import { ConnectionIndicator } from './connection-indicator'
import { Nav } from './nav'
import { ThemeToggle } from './theme-toggle'

export type Me = components['schemas']['ApiMeResponse']

/**
 * Shell A (decisions §7). Everything it renders it was HANDED.
 *
 * `me` arrives as a prop because the `_app` loader has already resolved the
 * session — no shell component fetches anything, so the whole chrome renders
 * in a test with no server and no query client.
 *
 * `connection` is a prop for the same reason and one more: `AppLayout` is the
 * single caller and already holds the value. An earlier draft also promised a
 * `useConnectionState()` hook here; two mechanisms for one value means one of
 * them is dead, and the dead one is the one a reader reaches for. Everything
 * BELOW the layout that needs the stream's state reads `useStreamStatus()`
 * (Task 11) — the same context, and the only hook.
 */
export function AppShell(props: {
  me: Me
  children: ReactNode
  connection?: ConnectionState
  approvalCount?: number
}) {
  return (
    <div className="bg-ground text-text flex min-h-dvh flex-col">
      <header className="border-border bg-surface flex items-center justify-between border-b px-4 py-2">
        <span className="font-mono text-sm">caesar</span>
        <div className="flex items-center gap-2">
          <span className="text-text-muted font-mono text-xs">{props.me.user.email}</span>
          <ThemeToggle />
        </div>
      </header>
      <ConnectionIndicator state={props.connection ?? 'live'} />
      <main className="flex-1 p-4">{props.children}</main>
      <Nav approvalCount={props.approvalCount ?? 0} />
    </div>
  )
}
