import { createFileRoute } from '@tanstack/react-router'
import { StubPanel } from '../../ui/stub-panel'

export const Route = createFileRoute('/_app/repos')({ component: Repos })

function Repos() {
  return <StubPanel plan="Plan 12" what="Repos" />
}
