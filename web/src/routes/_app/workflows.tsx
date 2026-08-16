import { createFileRoute } from '@tanstack/react-router'
import { StubPanel } from '../../ui/stub-panel'

export const Route = createFileRoute('/_app/workflows')({ component: Workflows })

function Workflows() {
  return <StubPanel plan="Plan 12" what="Workflows" />
}
