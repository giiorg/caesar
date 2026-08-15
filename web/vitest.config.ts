import react from '@vitejs/plugin-react'
import { configDefaults, defineConfig } from 'vitest/config'

const alias = { '@': new URL('./src', import.meta.url).pathname }

// Two project shapes, because they need genuinely different worlds:
//
//   node — the pure/transport half. Build tooling and anything whose subject is
//          the filesystem or a byte stream. No DOM globals at all, so a module
//          that quietly reaches for `document` fails here instead of passing on
//          a global it should never have seen.
//   dom  — the component half. jsdom, the React plugin (JSX transform + Fast
//          Refresh's runtime shims) and the shared setup file.
//
// The split is by directory rather than by file extension on purpose: the DOM
// half has `.ts` tests too (hooks with no JSX), so a `.tsx`-means-DOM rule would
// silently run them without a document.
export default defineConfig({
  test: {
    projects: [
      {
        resolve: { alias },
        test: {
          name: 'node',
          environment: 'node',
          include: ['tools/**/*.test.ts'],
          // Task 3's arch fixtures are deliberate non-tests: one of them is
          // NAMED `helper.test.ts` precisely to prove the import-graph checker
          // does not count a test as a consumer, so it can never be renamed
          // away. vitest hard-fails on a collected file that declares no suite
          // ("No test suite found in file …"), so without this exclude
          // `task web:test` goes red the moment Task 3 lands — MEASURED, with
          // that exact message. Spread `configDefaults.exclude` rather than
          // writing a literal: setting `exclude` REPLACES vitest's defaults
          // (today `**/node_modules/**` and `**/.git/**`), it does not extend
          // them.
          exclude: [...configDefaults.exclude, 'tools/**/fixtures/**'],
          globals: false,
          restoreMocks: true,
          unstubGlobals: true,
        },
      },
      {
        plugins: [react()],
        resolve: { alias },
        test: {
          name: 'dom',
          environment: 'jsdom',
          // Vitest stubs CSS imports to the empty string by default, and the
          // stub catches `?raw` too — MEASURED: `import css from
          // './theme-dark.css?raw'` yielded `''` with this option absent, so a
          // test whose subject is the CSS silently asserted against nothing.
          // Narrowed to the two theme files rather than `css: true`, because
          // the blanket form would also put tokens.css — and through it the
          // whole `@import 'tailwindcss'` build — on the critical path of every
          // component test from Task 8 onward.
          // The pattern is matched against the full module id, which still
          // carries the `?raw` query — MEASURED: anchoring with `\.css$`
          // matched nothing and handed the test an empty string again.
          css: { include: [/src\/styles\/theme-\w+\.css(\?|$)/] },
          include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
          setupFiles: ['./src/test/setup.ts'],
          globals: false,
          restoreMocks: true,
          // `restoreMocks` does not undo `vi.stubGlobal`, so without this a
          // stubbed matchMedia would leak from the test that stubbed it into
          // every later test in the file, defeating setup.ts's default.
          unstubGlobals: true,
        },
      },
    ],
  },
})
