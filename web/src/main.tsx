import './styles/app.css'
import { QueryClientProvider } from '@tanstack/react-query'
import { RouterProvider } from '@tanstack/react-router'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { queryClient } from './api/query-client'
import { router } from './router'

const root = document.getElementById('root')
if (root === null) throw new Error('web: #root is missing from index.html')
// The QueryClientProvider is OUTSIDE the router on purpose: route loaders and
// beforeLoad guards run before any route component renders, so the cache has to
// exist above the router, not inside one of its routes.
createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
)
