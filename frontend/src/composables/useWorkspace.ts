import { computed, ref } from 'vue'

const STORAGE_KEY = 'tirionWorkspaceId'
const DEFAULT_WORKSPACE = 'default-main'

const activeWorkspaceId = ref(readStoredWorkspace())

function readStoredWorkspace(): string {
  if (typeof localStorage === 'undefined') return DEFAULT_WORKSPACE
  return localStorage.getItem(STORAGE_KEY) || DEFAULT_WORKSPACE
}

export function getActiveWorkspaceId(): string {
  return activeWorkspaceId.value || DEFAULT_WORKSPACE
}

export function setActiveWorkspaceId(workspaceId: string): void {
  const next = workspaceId.trim() || DEFAULT_WORKSPACE
  activeWorkspaceId.value = next
  if (typeof localStorage !== 'undefined') {
    localStorage.setItem(STORAGE_KEY, next)
  }
}

export function useWorkspace() {
  const workspaceId = computed(() => getActiveWorkspaceId())
  return {
    workspaceId,
    setWorkspaceId: setActiveWorkspaceId,
  }
}
