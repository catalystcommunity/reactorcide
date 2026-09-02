import { cleanup } from '@solidjs/testing-library'
import { afterEach, beforeEach } from 'vitest'

// Without this NOTHING is unmounted between tests: the previous render stays
// in the document and every query then finds two of everything. The symptom
// reads as a duplicate-element bug in the component under test. It is not.
afterEach(cleanup)

// @solidjs/router reads the real history, so a test that navigates leaves the
// next test starting on whatever route it ended on. Only shows up once a
// second test navigates, which is why it is reset here rather than per file.
beforeEach(() => {
  window.history.pushState({}, '', '/app/')
})

// No real WebSocket in tests.
//
// jsdom's WebSocket fails asynchronously with "ws does not work in the
// browser", which surfaces as an unhandled rejection that vitest reports as an
// error even when every test passes. Opting out here is also closer to what the
// tests mean: they exercise subscription bookkeeping, not the network.
import { eventSocket } from './src/store/events.ts'
eventSocket.setSocketFactory(() => null)
