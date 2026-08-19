import { ref } from 'vue'

export type Theme = 'light' | 'dark'

const STORAGE_KEY = 'markup-theme'

function systemTheme(): Theme {
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function initialTheme(): Theme {
  const saved = localStorage.getItem(STORAGE_KEY)
  return saved === 'light' || saved === 'dark' ? saved : systemTheme()
}

/** Reactive current theme; persisted to localStorage on manual toggle. */
export const theme = ref<Theme>(initialTheme())

/** Reflect the theme onto <html data-theme="..."> for CSS variables. */
export function applyTheme(t: Theme): void {
  document.documentElement.dataset.theme = t
}

export function toggleTheme(): void {
  theme.value = theme.value === 'dark' ? 'light' : 'dark'
  localStorage.setItem(STORAGE_KEY, theme.value)
}

// Apply as early as possible (module import) to avoid a light flash
// before the Vue app mounts.
applyTheme(theme.value)
