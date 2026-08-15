import { createFileRoute } from '@tanstack/react-router'
import { StubPanel } from '../../ui/stub-panel'

/** Task 11 of THIS plan replaces the body — the stub is one task long. */
export const Route = createFileRoute('/_app/approvals')({ component: Approvals })

function Approvals() {
  return <StubPanel plan="Plan 11" what="Approvals" />
}
