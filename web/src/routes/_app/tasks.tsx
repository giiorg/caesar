import { createFileRoute } from '@tanstack/react-router'

/**
 * PLACEHOLDER, and deliberately COMPONENT-ONLY. Task 8 creates the real one.
 *
 * `_app.test.tsx` navigates here precisely because this route issues no
 * request of its own: that is the case a redirect-on-401 design cannot catch,
 * since a stub makes nothing for the server to reject. Adding a loader here
 * would make the route fetch something and destroy the property the guard
 * test exists to prove.
 */
export const Route = createFileRoute('/_app/tasks')({ component: Tasks })

function Tasks() {
  return <p>tasks</p>
}
