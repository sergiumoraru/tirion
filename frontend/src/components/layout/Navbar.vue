<template>
  <nav class="bg-white dark:bg-n-900 border-b-2 border-black dark:border-n-600">
    <div class="max-w-7xl mx-auto px-4">
      <div class="py-4 grid gap-3 md:[grid-template-columns:auto_1fr] md:items-center xl:[grid-template-columns:auto_1fr_auto]">
        <div class="flex items-center gap-2 shrink-0 md:row-start-1 md:col-start-1">
            <router-link to="/" class="no-underline">
              <span
                class="text-xl font-bold uppercase border-4 border-double px-[7px] py-[1px] cursor-pointer"
                :class="logoClass"
                style="letter-spacing: 3px;"
              >
                Tirion
              </span>
            </router-link>
        </div>

        <div class="flex flex-wrap items-center gap-1 md:row-start-1 md:col-start-2 md:min-w-0 md:justify-start xl:justify-center">
          <router-link
            v-for="link in links"
            :key="link.to"
            :to="link.to"
            class="px-3 py-1 text-xs font-mono uppercase tracking-wide transition-colors"
            :class="[
              $route.name === link.name
                ? 'bg-accent text-white'
                : 'text-n-400 hover:text-black dark:hover:text-n-300'
            ]"
          >
            {{ link.label }}
          </router-link>
        </div>

        <div class="flex items-center justify-between gap-4 md:row-start-2 md:col-span-2 xl:row-start-1 xl:col-start-3 xl:col-span-1 xl:justify-end xl:shrink-0">
          <WorkspaceSelector />
          <button
            @click="toggleTheme"
            title="Cycle theme: dark, light, menuconfig"
            class="text-xs font-mono uppercase tracking-wide text-n-400 hover:text-black dark:hover:text-n-300 border-2 border-gray-300 dark:border-n-600 px-2 py-1"
          >
            [{{ themeLabel }}]
          </button>
        </div>
      </div>
    </div>
  </nav>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useTheme } from '@/composables/useTheme'
import WorkspaceSelector from '@/components/layout/WorkspaceSelector.vue'

const { themeMode, toggleTheme } = useTheme()
const themeLabel = computed(() => {
  if (themeMode.value === 'menu') return 'MENU'
  if (themeMode.value === 'light') return 'LIGHT'
  return 'DARK'
})
const logoClass = computed(() => {
  if (themeMode.value === 'menu') return 'text-[#1a3abd] border-[#1a3abd]'
  if (themeMode.value === 'light') return 'text-accent border-black'
  return 'text-accent dark:border-n-600'
})

const links = [
  { to: '/', name: 'search', label: 'Search' },
  { to: '/trace', name: 'trace', label: 'Trace' },
  { to: '/flow', name: 'flow', label: 'Flow' },
  { to: '/impact', name: 'impact', label: 'Impact' },
  { to: '/contracts', name: 'contracts', label: 'Contracts' },
  { to: '/admin', name: 'admin', label: 'Admin' },
]
</script>
