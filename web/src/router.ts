import { createRouter } from '@tanstack/react-router'
import { onUnauthenticated } from './api/client'
import { queryClient } from './api/query-client'
import { routeTree } from './routeTree.gen'

export const router = createRouter({
  routeTree,
  context: { queryClient },
  defaultPreload: 'intent',
})

// A bare module cannot navigate; the router can. This is the other half of the
// 401 interceptor — the exemption list decides WHETHER, this decides WHERE.
onUnauthenticated(() => {
  void router.navigate({ to: '/login' })
})

// WITHOUT THIS BLOCK the router's type safety does not exist: tsc passes and
// every `to` accepts an arbitrary string. It is invisible in every other signal
// CI produces, which is why tools/mutants/bad-route-to.tsx exists.
declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
