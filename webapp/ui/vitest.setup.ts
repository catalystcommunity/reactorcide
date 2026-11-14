import '@testing-library/jest-dom/vitest'
import { afterEach, beforeEach } from 'vitest'
import { defineZest } from './src/ui/zest.ts'
import { syncRouteFromLocation } from './src/ui/router.ts'

// The app defines the Zest tags at boot; tests do the same.
defineZest()

// The router reads the real history, so a test that navigates would leave the
// next test starting on whatever route it ended on. Reset it for each test.
beforeEach(() => {
  window.history.pushState({}, '', '/app/')
  syncRouteFromLocation()
})

// Without this nothing is removed between tests: the previous element stays in
// the document, keeps its subscriptions, and every query finds two of
// everything. Removing it also runs each element's disconnect cleanup.
afterEach(() => {
  document.body.replaceChildren()
})

// No real WebSocket in tests.
//
// jsdom's WebSocket fails asynchronously with "ws does not work in the
// browser", which surfaces as an unhandled rejection that vitest reports as an
// error even when every test passes. Opting out here is also closer to what the
// tests mean: they exercise subscription bookkeeping, not the network.
import { eventSocket } from './src/store/events.ts'
eventSocket.setSocketFactory(() => null)
