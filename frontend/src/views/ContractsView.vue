<template>
  <div class="max-w-7xl mx-auto p-4">
    <div class="mb-6">
      <div class="flex items-center gap-3">
        <input
          v-model="searchQuery"
          @input="queueSearch"
          @keyup.enter="refreshList"
          type="text"
          placeholder="Search repos..."
          class="flex-1 px-4 py-2 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none focus:outline-none focus:border-accent text-black dark:text-n-300 placeholder-n-500 font-mono"
        />
        <button
          @click="refreshList"
          :disabled="loadingList"
          class="px-3 py-2 bg-gray-100 dark:bg-n-800 hover:bg-gray-200 dark:hover:bg-n-600 disabled:opacity-50 rounded-none border-2 border-black dark:border-n-600 text-xs uppercase tracking-wide text-black dark:text-n-300"
        >
          {{ loadingList ? 'Loading...' : 'Refresh' }}
        </button>
      </div>
    </div>

    <div class="grid grid-cols-1 lg:grid-cols-[280px_minmax(0,1fr)] gap-6">
      <div class="border-2 border-black dark:border-n-600 rounded-none p-3">
        <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Services</div>
        <div v-if="loadingList" class="text-sm text-n-500">Loading...</div>
        <div v-else-if="services.length === 0" class="text-sm text-n-500">No services found.</div>
        <div v-else class="space-y-1">
          <button
            v-for="serviceItem in services"
            :key="serviceItem.repo"
            @click="selectService(serviceItem.repo)"
            class="w-full text-left px-3 py-2 border-2 rounded-none text-sm transition-colors"
            :class="serviceItem.repo === selectedRepo
              ? 'bg-accent border-accent text-white'
              : 'border-transparent hover:border-n-600 text-n-300'"
          >
            <div class="font-mono">{{ serviceItem.repo }}</div>
            <div class="text-xs text-n-500 mt-1">
              endpoints {{ serviceItem.endpointCount }} · http {{ serviceItem.httpCallCount }} ·
              gql {{ summaryGraphQLCount(serviceItem) }} · data {{ serviceItem.dataAccessCount ?? 0 }} ·
              queues {{ serviceItem.queuesProduced }}/{{ serviceItem.queuesConsumed }}
              <span v-if="serviceItem.azureTimerTriggers"> · timers {{ serviceItem.azureTimerTriggers }}</span>
            </div>
          </button>
        </div>
      </div>

      <div>
        <div v-if="loadingDetail" class="text-center py-12">
          <div class="animate-spin w-8 h-8 border-2 border-accent border-t-transparent mx-auto"></div>
          <p class="mt-3 text-n-500 uppercase text-xs tracking-widest">Loading service contract...</p>
        </div>

        <div v-else-if="detailError" class="border-2 border-red-500 bg-red-50 dark:bg-red-950 rounded-none p-4 text-red-600 dark:text-red-400">
          {{ detailError }}
        </div>

        <div v-else-if="service">
          <div class="flex flex-wrap items-center justify-between gap-3 mb-4">
            <div>
              <div class="text-lg font-bold uppercase tracking-wide">{{ service.repo }}</div>
              <div class="text-xs text-n-500 uppercase tracking-widest">
                Service contract snapshot
                <span v-if="detailWorkspaceId"> · workspace {{ detailWorkspaceId }}</span>
                <span v-if="detailRepoContextCount"> · {{ detailRepoContextCount }} indexed repos</span>
              </div>
            </div>
            <button
              @click="downloadJson"
              class="px-3 py-1.5 border-2 border-black dark:border-n-600 rounded-none text-xs uppercase tracking-wide hover:bg-gray-100 dark:hover:bg-n-800"
            >
              Download JSON
            </button>
          </div>

          <div class="grid grid-cols-1 xl:grid-cols-2 gap-6">
            <div class="border-2 border-black dark:border-n-600 rounded-none p-5">
              <h2 class="text-lg font-bold uppercase tracking-wide mb-1">Endpoints</h2>
              <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Inbound HTTP handlers.</div>
              <div v-if="!service.endpoints?.length" class="text-n-500 text-sm">
                No endpoints found.
              </div>
              <div v-else class="space-y-2">
                <div
                  v-for="endpoint in service.endpoints"
                  :key="endpoint.method + endpoint.path + endpoint.file"
                  class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-800"
                >
                  <div class="flex items-start justify-between gap-2">
                    <span class="font-mono break-words flex-1 min-w-0">
                      {{ endpoint.method }} {{ endpoint.path }}
                    </span>
                    <button
                      v-if="endpoint.handler"
                      @click="openTrace(endpoint.handler)"
                      class="text-accent hover:underline uppercase text-xs shrink-0"
                    >
                      Trace
                    </button>
                  </div>
                  <div class="text-xs text-n-500 mt-1 break-words whitespace-normal">
                    {{ endpoint.handler }} · {{ endpoint.file }}<span v-if="endpoint.line">:{{ endpoint.line }}</span>
                  </div>
                  <div v-if="endpoint.owners?.length" class="text-xs text-n-500 mt-1">
                    Owners: {{ endpoint.owners.join(', ') }}
                  </div>
                </div>
              </div>
            </div>

            <div class="border-2 border-black dark:border-n-600 rounded-none p-5">
              <h2 class="text-lg font-bold uppercase tracking-wide mb-1">Outbound HTTP</h2>
              <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Client calls made by this service.</div>
              <div v-if="!service.httpCalls?.length" class="text-n-500 text-sm">
                No HTTP calls found.
              </div>
              <div v-else class="space-y-2">
                <div
                  v-for="call in service.httpCalls"
                  :key="call.method + call.path"
                  class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-800"
                >
                  <div class="flex items-start justify-between gap-2">
                    <span class="font-mono break-words flex-1 min-w-0">
                      {{ call.method }} {{ formatPath(call.path) }}
                    </span>
                    <span class="text-xs text-n-500 shrink-0">
                      {{ call.count }} call{{ call.count === 1 ? '' : 's' }}
                    </span>
                  </div>
                  <div class="text-xs text-n-500 mt-1">
                    <span v-if="call.clientType">client {{ call.clientType }}</span>
                    <span v-if="call.external" class="ml-2 text-rose-500">external</span>
                  </div>
                  <div v-if="call.matches?.length" class="text-xs text-n-500 mt-2">
                    <div class="uppercase tracking-widest">matches:</div>
                    <ul class="mt-2 space-y-2">
                      <li
                        v-for="match in visibleMatches(call)"
                        :key="match.repo + match.handler + match.path"
                        class="border border-gray-300 dark:border-n-600 rounded-none bg-gray-100 dark:bg-n-900 px-2 py-1"
                      >
                        <div class="font-mono text-xs text-n-300 break-words">
                          {{ match.repo }}.{{ match.handler }}
                        </div>
                        <div v-if="match.path && match.path !== '/'" class="font-mono text-xs text-n-500 break-words">
                          path {{ formatPath(match.path) }}
                        </div>
                      </li>
                    </ul>
                    <div v-if="hiddenMatchCount(call) > 0" class="mt-1 text-xs text-n-500">
                      +{{ hiddenMatchCount(call) }} more matches hidden
                    </div>
                  </div>
                </div>
              </div>
            </div>

            <div class="border-2 border-black dark:border-n-600 rounded-none p-5">
              <h2 class="text-lg font-bold uppercase tracking-wide mb-1">HTTP Targets</h2>
              <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Repos this service calls (best-effort match).</div>
              <div v-if="!service.httpTargets || service.httpTargets.length === 0" class="text-n-500 text-sm">
                No matched targets.
              </div>
              <div v-else class="space-y-2">
                <div
                  v-for="target in service.httpTargets"
                  :key="target.repo"
                  class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-800"
                >
                  <div class="flex items-start justify-between gap-2">
                    <span class="font-mono break-words flex-1 min-w-0">{{ target.repo }}</span>
                    <span class="text-xs text-n-500 shrink-0">
                      {{ target.count }} call{{ target.count === 1 ? '' : 's' }}
                    </span>
                  </div>
                </div>
              </div>
            </div>

            <div class="border-2 border-black dark:border-n-600 rounded-none p-5">
              <h2 class="text-lg font-bold uppercase tracking-wide mb-1">HTTP Callers</h2>
              <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Repos calling this service (best-effort match).</div>
              <div v-if="!service.httpCallers || service.httpCallers.length === 0" class="text-n-500 text-sm">
                No callers found.
              </div>
              <div v-else class="space-y-2">
                <div
                  v-for="caller in service.httpCallers"
                  :key="caller.repo"
                  class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-800"
                >
                  <div class="flex items-start justify-between gap-2">
                    <span class="font-mono break-words flex-1 min-w-0">{{ caller.repo }}</span>
                    <span class="text-xs text-n-500 shrink-0">
                      {{ caller.count }} call{{ caller.count === 1 ? '' : 's' }}
                    </span>
                  </div>
                </div>
              </div>
            </div>

            <div class="border-2 border-black dark:border-n-600 rounded-none p-5">
              <h2 class="text-lg font-bold uppercase tracking-wide mb-1">GraphQL Operations</h2>
              <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Schema operations owned by this service.</div>
              <div v-if="!service.graphqlOperations?.length" class="text-n-500 text-sm">
                No GraphQL operations found.
              </div>
              <div v-else class="space-y-2">
                <div
                  v-for="operation in service.graphqlOperations"
                  :key="operation.type + operation.name + operation.file"
                  class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-800"
                >
                  <div class="flex items-start justify-between gap-2">
                    <span class="font-mono break-words flex-1 min-w-0">{{ operation.name }}</span>
                    <span class="text-xs uppercase text-n-500 shrink-0">{{ operation.type }}</span>
                  </div>
                  <div class="text-xs text-n-500 mt-1 break-words whitespace-normal">
                    {{ formatLocation(operation.file, operation.line) }}
                  </div>
                </div>
              </div>
            </div>

            <div class="border-2 border-black dark:border-n-600 rounded-none p-5">
              <h2 class="text-lg font-bold uppercase tracking-wide mb-1">GraphQL Usages</h2>
              <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Generated clients and operation imports.</div>
              <div v-if="!service.graphqlUsages?.length" class="text-n-500 text-sm">
                No GraphQL usages found.
              </div>
              <div v-else class="space-y-2">
                <div
                  v-for="usage in service.graphqlUsages"
                  :key="usage.importedAs + usage.importPath + usage.file"
                  class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-800"
                >
                  <div class="font-mono break-words">{{ usage.importedAs || usage.importPath }}</div>
                  <div class="text-xs text-n-500 mt-1 break-words">
                    <span v-if="usage.caller">caller {{ usage.caller }} · </span>{{ usage.importPath }}
                  </div>
                  <div class="text-xs text-n-500 mt-1 break-words whitespace-normal">
                    {{ formatLocation(usage.file, usage.line) }}
                  </div>
                </div>
              </div>
            </div>

            <div class="border-2 border-black dark:border-n-600 rounded-none p-5">
              <h2 class="text-lg font-bold uppercase tracking-wide mb-1">GraphQL Resolvers</h2>
              <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Backend resolver bindings.</div>
              <div v-if="!service.graphqlResolvers?.length" class="text-n-500 text-sm">
                No GraphQL resolvers found.
              </div>
              <div v-else class="space-y-2">
                <div
                  v-for="resolver in service.graphqlResolvers"
                  :key="resolver.operationType + resolver.operationName + resolver.resolver"
                  class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-800"
                >
                  <div class="font-mono break-words">
                    {{ resolver.operationType }} {{ resolver.operationName }}
                  </div>
                  <div class="text-xs text-n-500 mt-1 break-words">
                    resolver {{ resolver.resolver }}
                  </div>
                  <div class="text-xs text-n-500 mt-1 break-words whitespace-normal">
                    {{ formatLocation(resolver.file, resolver.line) }}
                  </div>
                </div>
              </div>
            </div>

            <div class="border-2 border-black dark:border-n-600 rounded-none p-5">
              <h2 class="text-lg font-bold uppercase tracking-wide mb-1">GraphQL Permissions</h2>
              <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Authorization rules attached to operations.</div>
              <div v-if="!service.graphqlPermissions?.length" class="text-n-500 text-sm">
                No GraphQL permissions found.
              </div>
              <div v-else class="space-y-2">
                <div
                  v-for="permission in service.graphqlPermissions"
                  :key="permission.operationType + permission.operationName + permission.ruleExpression"
                  class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-800"
                >
                  <div class="font-mono break-words">
                    {{ permission.operationType }} {{ permission.operationName }}
                  </div>
                  <div class="text-xs text-n-500 mt-1 break-words">
                    {{ permission.ruleExpression }}
                  </div>
                  <div class="text-xs text-n-500 mt-1 break-words whitespace-normal">
                    {{ formatLocation(permission.file, permission.line) }}
                  </div>
                </div>
              </div>
            </div>

            <div class="border-2 border-black dark:border-n-600 rounded-none p-5">
              <h2 class="text-lg font-bold uppercase tracking-wide mb-1">GraphQL Entrypoints</h2>
              <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Backend GraphQL host registrations.</div>
              <div v-if="!service.graphqlEntrypoints?.length" class="text-n-500 text-sm">
                No GraphQL entrypoints found.
              </div>
              <div v-else class="space-y-2">
                <div
                  v-for="entrypoint in service.graphqlEntrypoints"
                  :key="entrypoint.registrationKind + entrypoint.file + entrypoint.line"
                  class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-800"
                >
                  <div class="flex items-start justify-between gap-2">
                    <span class="font-mono break-words flex-1 min-w-0">
                      {{ entrypoint.handlerName || entrypoint.controllersPath || entrypoint.registrationKind }}
                    </span>
                    <span class="text-xs uppercase text-n-500 shrink-0">{{ entrypoint.registrationKind }}</span>
                  </div>
                  <div class="text-xs text-n-500 mt-1 break-words whitespace-normal">
                    {{ formatLocation(entrypoint.file, entrypoint.line) }}
                  </div>
                </div>
              </div>
            </div>

            <div class="border-2 border-black dark:border-n-600 rounded-none p-5">
              <h2 class="text-lg font-bold uppercase tracking-wide mb-1">Data Access</h2>
              <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Database entities and side effects.</div>
              <div v-if="!service.dataAccesses?.length" class="text-n-500 text-sm">
                No data accesses found.
              </div>
              <div v-else class="space-y-2">
                <div
                  v-for="access in service.dataAccesses"
                  :key="access.caller + access.entity + access.access + access.line"
                  class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-800"
                >
                  <div class="flex items-start justify-between gap-2">
                    <span class="font-mono break-words flex-1 min-w-0">{{ access.entity }}</span>
                    <span class="text-xs uppercase text-n-500 shrink-0">{{ access.access }}</span>
                  </div>
                  <div class="text-xs text-n-500 mt-1 break-words">
                    caller {{ access.caller }}
                  </div>
                  <div class="text-xs text-n-500 mt-1 break-words whitespace-normal">
                    {{ formatLocation(access.file, access.line) }}
                  </div>
                </div>
              </div>
            </div>

            <div class="border-2 border-black dark:border-n-600 rounded-none p-5">
              <h2 class="text-lg font-bold uppercase tracking-wide mb-1">EventBridge Triggers</h2>
              <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Scheduled EventBridge rules.</div>
              <div v-if="!service.eventBridgeTriggers?.length" class="text-n-500 text-sm">
                No EventBridge triggers found.
              </div>
              <div v-else class="space-y-2">
                <div
                  v-for="trigger in service.eventBridgeTriggers"
                  :key="trigger.ruleName + trigger.scheduleExpression + trigger.targetQueue"
                  class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-800"
                >
                  <div class="font-mono break-words">{{ trigger.ruleName || trigger.targetQueue }}</div>
                  <div class="text-xs text-n-500 mt-1 break-words">
                    {{ trigger.scheduleExpression }}<span v-if="trigger.targetQueue"> · target {{ trigger.targetQueue }}</span>
                  </div>
                  <div v-if="trigger.file" class="text-xs text-n-500 mt-1 break-words whitespace-normal">
                    {{ formatLocation(trigger.file, trigger.line) }}
                  </div>
                </div>
              </div>
            </div>

            <div class="border-2 border-black dark:border-n-600 rounded-none p-5">
              <h2 class="text-lg font-bold uppercase tracking-wide mb-1">Azure Timer Triggers</h2>
              <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Timer-triggered Azure Functions.</div>
              <div v-if="!service.azureTimerTriggers?.length" class="text-n-500 text-sm">
                No Azure timer triggers found.
              </div>
              <div v-else class="space-y-2">
                <div
                  v-for="trigger in service.azureTimerTriggers"
                  :key="trigger.functionName + trigger.scheduleExpression + trigger.file"
                  class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-800"
                >
                  <div class="font-mono break-words">{{ trigger.functionName || trigger.ruleName }}</div>
                  <div class="text-xs text-n-500 mt-1 break-words">
                    {{ trigger.scheduleExpression }}<span v-if="trigger.source"> · {{ trigger.source }}</span>
                  </div>
                  <div v-if="trigger.file" class="text-xs text-n-500 mt-1 break-words whitespace-normal">
                    {{ formatLocation(trigger.file, trigger.line) }}
                  </div>
                </div>
              </div>
            </div>

            <div class="border-2 border-black dark:border-n-600 rounded-none p-5">
              <h2 class="text-lg font-bold uppercase tracking-wide mb-1">Queues Produced</h2>
              <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Queues this service publishes to.</div>
              <div v-if="!service.queuesProduced?.length" class="text-n-500 text-sm">
                No queues produced.
              </div>
              <div v-else class="space-y-2">
                <div
                  v-for="queue in service.queuesProduced"
                  :key="queue.name"
                  class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-800"
                >
                  <div class="flex items-center justify-between">
                    <span class="font-mono">{{ queue.name }}</span>
                    <span class="text-xs text-n-500">{{ queue.count }}</span>
                  </div>
                  <div v-if="queue.counterparties?.length" class="text-xs text-n-500 mt-1">
                    Consumers: {{ queue.counterparties.join(', ') }}
                  </div>
                </div>
              </div>
            </div>

            <div class="border-2 border-black dark:border-n-600 rounded-none p-5">
              <h2 class="text-lg font-bold uppercase tracking-wide mb-1">Queues Consumed</h2>
              <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Queues this service consumes.</div>
              <div v-if="!service.queuesConsumed?.length" class="text-n-500 text-sm">
                No queues consumed.
              </div>
              <div v-else class="space-y-2">
                <div
                  v-for="queue in service.queuesConsumed"
                  :key="queue.name"
                  class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-800"
                >
                  <div class="flex items-center justify-between">
                    <span class="font-mono">{{ queue.name }}</span>
                    <span class="text-xs text-n-500">{{ queue.count }}</span>
                  </div>
                  <div v-if="queue.counterparties?.length" class="text-xs text-n-500 mt-1">
                    Producers: {{ queue.counterparties.join(', ') }}
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>

        <div v-else class="text-center py-12 text-n-500 uppercase tracking-widest text-xs">
          Select a service to view its contract.
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { api, ApiError } from '@/api/client'
import type { ContractDetail, ContractSummary, ContractHttpCall, ContractsResponse } from '@/types'

