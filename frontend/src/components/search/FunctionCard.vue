<template>
  <div class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4 hover:border-accent dark:hover:border-accent transition-colors">
    <div class="flex items-start justify-between gap-4">
      <div class="flex-1 min-w-0">
        <!-- Function name -->
        <div class="font-mono text-accent font-medium truncate" :title="fn.name">
          {{ displayName }}
        </div>

        <!-- File path -->
        <div class="text-sm text-n-500 truncate mt-1">
          {{ fn.file }}
          <span class="text-n-500">:{{ fn.startLine }}-{{ fn.endLine }}</span>
        </div>

        <!-- Repo -->
        <div class="flex items-center gap-2 mt-2">
          <span class="px-2 py-0.5 text-xs bg-gray-100 dark:bg-n-900 text-n-300 rounded-none border border-gray-300 dark:border-n-600">
            {{ fn.repo }}
          </span>
          <span v-if="snapshotLabel" class="text-xs text-n-500 font-mono">
            {{ snapshotLabel }}
          </span>
          <span v-if="fn.richness" class="text-xs text-accent">
            {{ fn.richness }} edges
          </span>
          <span v-if="fn.score !== undefined && fn.score > 0" class="text-xs text-n-500">
            Score: {{ fn.score.toFixed(2) }}
          </span>
        </div>
      </div>

      <div class="flex items-center gap-2">
        <button
          @click.stop="$emit('trace', fn.name)"
          class="px-3 py-1.5 text-xs uppercase tracking-wide bg-gray-100 dark:bg-n-900 hover:bg-gray-200 dark:hover:bg-n-600 rounded-none text-n-300 border border-gray-300 dark:border-n-600 transition-colors"
          title="Trace call chain"
        >
          Trace
        </button>
        <button
          @click.stop="$emit('impact', fn.name)"
          class="px-3 py-1.5 text-xs uppercase tracking-wide bg-accent hover:bg-accent/80 rounded-none text-white transition-colors"
          title="Run impact analysis"
        >
          Impact
        </button>
      </div>
    </div>

    <div v-if="showIntegrationSection" class="mt-3 border border-gray-300 dark:border-n-600 p-3">
      <div class="text-xs text-n-500 uppercase tracking-widest">Connected Integrations</div>
      <div v-if="integrations.length" class="mt-2 space-y-2">
        <div v-for="integration in integrations" :key="integrationKey(integration)" class="text-sm">
          <div class="font-mono text-black dark:text-n-300">
            <span v-if="isQueueIntegration(integration)">SQS → {{ integration.path }}</span>
            <span v-else>{{ integration.method }} {{ integration.path }}</span>
          </div>
          <div class="text-xs text-n-500 mt-0.5">
            <span v-if="isQueueIntegration(integration)">
              <span v-if="integration.targetRepo">{{ integration.targetRepo }}</span>
              <span v-else>no indexed consumer</span>
            </span>
            <span v-else>{{ integration.targetRepo }}</span>
            <span v-if="integration.targetHandler"> · {{ integration.targetHandler }}</span>
            <span> · {{ integration.resolution === 'matched' ? 'directly matched' : integration.resolution === 'inferred' ? 'inferred from source' : 'consumer unresolved' }}</span>
          </div>
        </div>
      </div>
    </div>

    <!-- Source preview -->
    <div v-if="fn.source" class="mt-3">
      <pre class="text-xs text-n-400 bg-gray-50 dark:bg-n-900 rounded-none p-3 overflow-x-auto max-h-32 border border-gray-300 dark:border-n-600"><code>{{ fn.source }}</code></pre>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { FunctionIntegration, FunctionResult } from '@/types'
import { formatFunctionDisplayName } from '@/lib/functionNames'

const props = defineProps<{
  fn: FunctionResult
  prefetchedIntegrations?: FunctionIntegration[]
  snapshotLabel?: string
}>()

defineEmits<{
  trace: [name: string]
  impact: [name: string]
}>()

const displayName = computed(() => formatFunctionDisplayName(props.fn.name))
const integrations = computed(() => props.prefetchedIntegrations ?? props.fn.integrations ?? [])
const showIntegrationSection = computed(() => integrations.value.length > 0)

function isQueueIntegration(integration: FunctionIntegration): boolean {
  return integration.type === 'sqs' || integration.method === 'SQS' || integration.clientType === 'sqs'
}

function integrationKey(integration: FunctionIntegration): string {
  return [
    integration.type ?? '',
    integration.method,
    integration.path,
    integration.targetRepo,
    integration.targetHandler ?? '',
    integration.lineNumber,
  ].join('|')
}
</script>
