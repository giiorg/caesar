import type { QueryClient } from '@tanstack/react-query'
import { createRootRouteWithContext, Outlet } from '@tanstack/react-router'

// The context type is what makes `context.queryClient` in a loader a typed
// dereference rather than a cast. Every route's loader inherits it, and the
// `_app` loader is the first consumer.
export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: RootLayout,
})

function RootLayout() {
  return <Outlet />
}