const router = useRouter()

const services = ref<ContractSummary[]>([])
const service = ref<ContractDetail | null>(null)
const detailResponse = ref<ContractsResponse | null>(null)
const selectedRepo = ref('')
const loadingList = ref(false)
const loadingDetail = ref(false)
const detailError = ref('')
const searchQuery = ref('')
let searchTimer: number | null = null
const detailWorkspaceId = computed(() => detailResponse.value?.workspace?.id ?? '')
const detailRepoContextCount = computed(() => detailResponse.value?.repoContext?.length ?? 0)

async function loadServices(query?: string) {
  loadingList.value = true
  try {
    const response = await api.listContracts({ q: query })
    services.value = response.services ?? []
  } finally {
    loadingList.value = false
  }
}

function queueSearch() {
  if (searchTimer) {
    clearTimeout(searchTimer)
  }
  searchTimer = window.setTimeout(() => {
    loadServices(searchQuery.value.trim())
  }, 250)
}

function refreshList() {
  loadServices(searchQuery.value.trim())
}

async function selectService(repo: string) {
  selectedRepo.value = repo
  loadingDetail.value = true
  detailError.value = ''
  detailResponse.value = null
  try {
    const response = await api.getContract(repo)
    detailResponse.value = response
    service.value = response.service ?? null
  } catch (err) {
    if (err instanceof ApiError) {
      detailError.value = err.message
    } else {
      detailError.value = 'Failed to load contract.'
    }
  } finally {
    loadingDetail.value = false
  }
}

