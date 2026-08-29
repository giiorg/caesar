import { Link } from '@tanstack/react-router'
import { type ReactNode, useEffect, useState } from 'react'
import type { components } from '../api/schema'
import type { ConnectionState } from '../realtime/connection-state'
import { CommandPalette } from './command-palette'
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
  const [paletteOpen, setPaletteOpen] = useState(false)

  /**
   * ONE window-level listener, removed on unmount.
   *
   * `metaKey` AND `ctrlKey`, because the same chord is ⌘K on a Mac and Ctrl+K
   * everywhere else and this is one operator's tool on whichever machine is to
   * hand. `preventDefault` because ⌘K is the browser's own focus-the-search
   * shortcut in Chrome and Firefox — without it the address bar takes the
   * keystroke and the palette opens behind it.
   */
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() !== 'k' || !(e.metaKey || e.ctrlKey)) return
      e.preventDefault()
      setPaletteOpen(true)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [])

  return (
    <div className="bg-ground text-text flex min-h-dvh flex-col">
      <header className="border-border bg-surface flex items-center justify-between border-b px-4 py-2">
        {/* The wordmark is a link Home — the affordance every web surface has
            trained people to expect. In the <header>, so it adds no second
            navigation landmark. */}
        <Link to="/" className="font-mono text-sm">
          caesar
        </Link>
        <div className="flex items-center gap-2">
          {/* THE VISIBLE HALF OF ⌘K, and it is in the top bar because that is
              where the locked decision puts it: "desktop = full sidebar + top
              bar with ⌘K" (decisions §7). A chord with no affordance is a
              feature nobody was told about — and the plan's file list asked for
              the listener alone.

              `md:flex` ONLY. The phone shell is the five tabs and has no room
              for chrome that duplicates them; a keyboard chord on a device with
              no keyboard is not a control worth the width. The listener stays
              mounted at every viewport, so an attached keyboard still works. */}
          <button
            type="button"
            onClick={() => setPaletteOpen(true)}
            className="border-border text-text-muted hidden items-center rounded-md border px-2 py-1 font-mono text-xs md:flex"
          >
            ⌘K
          </button>
          <span className="text-text-muted font-mono text-xs">{props.me.user.email}</span>
          <ThemeToggle />
        </div>
      </header>
      <CommandPalette open={paletteOpen} onClose={() => setPaletteOpen(false)} />
      <ConnectionIndicator state={props.connection ?? 'live'} />
      {/* DOM ORDER IS header, indicator, main, nav — deliberately, and it does
          not change with the viewport. On a phone that puts the tab bar last in
          the tab sequence, where a thumb-zone control belongs; on a wide
          viewport `md:order-first` on the <nav> moves the rail to the left of
          <main> visually, which is a paint decision and not a reading order. */}
      <div className="flex flex-1 flex-col md:flex-row">
        {/* `pb-tab-bar` is the other half of the fixed bar: pinning alone would
            overlay the last screenful of every page. `md:pb-4` gives it back on
            a viewport where the bar is display:none and the nav is a rail. */}
        <main className="flex-1 p-4 pb-tab-bar md:pb-4">{props.children}</main>
        <Nav approvalCount={props.approvalCount ?? 0} />
      </div>
    </div>
  )
}
