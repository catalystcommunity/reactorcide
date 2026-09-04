/* @refresh reload */
import { render } from 'solid-js/web'
import { Router } from '@solidjs/router'
import { App } from './App.tsx'
import { routes } from './routes/index.tsx'
import { storeStats } from './store/index.ts'
import './styles/tokens.css'
import './styles/base.css'
import './components/ui.css'

const root = document.getElementById('root')
if (!root) throw new Error('#root is missing from index.html')

// The SPA is mounted under /app/ by the Go binary, so the router has to know
// that prefix or every link would resolve to the wrong path.
render(() => <Router base="/app" root={App}>{routes}</Router>, root)

if (import.meta.env.DEV) {
  // A console handle for the store's bookkeeping. The leak test asserts the
  // same numbers; this is for when something feels heavy in a real session.
  ;(window as unknown as Record<string, unknown>).__reactorcideStore = storeStats
}
