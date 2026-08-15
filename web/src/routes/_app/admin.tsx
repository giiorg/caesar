import { createFileRoute } from '@tanstack/react-router'
import { StubPanel } from '../../ui/stub-panel'

export const Route = createFileRoute('/_app/admin')({ component: Admin })

function Admin() {
  return <StubPanel plan="Plan 12" what="Admin" />
}
