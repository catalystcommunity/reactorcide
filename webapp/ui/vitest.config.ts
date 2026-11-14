import { defineConfig } from 'vitest/config'
import { resolve } from 'node:path'

// Standalone rather than mergeConfig(viteConfig, ...): the build config writes
// into the Go embed directory and sets a dev proxy, neither of which a test
// needs.
export default defineConfig({
  resolve: {
    conditions: ['development', 'browser'],
    alias: { '~': resolve(__dirname, 'src') },
  },
  test: {
    environment: 'happy-dom',
    setupFiles: ['./vitest.setup.ts'],
    include: ['src/**/*.test.ts'],
    globals: false,
  },
})
