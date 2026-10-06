<template>
  <div class="font-mono text-sm">
    <TraceNode
      :node="node"
      :depth="depth"
      :direction="direction"
      :max-depth="maxDepth"
      :load-children="loadChildren"
      :should-include-node="shouldIncludeNode"
      :hide-module="hideModule"
    />
  </div>
</template>

<script setup lang="ts">
import type { TraceExpandResponse, TraceNode as TraceNodeType } from '@/types'
import TraceNode from './TraceNode.vue'

defineProps<{
  node: TraceNodeType
  depth: number
  direction: 'downstream' | 'upstream'
  maxDepth: number
  loadChildren: (node: TraceNodeType, direction: 'downstream' | 'upstream') => Promise<TraceExpandResponse>
  shouldIncludeNode: (node: TraceNodeType, isRoot: boolean) => boolean
  hideModule: boolean
}>()
</script>
