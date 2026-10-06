<template>
  <div class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4 hover:border-accent dark:hover:border-accent transition-colors">
    <div class="flex items-start justify-between gap-4">
      <div class="flex-1 min-w-0">
        <div class="font-mono text-accent font-medium truncate">
          {{ symbol.name }}
        </div>

        <div class="text-sm text-n-500 truncate mt-1">
          {{ symbol.file }}
          <span class="text-n-500">:{{ symbol.line }}</span>
        </div>

        <div class="flex items-center gap-2 mt-2 flex-wrap">
          <span class="px-2 py-0.5 text-xs bg-gray-100 dark:bg-n-900 text-n-300 rounded-none border border-gray-300 dark:border-n-600">
            {{ symbol.repo }}
          </span>
          <span v-if="snapshotLabel" class="text-xs text-n-500 font-mono">
            {{ snapshotLabel }}
          </span>
          <span class="px-2 py-0.5 text-xs uppercase tracking-wide bg-gray-100 dark:bg-n-900 text-n-300 rounded-none border border-gray-300 dark:border-n-600">
            unresolved
          </span>
          <span v-if="symbol.caller" class="text-xs text-n-500">
            from {{ symbol.caller }}
          </span>
          <span v-if="symbol.score !== undefined && symbol.score > 0" class="text-xs text-n-500">
            Score: {{ symbol.score.toFixed(2) }}
          </span>
        </div>
      </div>

      <div v-if="symbol.caller" class="flex items-center gap-2">
        <button
          @click.stop="$emit('trace', symbol.caller)"
          class="px-3 py-1.5 text-xs uppercase tracking-wide bg-gray-100 dark:bg-n-900 hover:bg-gray-200 dark:hover:bg-n-600 rounded-none text-n-300 border border-gray-300 dark:border-n-600 transition-colors"
          title="Trace containing caller"
        >
          Trace Caller
        </button>
        <button
          @click.stop="$emit('impact', symbol.caller)"
          class="px-3 py-1.5 text-xs uppercase tracking-wide bg-accent hover:bg-accent/80 rounded-none text-white transition-colors"
          title="Run impact from containing caller"
        >
          Impact Caller
        </button>
      </div>
    </div>

    <div v-if="symbol.source" class="mt-3">
      <pre class="text-xs text-n-400 bg-gray-50 dark:bg-n-900 rounded-none p-3 overflow-x-auto max-h-32 border border-gray-300 dark:border-n-600"><code>{{ symbol.source }}</code></pre>
    </div>
  </div>
</template>

<script setup lang="ts">
import type { ExternalSymbolResult } from '@/types'

defineProps<{
  symbol: ExternalSymbolResult
  snapshotLabel?: string
}>()

defineEmits<{
  trace: [name: string]
  impact: [name: string]
}>()
</script>
