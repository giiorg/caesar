import { createRouter } from '@tanstack/react-router'
import { routeTree } from './routeTree.gen'

export const router = createRouter({ routeTree })

// WITHOUT THIS BLOCK the router's type safety does not exist: tsc passes and
// every `to` accepts an arbitrary string. It is invisible in every other signal
// CI produces, which is why tools/mutants/bad-route-to.tsx exists.
declare module '@tanstack/react-router' {
  interface Register {
    router: typeof router
  }
}
