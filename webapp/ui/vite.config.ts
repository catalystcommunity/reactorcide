import { defineConfig } from 'vite'
import solid from 'vite-plugin-solid'
import { resolve } from 'node:path'

export default defineConfig({
  plugins: [solid()],
  // The SPA is served from /app/ by the Go binary, so every asset URL it emits
  // must be absolute under that prefix. A relative base would break on deep
  // links like /app/workflows/01ABC, where the browser would resolve assets
  // against /app/workflows/.
  base: '/app/',
  resolve: { alias: { '~': resolve(__dirname, 'src') } },
  build: {
    // Straight into the Go embed directory. There is no copy step to forget.
    outDir: resolve(__dirname, '../internal/embedui/dist'),
    emptyOutDir: true,
    sourcemap: true,
  },
  server: {
    // `npm run dev` proxies the API surface to a locally running webapp so the
    // SPA can be developed with hot reload against a real coordinator.
    proxy: {
      '/app/rpc': 'http://localhost:5080',
      '/app/auth': 'http://localhost:5080',
      '/app/ws': { target: 'ws://localhost:5080', ws: true },
    },
  },
})
