import tailwindcss from '@tailwindcss/vite'
import { tanstackRouter } from '@tanstack/router-plugin/vite'
import react from '@vitejs/plugin-react'
import { defineConfig, type Plugin } from 'vite'

// COMMIT is the external oracle §8 needs: the workflow knows the SHA
// independently of the image, so a stale build cannot grade itself as fresh.
// "dev" is the local default and is never what CI publishes.
const commit = process.env.CAESAR_COMMIT ?? 'dev'

// The stamp has to be EMITTED, not merely DEFINED — and that distinction was
// MEASURED, not reasoned about. `define` alone baked nothing at all into the
// bundle: `__CAESAR_COMMIT__` is referenced by no emitted source (it is only
// DECLARED, in src/vite-env.d.ts), so the substitution ran at zero call sites.
//
//   CAESAR_COMMIT=DEADBEEFCAFE0001 ./node_modules/.bin/vite build
//   grep -rl DEADBEEFCAFE0001 ../backend/internal/webui/dist/   -> exit 1, no match
//   grep -rl index-DOmNgoZz     ../backend/internal/webui/dist/ -> 3 files (the
//                                                                  grep itself works)
//
// §8's image assertion would therefore have been comparing the workflow's SHA
// against an oracle nothing ever wrote, and nothing anywhere would have gone
// red. So the stamp is injected into index.html as a meta tag: the one artifact
// an image assertion can read over plain HTTP without parsing a content-hashed
// bundle, and the one that changes on every rebuild.
//
// backend/internal/webui/ui_embed_test.go's TestServedIndexCarriesTheBuildStamp
// is the guard — delete this plugin and it goes red.
//
// `define` is kept as well, so a future module that wants the value in
// JavaScript has it; it is dead today and vite-env.d.ts's declaration is the
// only reason it type-checks.
const commitStamp: Plugin = {
  name: 'caesar-commit-stamp',
  transformIndexHtml: () => [
    { tag: 'meta', attrs: { name: 'caesar-commit', content: commit }, injectTo: 'head' },
  ],
}

export default defineConfig({
  plugins: [
    tanstackRouter({ target: 'react', autoCodeSplitting: true }),
    react(),
    tailwindcss(),
    commitStamp,
  ],
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
    // that talked to :8080 directly would silently drop the cookie, so /api is
    // proxied through the Vite origin and the whole dev session stays
    // same-origin.
    //
    // STATUS: the COOKIE claim above is REASONED, NOT MEASURED. Nothing in any
    // automated lane exercises it — it needs a real browser's cookie jar, and
    // no test here has one. It follows from the __Host- prefix rules and the
    // secure-context treatment of localhost; it is not an observation and must
    // not be quoted as one.
    //
    // THE ORIGIN PROBLEM IS MEASURED, AND THE PROXY ALONE DOES NOT SOLVE IT.
    // This proxy does not rewrite the browser's Origin — changeOrigin rewrites
    // Host, not Origin — so the backend sees `Origin: http://localhost:5173`.
    // MEASURED with this repo's own vite 8.2.1 against an echo server:
    //
    //   proxy: { '/api': { target: 'http://127.0.0.1:8099', changeOrigin: false } }
    //   curl -X POST -H 'Origin: http://localhost:5199' \
    //        http://localhost:5199/api/v1/auth/login          -> 200 (proxied)
    //   the echo server logged:
    //     ECHO POST /api/v1/auth/login Origin=http://localhost:5199 \
    //          Host=localhost:5199
    //
    // httpx.CheckOrigin rejects an unsafe method whose PRESENT Origin differs
    // from the configured app origin, and backend/caesar.dev.json sets no
    // app_origin, so config.go defaults it to http://localhost:8080. MEASURED
    // against the real Routes chain:
    //
    //   POST /api/v1/auth/login Origin="http://localhost:5173" -> 403
    //     {"type":"/errors/forbidden","title":"Forbidden","status":403,
    //      "detail":"cross-origin request rejected"}
    //   POST /api/v1/auth/login Origin="http://localhost:8080" -> passes
    //   POST /api/v1/auth/login Origin=""                      -> passes
    //
    // So the login POST is refused before the cookie question can even arise,
    // and an operator who does not know this will misread the 403 as a
    // credential or cookie problem.
    //
    // The backend has ONE app origin, so the two dev loops need different
    // configs and no single default serves both:
    //   - Vite loop (this file):    app_origin must be http://localhost:5173
    //   - Embedded loop (task
    //     backend:test:embedui's
    //     bundle served from :8080): app_origin must be http://localhost:8080
    // Which one caesar.dev.json should carry is not this file's call; today it
    // carries neither explicitly and therefore defaults to the second.
    //
    // To run the Vite loop, an operator must:
    //   1. task db:up
    //   2. Add "app_origin": "http://localhost:5173" to the "server" block of
    //      backend/caesar.dev.json. WITHOUT THIS STEP, STEP 4 FAILS 403.
    //   3. cd backend && go run ./cmd/caesar serve -config caesar.dev.json
    //   4. cd web && ./node_modules/.bin/vite   (never npx — see Taskfile)
    //   5. In a browser at the VITE origin: log in, then reload the page.
    //      EXPECT: the session survives the reload.
    //   6. Delete this proxy block, point the client at http://127.0.0.1:8080
    //      directly, repeat step 5.
    //      EXPECT: the cookie is DROPPED and the reload lands back on login.
    //      That failure is the thing this proxy prevents, and it is silent —
    //      if step 6 does NOT drop the cookie, this block's cookie
    //      justification is wrong and should be rewritten, not kept "just in
    //      case".
    //   7. Restore this block.
    // Until someone records the outcome of step 6 here, treat the cookie claim
    // as unproven.
    proxy: { '/api': { target: 'http://127.0.0.1:8080', changeOrigin: false } },
  },
})
