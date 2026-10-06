<template>
  <div :class="!collapsedNode && depth > 0 ? 'border-l border-gray-300 dark:border-n-600 ml-2' : ''">
    <div
      v-if="!collapsedNode"
      class="flex items-start gap-2 py-1.5 px-2 hover:bg-gray-100 dark:hover:bg-n-800/50 cursor-pointer"
      @click="toggle"
    >
      <!-- Expand icon -->
      <button
        v-if="canExpand"
        class="text-n-500 w-4 h-4 flex items-center justify-center text-xs flex-shrink-0 mt-0.5"
      >
        <span v-if="loadingChildren">…</span>
        <span v-else>{{ expanded ? '▼' : '▶' }}</span>
      </button>
      <span v-else class="w-4 flex-shrink-0"></span>

      <div class="flex-1 min-w-0">
        <!-- Function name + markers -->
        <div class="flex items-center gap-2 flex-wrap">
          <span :class="nodeClass" class="font-mono font-medium">{{ displayName }}</span>

          <!-- Markers -->
          <span
            v-if="node.confidence"
            :class="resolutionClass"
            class="px-1.5 py-0.5 text-xs rounded-none flex-shrink-0"
          >
            {{ resolutionLabel }}
          </span>
          <span v-if="node.injected" class="px-1.5 py-0.5 text-xs bg-purple-900/50 text-purple-300 rounded-none flex-shrink-0">
            injected
          </span>
          <span v-if="node.is_sqs" class="px-1.5 py-0.5 text-xs bg-yellow-900/50 text-yellow-300 rounded-none flex-shrink-0">
            SQS → {{ node.queue_target }}
          </span>
          <span v-if="node.edge_type === 'eventbridge'" class="px-1.5 py-0.5 text-xs bg-orange-900/50 text-orange-300 rounded-none flex-shrink-0">
            EventBridge
          </span>
          <span v-if="node.is_cross_service && node.http_method" class="px-1.5 py-0.5 text-xs bg-blue-900/50 text-blue-300 rounded-none flex-shrink-0">
            {{ node.http_method }} → {{ node.http_target }}
          </span>
          <span
            v-if="targetRepoLabel"
            class="px-1.5 py-0.5 text-xs bg-indigo-900/50 text-indigo-300 rounded-none flex-shrink-0"
          >
            {{ targetRepoLabel }}
          </span>
        </div>

        <div v-if="targetLocation" :title="targetLocation" class="text-xs text-n-400 mt-0.5 truncate">
          {{ targetLocation }}
        </div>
        <div v-if="evidenceLocation" :title="evidenceLocation" class="text-xs text-n-500 mt-0.5 truncate">
          {{ evidenceLocation }}
        </div>
      </div>
    </div>

    <!-- Children -->
    <div v-if="expanded && expandWarnings.length" class="pl-6 text-xs text-amber-600 dark:text-amber-300 space-y-1">
      <div v-for="warning in expandWarnings" :key="warning">
        {{ warning }}
      </div>
    </div>
    <div v-if="(expanded || collapsedNode) && filteredChildren.length" :class="collapsedNode ? '' : 'pl-4'">
      <TraceNode
        v-for="child in visibleChildren"
        :key="childKey(child)"
        :node="child"
        :depth="collapsedNode ? depth : depth + 1"
        :direction="direction"
        :max-depth="maxDepth"
        :load-children="loadChildren"
        :should-include-node="shouldIncludeNode"
        :hide-module="hideModule"
      />
      <details v-if="unindexedChildren.length" :key="node.caller_id || node.name" class="ml-2 mt-1">
        <summary class="cursor-pointer px-2 py-1.5 text-xs text-n-500 hover:text-n-700 dark:hover:text-n-300">
          Other calls ({{ unindexedChildren.length }}) — no indexed target
        </summary>
        <p class="px-2 pb-2 text-xs text-n-500">
          Library calls and unresolved application calls. Call sites are retained below.
        </p>
        <TraceNode
          v-for="child in unindexedChildren"
          :key="childKey(child)"
          :node="child"
          :depth="collapsedNode ? depth : depth + 1"
          :direction="direction"
          :max-depth="maxDepth"
          :load-children="loadChildren"
          :should-include-node="shouldIncludeNode"
          :hide-module="hideModule"
        />
      </details>
    </div>
    <div v-else-if="expanded && loadError" class="pl-6 text-xs text-red-400">
      {{ loadError }}
    </div>
    <div v-else-if="expanded && noChildren" class="pl-6 mx-2 text-xs text-n-500">
      No further calls
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed } from 'vue'
import type { TraceExpandResponse, TraceNode as TraceNodeType } from '@/types'
import { formatFunctionDisplayName } from '@/lib/functionNames'
import { isUnindexedLeafCall, traceLocations } from '@/lib/tracePresentation'

const props = defineProps<{
  node: TraceNodeType
  depth: number
  direction: 'downstream' | 'upstream'
  maxDepth: number
  loadChildren: (node: TraceNodeType, direction: 'downstream' | 'upstream') => Promise<TraceExpandResponse>
  shouldIncludeNode: (node: TraceNodeType, isRoot: boolean) => boolean
  hideModule: boolean
}>()

