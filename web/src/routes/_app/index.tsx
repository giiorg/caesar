import { createFileRoute } from '@tanstack/react-router'
import { StubPanel } from '../../ui/stub-panel'

/**
 * Home is a REAL screen in the V1 page map — decisions §7 (Home A) asks for a
 * one-line situation résumé, "Needs you" gate cards with inline approve/reject,
 * running cards with live phase dots, and the assistant ask-bar. Row 12 builds
 * it.
 *
 * Task 7 left `<p>caesar</p>` here so the layout had something to render. That
 * is exactly the "plausible empty content" StubPanel exists to prevent: a
 * landing page showing a wordmark and nothing else is indistinguishable, to
 * the person looking at it, from Home having loaded and found nothing to say.
 * The plan's Files list does not name this route; it should, and the rule it
 * states about stubs is the reason.
 */
export const Route = createFileRoute('/_app/')({ component: Home })

function Home() {
  return <StubPanel plan="Plan 12" what="Home" />
}
