/**
 * Defines the Zest tags the UI uses. The application calls this one time at
 * boot, and vitest.setup.ts calls it for the tests. Importing a Zest package
 * defines nothing.
 */
import { defineControls } from '@catalystcommunity/controls'
import { defineContainers } from '@catalystcommunity/containers'
import { defineMenus } from '@catalystcommunity/menus'

export function defineZest(): void {
  defineControls()
  defineContainers()
  defineMenus()
}
