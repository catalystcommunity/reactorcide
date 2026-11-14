import './styles/layers.css'
import '@catalystcommunity/design-tokens/src/tokens.css'
import './styles/tokens.css'
import './styles/base.css'
import './components/ui.css'
import { defineZest } from './ui/zest.ts'
import { bootstrapTheme } from './lib/theme.ts'
import { loadSession } from './lib/session.ts'
import { routeLinks, startRouter } from './ui/router.ts'
import { storeStats } from './store/index.ts'
import './App.ts'

defineZest()
bootstrapTheme()

const root = document.getElementById('root')
if (!root) throw new Error('#root is missing from index.html')

startRouter()
routeLinks(document)
void loadSession()
root.append(document.createElement('rc-app'))

if (import.meta.env.DEV) {
  // A console handle for the store's bookkeeping. The leak test asserts the
  // same numbers; this is for when something feels heavy in a real session.
  ;(window as unknown as Record<string, unknown>).__reactorcideStore = storeStats
}