function shouldAutoExpandNode(node: TraceNodeType, depth: number): boolean {
  if ((node.children?.length ?? 0) === 0) return false
  if (depth < 2) return true
  return node.source === 'integration_handler' || node.source === 'integration_continuation'
}

const expanded = ref(shouldAutoExpandNode(props.node, props.depth))
const loadingChildren = ref(false)
const loadError = ref<string | null>(null)
const lazyChildren = ref<TraceNodeType[] | null>(null)
const expandWarnings = ref<string[]>([])
const hasLoaded = ref(false)

const rawChildren = computed(() => lazyChildren.value ?? props.node.children ?? [])
const hasRawChildren = computed(() => rawChildren.value.length > 0)
const noChildren = computed(() => hasLoaded.value && !hasRawChildren.value)
function traceNodeHasBoundary(node: TraceNodeType, maxDepth = 2): boolean {
  if (node.edge_type === 'http' || node.is_cross_service || node.is_sqs || node.edge_type === 'eventbridge') {
    return true
  }
  if (maxDepth <= 0) return false
  return (node.children ?? []).some((child) => traceNodeHasBoundary(child, maxDepth - 1))
}

function traceChildPriority(node: TraceNodeType): number {
  if (node.edge_type === 'http' || node.is_cross_service || node.is_sqs || node.edge_type === 'eventbridge') {
    return 0
  }
  if (traceNodeHasBoundary(node)) {
    return 1
  }
  if (node.source === 'integration_handler' || node.source === 'integration_continuation') {
    return 2
  }
  return 3
}
const filteredChildren = computed(() => {
  const base = !props.shouldIncludeNode
    ? rawChildren.value
    : rawChildren.value.filter((child) => props.shouldIncludeNode(child, false))

  return [...base]
    .map((child, index) => ({ child, index }))
    .sort((left, right) => {
      const priorityDelta = traceChildPriority(left.child) - traceChildPriority(right.child)
      if (priorityDelta !== 0) return priorityDelta
      return left.index - right.index
    })
    .map(({ child }) => child)
})
const unindexedChildren = computed(() => props.direction === 'downstream'
  ? filteredChildren.value.filter(isUnindexedLeafCall)
  : [])
const visibleChildren = computed(() => props.direction === 'downstream'
  ? filteredChildren.value.filter((child) => !isUnindexedLeafCall(child))
  : filteredChildren.value)

function childKey(child: TraceNodeType): string {
  return JSON.stringify([child.caller_id, child.name, child.file, child.line, child.edge_type, child.evidence])
}

const canExpand = computed(() => {
  if (hasRawChildren.value) return true
  if (!props.node.caller_id) return false
  if (props.depth >= props.maxDepth) return false
  return !noChildren.value
})
const isModule = computed(() => props.node.name === '_module_')
const collapsedNode = computed(() => props.hideModule && isModule.value)
const locations = computed(() => traceLocations(props.node, props.depth === 0))
const targetLocation = computed(() => locations.value.target)
const evidenceLocation = computed(() => locations.value.evidence)
const displayName = computed(() => formatFunctionDisplayName(props.node.name))
const targetRepoLabel = computed(() => {
  const isSyntheticBoundaryNode = props.node.edge_type === 'http' && props.node.name.startsWith('[')
  if (!isSyntheticBoundaryNode && !props.node.is_sqs) return ''
  if (props.node.repo) return `repo: ${props.node.repo}`
  const firstChildRepo = rawChildren.value.find((child) => child.repo)?.repo
  return firstChildRepo ? `repo: ${firstChildRepo}` : ''
})

const nodeClass = computed(() => {
  if (props.node.injected) return 'text-purple-400'
  if (props.node.edge_type === 'eventbridge') return 'text-orange-400'
  if (props.node.is_sqs) return 'text-yellow-400'
  if (props.node.is_cross_service) return 'text-accent'
  return 'text-black dark:text-n-300'
})

const resolutionLabel = computed(() => {
  switch (props.node.confidence) {
    case 'high':
      return 'direct'
    case 'medium':
      return 'resolved'
    case 'low':
      return 'name-based'
    default:
      return ''
  }
})

const resolutionClass = computed(() => {
  switch (props.node.confidence) {
    case 'high':
      return 'bg-emerald-900/50 text-emerald-300'
    case 'medium':
      return 'bg-amber-900/50 text-amber-300'
    case 'low':
      return 'bg-rose-900/50 text-rose-300'
    default:
      return 'bg-gray-200 dark:bg-n-800 text-n-400'
  }
})

function toggle() {
  if (expanded.value) {
    expanded.value = false
    return
  }
  if (!canExpand.value) return
  if (!hasRawChildren.value && props.node.caller_id && !hasLoaded.value && !loadingChildren.value) {
    loadingChildren.value = true
    loadError.value = null
    props
      .loadChildren(props.node, props.direction)
      .then((response) => {
        lazyChildren.value = response.children ?? []
        expandWarnings.value = response.warnings ?? []
        hasLoaded.value = true
      })
      .catch((err) => {
        loadError.value = err instanceof Error ? err.message : 'Failed to load children'
      })
      .finally(() => {
        loadingChildren.value = false
      })
  }
  expanded.value = true
}
</script>
