import { createSignal, onMount } from 'solid-js'

/**
 * Theme selection.
 *
 * Three states, not two: 'light', 'dark', and 'system'. System is the default
 * and stamps NO attribute on the root, which is what lets the CSS media query
 * in tokens.css take effect. Stamping an attribute for the system choice would
 * freeze the theme at whatever the system was when the page loaded.
 */

export type Theme = 'light' | 'dark' | 'system'

const STORAGE_KEY = 'reactorcide.theme'

function readStored(): Theme {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    if (stored === 'light' || stored === 'dark' || stored === 'system') return stored
  } catch {
    // A private window, cleared site data, or a browser configured to block
    // storage. Some browsers THROW on access rather than returning null, so
    // this has to be a try/catch rather than a null check.
  }
  return 'system'
}

const [theme, setThemeSignal] = createSignal<Theme>(readStored())

export { theme }

export function setTheme(next: Theme): void {
  setThemeSignal(next)
  applyTheme(next)
  try {
    localStorage.setItem(STORAGE_KEY, next)
  } catch {
    // Not being able to remember the choice is a small loss; failing the
    // interaction over it would be a larger one.
  }
}

export function applyTheme(next: Theme): void {
  const root = document.documentElement
  if (next === 'system') {
    root.removeAttribute('data-theme')
  } else {
    root.setAttribute('data-theme', next)
  }
}

/** Applies the stored theme as early as possible. */
export function useThemeBootstrap(): void {
  onMount(() => applyTheme(theme()))
}

/** Cycles light -> dark -> system, which is the order a toggle button walks. */
export function nextTheme(current: Theme): Theme {
  if (current === 'light') return 'dark'
  if (current === 'dark') return 'system'
  return 'light'
}

export function themeLabel(current: Theme): string {
  if (current === 'light') return 'Light'
  if (current === 'dark') return 'Dark'
  return 'System'
}

export function themeGlyph(current: Theme): string {
  if (current === 'light') return '☀'
  if (current === 'dark') return '☾'
  return '◐'
}
