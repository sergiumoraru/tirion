import { ref } from 'vue'

const storageKey = 'tirion-api-token'
function restoredToken(): string {
  try { return sessionStorage.getItem(storageKey) || '' } catch { return '' }
}
export const apiToken = ref(restoredToken())
export const apiAuthenticated = ref(false)

export function setApiToken(token: string) {
  apiToken.value = token.trim()
  try {
    if (apiToken.value) sessionStorage.setItem(storageKey, apiToken.value)
    else sessionStorage.removeItem(storageKey)
  } catch { /* Browsers may disable session storage. In-memory access still works. */ }
}

export function clearApiAuth() {
  setApiToken('')
  apiAuthenticated.value = false
}
