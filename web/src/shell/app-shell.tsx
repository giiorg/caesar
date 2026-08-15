import type { ReactNode } from 'react'
import type { components } from '../api/schema'

export type Me = components['schemas']['ApiMeResponse']

/**
 * PLACEHOLDER. Task 8 replaces this file wholesale with the real Shell A —
 * the nav, the theme toggle and the connection indicator.
 *
 * It exists now because Task 7's guard tests assert on the presence of the
 * shell (`role="navigation"`) versus the login form, and a layout route whose
 * component cannot resolve fails in a way that reads as the guard being
 * broken. The `me` prop is the seam Task 8 inherits: the session is resolved
 * by the `_app` loader and handed down, so no shell component ever fetches it.
 */
export function AppShell({ children }: { me: Me; children: ReactNode }) {
  return (
    <div className="min-h-dvh">
      <nav aria-label="Main" />
      {children}
    </div>
  )
}
