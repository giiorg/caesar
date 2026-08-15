import tailwindcss from '@tailwindcss/vite'
import { tanstackRouter } from '@tanstack/router-plugin/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// COMMIT is the external oracle §8 needs: the workflow knows the SHA
// independently of the image, so a stale build cannot grade itself as fresh.
// "dev" is the local default and is never what CI publishes.
const commit = process.env.CAESAR_COMMIT ?? 'dev'

export default defineConfig({
  plugins: [tanstackRouter({ target: 'react', autoCodeSplitting: true }), react(), tailwindcss()],
  define: { __CAESAR_COMMIT__: JSON.stringify(commit) },
  resolve: { alias: { '@': new URL('./src', import.meta.url).pathname } },
  build: {
    // //go:embed cannot reach outside its own package, so the bundle is built
    // straight into the Go package that embeds it. One artifact, one location,
    // no copy step to go stale.
    outDir: '../backend/internal/webui/dist',
    emptyOutDir: true,
  },
  server: {
    // __Host- cookies require Secure, and browsers treat http://localhost as a
    // secure context — but only if the session stays same-origin. A dev setup
    // that talked to :8080 directly would silently drop the cookie. Task 12
    // asserts this proxy exists.
    proxy: { '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false } },
  },
})
