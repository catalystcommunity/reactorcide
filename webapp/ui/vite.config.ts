import { defineConfig, type Plugin } from 'vite'
import solid from 'vite-plugin-solid'
import { resolve } from 'node:path'
import { writeFileSync } from 'node:fs'

const EMBED_DIR = resolve(__dirname, '../internal/embedui/dist')

/**
 * Re-creates the .gitkeep that `emptyOutDir` deletes.
 *
 * The Go binary embeds this directory with `go:embed all:dist`, which is a
 * COMPILE ERROR when nothing matches -- so a checkout with an empty dist does
 * not build at all. Only .gitkeep is committed (the build output never is,
 * since index.html names hashed files that change every build), and every
 * build wipes it.
 *
 * This lives in the vite config rather than in ./tools because there are three
 * build paths -- ./tools build-ui, the Docker image build, and the CI plugins
 * -- and a fix in only one of them is a fix that silently does not apply. It
 * already broke a production release exactly that way.
 */
function preserveEmbedGitkeep(): Plugin {
  return {
    name: 'reactorcide:preserve-embed-gitkeep',
    apply: 'build',
    closeBundle() {
      writeFileSync(resolve(EMBED_DIR, '.gitkeep'), '')
    },
  }
}

export default defineConfig({
  plugins: [solid(), preserveEmbedGitkeep()],
  // The SPA is served from /app/ by the Go binary, so every asset URL it emits
  // must be absolute under that prefix. A relative base would break on deep
  // links like /app/workflows/01ABC, where the browser would resolve assets
  // against /app/workflows/.
  base: '/app/',
  resolve: { alias: { '~': resolve(__dirname, 'src') } },
  build: {
    // Straight into the Go embed directory. There is no copy step to forget.
    outDir: EMBED_DIR,
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
