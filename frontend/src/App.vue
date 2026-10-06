<template>
  <div class="h-full flex flex-col overflow-hidden bg-white text-black dark:bg-n-950 dark:text-n-300 font-mono">
    <template v-if="apiAuthenticated">
      <Navbar />
      <main class="flex-1 min-h-0 overflow-auto"><router-view /></main>
    </template>
    <main v-else class="flex-1 flex items-center justify-center p-6">
      <form class="w-full max-w-lg space-y-4" @submit.prevent="connect">
        <h1 class="text-2xl font-bold">Connect to Tirion</h1>
        <p>Enter the API token from your server. For a local installation, read
          <code>~/.tirion/api-token</code> after starting <code>tirion serve</code>
          (or <code>api-token</code> inside the directory set by <code>TIRION_HOME</code>,
          or the file named by <code>TIRION_API_TOKEN_FILE</code>). Installations that
          predate the rename may still use <code>~/.codebase-intel/api-token</code>.
        </p>
        <label class="block" for="api-token">API token</label>
        <input id="api-token" v-model="enteredToken" type="password" autocomplete="off" required
          class="w-full border border-gray-400 bg-transparent p-3 rounded" :disabled="connecting" />
        <p class="text-sm">Stored only for this browser tab's session.</p>
        <p v-if="error" role="alert" class="text-red-600 dark:text-red-400">{{ error }}</p>
        <button type="submit" :disabled="connecting || !enteredToken.trim()"
          class="border border-gray-400 px-4 py-2 rounded disabled:opacity-50">
          {{ connecting ? 'Connecting…' : 'Connect' }}
        </button>
      </form>
    </main>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import Navbar from '@/components/layout/Navbar.vue'
import { useTheme } from '@/composables/useTheme'
import { api } from '@/api/client'
import { apiToken, apiAuthenticated, setApiToken, clearApiAuth } from '@/composables/useApiAuth'

const { init } = useTheme()
const enteredToken = ref(apiToken.value)
const connecting = ref(false)
const error = ref('')
async function connect() {
  connecting.value = true
  error.value = ''
  setApiToken(enteredToken.value)
  try {
    await api.health()
    apiAuthenticated.value = true
    enteredToken.value = ''
  } catch (cause) {
    clearApiAuth()
    error.value = cause instanceof Error ? cause.message : 'Unable to connect to Tirion'
  } finally { connecting.value = false }
}
onMounted(() => {
  init()
  if (apiToken.value) void connect()
})
</script>
