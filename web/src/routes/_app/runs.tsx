import { createFileRoute } from '@tanstack/react-router'
import { StubPanel } from '../../ui/stub-panel'

export const Route = createFileRoute('/_app/runs')({ component: Runs })

function Runs() {
  return <StubPanel plan="Plan 12" what="Runs" />
}
