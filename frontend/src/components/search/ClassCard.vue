<template>
  <div class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4 hover:border-accent dark:hover:border-accent transition-colors">
    <div class="flex items-start justify-between gap-4">
      <div class="flex-1 min-w-0">
        <!-- Class name -->
        <div class="font-mono text-green-600 dark:text-green-400 font-medium truncate">
          {{ cls.name }}
        </div>

        <!-- File path -->
        <div class="text-sm text-n-500 truncate mt-1">
          {{ cls.file }}
          <span class="text-n-500">:{{ cls.startLine }}-{{ cls.endLine }}</span>
        </div>

        <!-- Repo -->
        <div class="flex items-center gap-2 mt-2">
          <span class="px-2 py-0.5 text-xs bg-gray-100 dark:bg-n-900 text-n-300 rounded-none border border-gray-300 dark:border-n-600">
            {{ cls.repo }}
          </span>
          <span v-if="snapshotLabel" class="text-xs text-n-500 font-mono">
            {{ snapshotLabel }}
          </span>
          <span v-if="cls.score !== undefined" class="text-xs text-n-500">
            Score: {{ cls.score.toFixed(2) }}
          </span>
        </div>
      </div>
    </div>

    <div v-if="showIntegrationSection" class="mt-3 border border-gray-300 dark:border-n-600 p-3">
      <div class="text-xs text-n-500 uppercase tracking-widest">Connected Integrations</div>
      <div v-if="integrations.length" class="mt-2 space-y-2">
        <div v-for="integration in integrations" :key="integrationKey(integration)" class="text-sm">
          <div class="font-mono text-black dark:text-n-300">
            {{ integration.method }} {{ integration.path }}
          </div>
          <div class="text-xs text-n-500 mt-0.5">
            {{ shortMethodName(integration.sourceFunction) }} · {{ integration.targetRepo }}
            <span v-if="integration.targetHandler"> · {{ integration.targetHandler }}</span>
            <span v-if="integration.resolution === 'inferred'"> · inferred from source</span>
            <span v-else-if="integration.resolution === 'unresolved'"> · consumer unresolved</span>
          </div>
        </div>
      </div>
      <div v-if="handledEndpoints.length" class="mt-3 border border-gray-300 dark:border-n-600 p-3">
        <div class="text-xs text-n-500 uppercase tracking-widest">Handled Endpoints</div>
        <div class="mt-2 space-y-2">
          <div v-for="endpoint in handledEndpoints" :key="handledEndpointKey(endpoint)" class="text-sm">
            <div class="font-mono text-black dark:text-n-300">
              {{ endpoint.method }} {{ endpoint.path }}
            </div>
            <div class="text-xs text-n-500 mt-0.5">
              {{ endpoint.handler || cls.name }}
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { ClassHandledEndpoint, ClassIntegration, ClassResult } from '@/types'

const props = defineProps<{
  cls: ClassResult
  prefetchedIntegrations?: ClassIntegration[]
  prefetchedHandledEndpoints?: ClassHandledEndpoint[]
  snapshotLabel?: string
}>()

const integrations = computed<ClassIntegration[]>(() => props.prefetchedIntegrations ?? props.cls.integrations ?? [])
const handledEndpoints = computed<ClassHandledEndpoint[]>(() => props.prefetchedHandledEndpoints ?? props.cls.handledEndpoints ?? [])
const showIntegrationSection = computed(() => integrations.value.length > 0 || handledEndpoints.value.length > 0)

function shortMethodName(name: string): string {
  const parts = name.split('.')
  return parts[parts.length - 1] || name
}

function integrationKey(integration: ClassIntegration): string {
  return [
    integration.sourceFunction,
    integration.method,
    integration.path,
    integration.targetRepo,
    integration.targetHandler ?? '',
    integration.resolution,
  ].join('|')
}

function handledEndpointKey(endpoint: ClassHandledEndpoint): string {
  return [endpoint.method, endpoint.path, endpoint.handler ?? ''].join('|')
}
</script>
