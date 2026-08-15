import { createFileRoute } from '@tanstack/react-router'
import { StubPanel } from '../../ui/stub-panel'

/**
 * DELIBERATELY COMPONENT-ONLY, and it must stay that way while Plan 12 is
 * unbuilt. `_app.test.tsx`'s first test navigates here precisely because this
 * route issues no request of its own: that is the case a redirect-on-401
 * design cannot catch, since a stub makes nothing for the server to reject.
 * Giving it a loader would make the route fetch something and destroy the
 * property that guard test exists to prove.
 */
export const Route = createFileRoute('/_app/tasks')({ component: Tasks })

function Tasks() {
  return <StubPanel plan="Plan 12" what="Tasks" />
}
