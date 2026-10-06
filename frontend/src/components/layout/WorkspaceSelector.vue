<template>
  <div class="flex items-center gap-2 text-xs font-mono uppercase tracking-wide text-n-500">
    <span>Workspace</span>
    <select
      :value="workspaceId"
      class="bg-white dark:bg-n-900 text-black dark:text-n-300 border-2 border-gray-300 dark:border-n-600 px-2 py-1 max-w-[220px]"
      @change="handleChange"
    >
      <option v-for="workspace in selectorWorkspaces" :key="workspace.slug" :value="workspace.slug">
        {{ workspace.slug }}
      </option>
    </select>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { api } from '@/api/client'
import type { WorkspaceSummary } from '@/types'
import { useWorkspace } from '@/composables/useWorkspace'

const { workspaceId, setWorkspaceId } = useWorkspace()
const workspaces = ref<WorkspaceSummary[]>([])

const selectorWorkspaces = computed(() => {
  if (workspaces.value.some((workspace) => workspace.slug === workspaceId.value)) {
    return workspaces.value
  }
  return [
    ...workspaces.value,
    {
      id: 0,
      slug: workspaceId.value,
      name: workspaceId.value,
      description: '',
      createdByLabel: '',
      isDefault: workspaceId.value === 'default-main',
      createdAt: '',
      updatedAt: '',
      activeSnapshots: [],
      repos: [],
    },
  ]
})

function handleChange(event: Event) {
  const target = event.target as HTMLSelectElement
  setWorkspaceId(target.value)
}

async function loadWorkspaces() {
  try {
    const response = await api.listWorkspaces()
    workspaces.value = response.workspaces
    if (!workspaces.value.some((workspace) => workspace.slug === workspaceId.value)) {
      const fallback = workspaces.value.find((workspace) => workspace.isDefault) ?? workspaces.value[0]
      if (fallback) {
        setWorkspaceId(fallback.slug)
      }
    }
  } catch (error) {
    workspaces.value = [
      {
        id: 0,
        slug: workspaceId.value,
        name: workspaceId.value,
        description: '',
        createdByLabel: '',
        isDefault: workspaceId.value === 'default-main',
        createdAt: '',
        updatedAt: '',
        activeSnapshots: [],
        repos: [],
      },
    ]
    console.error('Failed to load workspaces:', error)
  }
}

watch(workspaceId, () => {
  loadWorkspaces()
})

onMounted(() => {
  loadWorkspaces()
})
</script>