function downloadJson() {
  if (!service.value) return
  const blob = new Blob([JSON.stringify(service.value, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = `${service.value.repo}-contract.json`
  link.click()
  URL.revokeObjectURL(url)
}

function openTrace(functionName: string) {
  if (!functionName) return
  router.push({ name: 'trace', query: { fn: functionName } })
}

function formatPath(path: string) {
  if (!path) return ''
  let normalized = path.replace(/#:/g, '/:').replace(/#\/?/g, '/')
  const queryIdx = normalized.indexOf('?')
  if (queryIdx >= 0) {
    normalized = normalized.slice(0, queryIdx)
  }
  return normalized
}

function summaryGraphQLCount(summary: ContractSummary) {
  return (summary.graphqlOperations ?? 0) +
    (summary.graphqlUsages ?? 0) +
    (summary.graphqlResolvers ?? 0) +
    (summary.graphqlPermissions ?? 0) +
    (summary.graphqlEntrypoints ?? 0)
}

function formatLocation(file?: string, line?: number) {
  if (!file) return ''
  return line ? `${file}:${line}` : file
}

function visibleMatches(call: ContractHttpCall) {
  if (!call.matches) return []
  const filtered = call.matches.filter((match) => match.path && match.path !== '/')
  const matches = filtered.length > 0 ? filtered : call.matches
  return matches.slice(0, 3)
}

function hiddenMatchCount(call: ContractHttpCall) {
  if (!call.matches) return 0
  const filtered = call.matches.filter((match) => match.path && match.path !== '/')
  const matches = filtered.length > 0 ? filtered : call.matches
  return Math.max(matches.length - 3, 0)
}


onMounted(() => {
  loadServices()
})
</script>
