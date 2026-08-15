import { createFileRoute } from '@tanstack/react-router'

export const Route = createFileRoute('/_app/')({ component: Home })

function Home() {
  return <p>caesar</p>
}
