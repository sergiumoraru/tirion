import { computed, ref } from 'vue'

type ThemeMode = 'dark' | 'light' | 'menu'

const themeMode = ref<ThemeMode>('menu')
const themeOrder: ThemeMode[] = ['dark', 'light', 'menu']

function applyTheme() {
  const root = document.documentElement
  root.classList.remove('dark', 'theme-menuconfig')

  if (themeMode.value === 'dark') {
    root.classList.add('dark')
    return
  }
  if (themeMode.value === 'menu') {
    // Keep dark utilities active while skinning with theme-menuconfig overrides.
    root.classList.add('dark', 'theme-menuconfig')
  }
}

function persistTheme(mode: ThemeMode) {
  localStorage.setItem('themeMode', mode)
  // Keep compatibility with old key usage.
  localStorage.setItem('theme', mode === 'menu' ? 'dark' : mode)
}

export function useTheme() {
  function init() {
    const modeStored = localStorage.getItem('themeMode')
    const legacy = localStorage.getItem('theme')

    if (modeStored === 'dark' || modeStored === 'light' || modeStored === 'menu') {
      themeMode.value = modeStored
    } else if (legacy === 'dark' || legacy === 'light') {
      themeMode.value = legacy
    } else {
      themeMode.value = 'menu'
    }

    persistTheme(themeMode.value)
    applyTheme()
  }

  function setTheme(mode: ThemeMode) {
    themeMode.value = mode
    persistTheme(mode)
    applyTheme()
  }

  function toggleTheme() {
    const currentIndex = themeOrder.indexOf(themeMode.value)
    const next = themeOrder[(currentIndex + 1) % themeOrder.length]
    setTheme(next)
  }

  const isDark = computed(() => themeMode.value === 'dark')
  const isMenu = computed(() => themeMode.value === 'menu')

  return { isDark, isMenu, themeMode, setTheme, toggleTheme, init }
}
