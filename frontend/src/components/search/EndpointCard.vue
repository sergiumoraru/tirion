<template>
  <div class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4 hover:border-accent dark:hover:border-accent transition-colors">
    <div class="flex items-start justify-between gap-4">
      <div class="flex-1 min-w-0">
        <!-- Method + Path -->
        <div class="flex items-center gap-2">
          <span :class="methodClass" class="px-2 py-0.5 text-xs font-semibold rounded-none">
            {{ endpoint.method }}
          </span>
          <span class="font-mono text-yellow-600 dark:text-yellow-400 truncate">
            {{ endpoint.path }}
          </span>
        </div>

        <!-- Handler -->
        <div class="text-sm text-n-400 mt-1">
          Handler:
          <span v-if="endpoint.handler" class="text-n-300">{{ endpoint.handler }}</span>
          <span v-else class="text-n-500 italic">none</span>
        </div>

        <!-- File path -->
        <div class="text-sm text-n-500 truncate mt-1">
          {{ endpoint.file }}
          <span class="text-n-500">:{{ endpoint.line }}</span>
        </div>

        <!-- Repo -->
        <div class="flex items-center gap-2 mt-2">
          <span class="px-2 py-0.5 text-xs bg-gray-100 dark:bg-n-900 text-n-300 rounded-none border border-gray-300 dark:border-n-600">
            {{ endpoint.repo }}
          </span>
          <span v-if="snapshotLabel" class="text-xs text-n-500 font-mono">
            {{ snapshotLabel }}
          </span>
        </div>
      </div>

      <!-- Actions -->
      <div v-if="endpoint.handler" class="flex items-center gap-2">
        <button
          @click.stop="$emit('trace', endpoint.handler)"
          class="px-3 py-1.5 text-xs uppercase tracking-wide bg-gray-100 dark:bg-n-900 hover:bg-gray-200 dark:hover:bg-n-600 rounded-none text-n-300 border border-gray-300 dark:border-n-600 transition-colors"
          title="Trace handler"
        >
          Trace
        </button>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { EndpointResult } from '@/types'

const props = defineProps<{
  endpoint: EndpointResult
  snapshotLabel?: string
}>()

defineEmits<{
  trace: [handler: string]
}>()

const methodClass = computed(() => {
  const colors: Record<string, string> = {
    GET: 'bg-green-900 text-green-300',
    POST: 'bg-blue-900 text-blue-300',
    PUT: 'bg-yellow-900 text-yellow-300',
    PATCH: 'bg-orange-900 text-orange-300',
    DELETE: 'bg-red-900 text-red-300',
  }
  return colors[props.endpoint.method] || 'bg-gray-200 dark:bg-n-800 text-n-300'
})
</script>
