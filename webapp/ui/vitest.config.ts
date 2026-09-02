import { defineConfig } from 'vitest/config'
import solid from 'vite-plugin-solid'
import { resolve } from 'node:path'

// Deliberately standalone rather than mergeConfig(viteConfig, ...).
//
// mergeConfig applies the solid() plugin twice, which loads two copies of
// solid-js. The symptoms are a "multiple instances of Solid" warning and a
// spurious "computations created outside a createRoot will never be disposed"
// on every render -- and the tests still pass, so it is easy to miss.
export default defineConfig({
  plugins: [solid()],
  resolve: {
    conditions: ['development', 'browser'],
    alias: { '~': resolve(__dirname, 'src') },
  },
  test: {
    environment: 'jsdom',
    setupFiles: ['./vitest.setup.ts'],
    // @solidjs/testing-library registers its afterEach(cleanup) only when
    // vitest runs with globals: true. vitest.setup.ts does it explicitly
    // instead, so this stays off.
    globals: false,
  },
})
