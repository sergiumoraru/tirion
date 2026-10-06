<template>
  <div class="max-w-7xl mx-auto p-4">
    <!-- Controls -->
    <div class="mb-6">
      <div class="flex gap-3">
        <input
          v-model="functionName"
          @keyup.enter="trace()"
          type="text"
          placeholder="Function name (e.g., ResourceService.load)"
          class="flex-1 px-4 py-3 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none focus:outline-none focus:border-accent text-black dark:text-n-300 placeholder-n-500 font-mono"
          autofocus
        />
        <button
          @click="trace()"
          :disabled="loading"
          class="px-6 py-3 bg-accent hover:bg-accent/80 disabled:opacity-50 disabled:cursor-not-allowed rounded-none font-medium text-white uppercase tracking-wide transition-colors"
        >
          {{ loading ? 'Tracing...' : 'Trace' }}
        </button>
      </div>

      <div class="mt-3">
        <button
          @click="showOptions = !showOptions"
          class="flex items-center gap-2 text-sm text-n-300 hover:text-black dark:hover:text-white"
        >
          <span class="text-n-500">{{ showOptions ? '▼' : '▶' }}</span>
          Trace options
        </button>
      </div>

      <div v-if="showOptions" class="mt-3 rounded-none border-2 border-black dark:border-n-600 bg-gray-50 dark:bg-n-900/40 p-3">
        <div class="flex flex-wrap gap-4 text-sm">
          <label class="flex items-center gap-2 text-n-400">
            <input type="checkbox" v-model="noTests" class="rounded-none bg-white dark:bg-n-800 border-gray-300 dark:border-n-600" />
            Hide tests
          </label>
          <label class="flex items-center gap-2 text-n-400">
            <input type="checkbox" v-model="resolveCalls" class="rounded-none bg-white dark:bg-n-800 border-gray-300 dark:border-n-600" />
            Resolve DI/impl
          </label>
          <label class="flex items-center gap-2 text-n-400">
            <input type="checkbox" v-model="hideModule" class="rounded-none bg-white dark:bg-n-800 border-gray-300 dark:border-n-600" />
            Hide file init (_module_)
          </label>
          <div class="flex items-center gap-2">
            <label class="text-n-400">Exclude:</label>
            <input
              v-model="excludeStr"
              type="text"
              placeholder="min.js,dist,build"
              class="w-64 px-2 py-1 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-black dark:text-n-300"
            />
          </div>
          <div class="flex items-start gap-2">
            <label class="text-n-400 mt-1">Repo scope:</label>
            <div class="space-y-2">
              <select
                v-model="selectedRepoScopes"
                multiple
                class="w-72 h-28 px-2 py-1 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-black dark:text-n-300"
              >
                <option v-for="repo in repoOptions" :key="repo" :value="repo">{{ repo }}</option>
              </select>
              <div class="flex items-center gap-2 text-xs text-n-500">
                <span v-if="selectedRepoScopes.length">{{ selectedRepoScopes.length }} repos selected</span>
                <span v-else>All repos</span>
                <button
                  v-if="selectedRepoScopes.length"
                  @click="clearRepoScope"
                  type="button"
                  class="px-2 py-0.5 border border-gray-300 dark:border-n-600 text-n-400 hover:bg-gray-100 dark:hover:bg-n-800"
                >
                  Clear
                </button>
              </div>
              <div class="text-xs text-n-500">
                Filters visible trace nodes to selected repos while keeping the traced root visible.
              </div>
            </div>
          </div>
          <div class="flex items-center gap-2">
            <label class="text-n-400">Depth:</label>
            <input
              type="number"
              v-model.number="depth"
              min="1"
              max="10"
              class="w-16 px-2 py-1 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-black dark:text-n-300"
            />
            <div class="relative group">
              <span class="inline-flex items-center justify-center w-4 h-4 border border-n-600 text-n-400 text-[10px]">
                i
              </span>
              <div class="absolute left-1/2 -translate-x-1/2 mt-2 w-52 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 text-n-300 text-xs px-2 py-1 opacity-0 group-hover:opacity-100 pointer-events-none">
                How many hops to expand from the root.
              </div>
            </div>
          </div>
          <div class="flex items-center gap-2">
            <label class="text-n-400">Max nodes:</label>
            <input
              type="number"
              v-model.number="maxNodes"
              min="100"
              max="5000"
              class="w-20 px-2 py-1 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-black dark:text-n-300"
            />
            <div class="relative group">
              <span class="inline-flex items-center justify-center w-4 h-4 border border-n-600 text-n-400 text-[10px]">
                i
              </span>
              <div class="absolute left-1/2 -translate-x-1/2 mt-2 w-56 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 text-n-300 text-xs px-2 py-1 opacity-0 group-hover:opacity-100 pointer-events-none">
                Hard cap per downstream/upstream tree returned.
              </div>
            </div>
          </div>
        </div>

        <div v-if="selectedRepoScopes.length" class="mt-3 flex flex-wrap gap-2 text-xs">
          <span class="text-n-500">Scoped repos:</span>
          <span
            v-for="repo in selectedRepoScopes"
            :key="repo"
            class="px-2 py-1 bg-gray-100 dark:bg-n-800 border border-gray-300 dark:border-n-600 text-n-300"
          >
            {{ repo }}
          </span>
        </div>

        <div class="flex flex-wrap gap-6 mt-3 text-xs text-n-400">
          <div class="flex items-center gap-2">
            <span class="text-n-500">Edges:</span>
            <label class="flex items-center gap-1 px-2 py-1 rounded-none bg-gray-100 dark:bg-n-800 text-n-300">
              <input type="checkbox" v-model="edgeTypeFilters.call" class="rounded-none border-n-600 bg-white dark:bg-n-900 accent-n-300" />
              Calls
            </label>
            <label class="flex items-center gap-1 px-2 py-1 rounded-none bg-blue-900/50 text-blue-300">
              <input type="checkbox" v-model="edgeTypeFilters.http" class="rounded-none border-blue-700 bg-blue-900/40 accent-blue-300" />
              HTTP
            </label>
            <label class="flex items-center gap-1 px-2 py-1 rounded-none bg-yellow-900/50 text-yellow-300">
              <input type="checkbox" v-model="edgeTypeFilters.sqs" class="rounded-none border-yellow-700 bg-yellow-900/40 accent-yellow-300" />
              SQS
            </label>
            <label class="flex items-center gap-1 px-2 py-1 rounded-none bg-orange-900/50 text-orange-300">
              <input type="checkbox" v-model="edgeTypeFilters.eventbridge" class="rounded-none border-orange-700 bg-orange-900/40 accent-orange-300" />
              EventBridge
            </label>
            <label class="flex items-center gap-1 px-2 py-1 rounded-none bg-purple-900/50 text-purple-300">
              <input type="checkbox" v-model="edgeTypeFilters.resolve" class="rounded-none border-purple-700 bg-purple-900/40 accent-purple-300" />
              Resolve
            </label>
          </div>
          <div class="flex items-center gap-2">
            <span class="text-n-500">Resolution:</span>
            <label class="flex items-center gap-1 px-2 py-1 rounded-none bg-emerald-900/50 text-emerald-300">
              <input type="checkbox" v-model="resolutionFilters.high" class="rounded-none border-emerald-700 bg-emerald-900/40 accent-emerald-300" />
              Direct
            </label>
            <label class="flex items-center gap-1 px-2 py-1 rounded-none bg-amber-900/50 text-amber-300">
              <input type="checkbox" v-model="resolutionFilters.medium" class="rounded-none border-amber-700 bg-amber-900/40 accent-amber-300" />
              Resolved
            </label>
            <label class="flex items-center gap-1 px-2 py-1 rounded-none bg-rose-900/50 text-rose-300">
              <input type="checkbox" v-model="resolutionFilters.low" class="rounded-none border-rose-700 bg-rose-900/40 accent-rose-300" />
              Name-based
            </label>
            <div class="relative group ml-1">
              <span class="inline-flex items-center justify-center w-4 h-4 border border-n-600 text-n-400 text-[10px]">
                i
              </span>
              <div
                class="absolute left-1/2 -translate-x-1/2 mt-2 w-72 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 text-n-300 text-xs px-2 py-1 opacity-0 group-hover:opacity-100 pointer-events-none"
              >
                <div class="space-y-1">
                  <div>Direct: exact call link from the code.</div>
                  <div>Resolved: inferred via DI/impl or HTTP/SQS match.</div>
                  <div>Name-based: matched by function name when no exact link exists.</div>
                  <div>HTTP/SQS badges mark cross-service edges.</div>
                  <div>Injected badge marks DI targets.</div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Loading -->
    <div v-if="loading" class="text-center py-12">
      <div class="animate-spin w-8 h-8 border-2 border-accent border-t-transparent mx-auto"></div>
      <p class="mt-3 text-n-500 uppercase text-xs tracking-widest">Tracing call chains...</p>
    </div>

    <!-- Error -->
    <div v-else-if="error" class="border-2 border-red-500 bg-red-50 dark:bg-red-950 rounded-none p-4 text-red-600 dark:text-red-400">
      {{ error }}
    </div>

    <!-- Results -->
    <div v-else-if="result">
      <div
        v-if="traceWarnings.length"
        class="mb-4 border-2 border-amber-500 bg-amber-50 dark:bg-amber-950 rounded-none p-4 text-amber-700 dark:text-amber-300"
      >
        <div class="flex flex-wrap items-start justify-between gap-3">
          <div>
            <div class="text-xs uppercase tracking-widest font-semibold mb-2">Trace Warnings</div>
            <div class="space-y-1 text-sm">
              <div v-for="warning in traceWarnings" :key="warning">{{ warning }}</div>
              <div v-if="result.completeness.truncated" class="text-xs text-amber-800/80 dark:text-amber-200/80">
                Current cap: {{ result.completeness.appliedMaxNodes }} nodes per upstream/downstream tree.
              </div>
            </div>
          </div>
          <div v-if="result.completeness.truncated" class="flex flex-wrap gap-2">
            <button
              @click="rerunWithMaxNodes(nextMaxNodes(2))"
              class="px-3 py-1.5 border-2 border-amber-600 text-amber-700 dark:text-amber-200 hover:bg-amber-100 dark:hover:bg-amber-900 rounded-none text-xs uppercase tracking-wide"
            >
              Retry {{ nextMaxNodes(2) }}
            </button>
            <button
              @click="rerunWithMaxNodes(nextMaxNodes(5))"
              class="px-3 py-1.5 border-2 border-amber-600 text-amber-700 dark:text-amber-200 hover:bg-amber-100 dark:hover:bg-amber-900 rounded-none text-xs uppercase tracking-wide"
            >
              Retry {{ nextMaxNodes(5) }}
            </button>
          </div>
        </div>
      </div>

      <!-- Match selector (if multiple) -->
      <div v-if="result.matches?.length > 1" class="mb-4 p-3 bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none">
        <div class="text-sm text-n-400 mb-3">
          Multiple matches found for <span class="font-mono text-n-300">{{ result.function }}</span>:
          same name across different files/repos. Pick one to trace.
        </div>
        <div class="space-y-3">
          <div v-for="(matches, repo) in matchesByRepo" :key="repo" class="flex flex-wrap items-center gap-2">
            <!-- Show repo header only if multiple matches in this repo -->
            <span v-if="matches.length > 1" class="text-xs text-n-500 w-full mb-0.5">{{ repo }}</span>
            <button
              v-for="match in matches"
              :key="match.qualified_id"
              @click="selectMatch(match.index)"
              :title="match.file"
              :class="[
                'px-3 py-1.5 rounded-none text-sm font-mono transition-colors',
                selectedMatch === match.index
                  ? 'bg-accent text-white'
                  : 'bg-gray-100 dark:bg-n-800 text-n-300 hover:bg-gray-200 dark:hover:bg-n-600 border border-gray-300 dark:border-n-600'
              ]"
            >
              <!-- Single match: show filename [repo], multiple: just filename -->
              {{ getFileName(match.file) }}<span v-if="matches.length === 1" class="text-xs opacity-70 ml-1">[{{ repo }}]</span>
            </button>
          </div>
        </div>
      </div>

      <!-- Stats -->
      <div class="flex items-center gap-6 mb-4 text-sm text-n-400">
        <span v-if="result.workspace">Workspace: {{ result.workspace.id }}</span>
        <span v-if="result.repoContext?.length">Indexed repos: {{ result.repoContext.length }}</span>
        <span>Downstream: {{ traceCountLabel('downstream') }}</span>
        <span>Upstream: {{ traceCountLabel('upstream') }}</span>
        <span>Time: {{ result.stats.traceTime }}</span>
        <span v-if="cacheHit" class="text-xs px-2 py-0.5 rounded-none bg-gray-100 dark:bg-n-800 text-n-300 border border-gray-300 dark:border-n-600">
          cached
        </span>
        <button
          v-if="cacheHit"
          @click="trace(selectedMatchInfo?.qualified_id, selectedMatch, true)"
          class="text-xs px-2 py-0.5 rounded-none border-2 border-black dark:border-n-600 text-n-300 hover:bg-gray-100 dark:hover:bg-n-800"
        >
          refresh
        </button>
      </div>

      <div
        v-if="traceInsights.length"
        class="mb-4 bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4"
      >
        <div class="text-xs text-n-500 uppercase tracking-widest mb-2">Trace Summary</div>
        <div class="space-y-1 text-sm text-n-300">
          <div v-for="insight in traceInsights" :key="insight">
            {{ insight }}
          </div>
        </div>
      </div>

      <div
        v-if="matchedClass || loadingClassIntegrations || classIntegrationError"
        class="mb-4 bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4"
      >
        <div class="text-xs text-n-500 uppercase tracking-widest mb-2">Matching Class</div>
        <div v-if="matchedClass" class="space-y-2">
          <div class="font-mono text-sm text-black dark:text-n-300">
            {{ matchedClass.name }}
          </div>
          <div class="text-xs text-n-500">
            {{ matchedClass.repo }} · {{ matchedClass.file }}:{{ matchedClass.startLine }}-{{ matchedClass.endLine }}
          </div>
        </div>
        <div v-if="loadingClassIntegrations" class="mt-3 text-sm text-n-500">
          Loading class integrations...
        </div>
        <div v-else-if="classIntegrationError" class="mt-3 text-sm text-amber-700 dark:text-amber-300">
          {{ classIntegrationError }}
        </div>
        <div v-else-if="classIntegrations.length" class="mt-3 space-y-2">
          <div
            v-for="integration in classIntegrations"
            :key="classIntegrationKey(integration)"
            class="border border-gray-300 dark:border-n-600 p-3"
          >
            <div class="font-mono text-black dark:text-n-300">
              {{ integration.method }} {{ integration.path }}
            </div>
            <div class="text-xs text-n-500 mt-1">
              {{ shortMethodName(integration.sourceFunction) }} · {{ integration.targetRepo }}
              <span v-if="integration.targetHandler"> · {{ integration.targetHandler }}</span>
              <span> · {{ integration.resolution === 'inferred' ? 'resolved from source' : integration.resolution === 'unresolved' ? 'unresolved' : 'directly matched' }}</span>
            </div>
          </div>
        </div>
        <div v-if="classHandledEndpoints.length" class="mt-3 space-y-2">
          <div class="text-xs text-n-500 uppercase tracking-widest">Handled Endpoints</div>
          <div
            v-for="endpoint in classHandledEndpoints"
            :key="classHandledEndpointKey(endpoint)"
            class="border border-gray-300 dark:border-n-600 p-3"
          >
            <div class="font-mono text-black dark:text-n-300">
              {{ endpoint.method }} {{ endpoint.path }}
            </div>
            <div class="text-xs text-n-500 mt-1">
              {{ endpoint.handler || matchedClass?.name }}
            </div>
          </div>
        </div>
      </div>

      <div
        v-if="methodSuggestions.length"
        class="mb-4 bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4"
      >
        <div class="text-xs text-n-500 uppercase tracking-widest mb-2">Matching Methods</div>
        <div class="text-sm text-n-500 mb-3">
          This input looks like a class/controller. Pick a method to trace.
        </div>
        <div class="space-y-2">
          <FunctionCard
            v-for="fn in methodSuggestions"
            :key="fn.id"
            :fn="fn"
            @trace="traceSuggestedMethod(fn)"
            @impact="openImpact(fn.name)"
          />
        </div>
      </div>

      <div
        v-if="rootIntegrations.length || loadingRootIntegrations || rootIntegrationError"
        class="mb-4 bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4"
      >
        <div class="text-xs text-n-500 uppercase tracking-widest mb-2">Connected API Calls</div>
        <div v-if="loadingRootIntegrations" class="text-sm text-n-500">
          Loading connected integrations...
        </div>
        <div v-else-if="rootIntegrationError" class="text-sm text-amber-700 dark:text-amber-300">
          {{ rootIntegrationError }}
        </div>
        <div v-else class="space-y-2">
          <div v-for="integration in rootIntegrations" :key="integrationKey(integration)" class="text-sm">
            <div class="font-mono text-black dark:text-n-300">
              {{ integration.method }} {{ integration.path }}
            </div>
            <div class="text-xs text-n-500 mt-0.5">
              {{ integration.targetRepo }}
              <span v-if="integration.targetHandler"> · {{ integration.targetHandler }}</span>
              <span> · {{ integration.resolution === 'inferred' ? 'resolved from source' : integration.resolution === 'unresolved' ? 'unresolved' : 'directly matched' }}</span>
            </div>
          </div>
        </div>
      </div>

      <!-- Two-column layout -->
      <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <!-- Downstream -->
        <div>
          <h2 class="text-lg font-bold uppercase tracking-wide mb-3 flex items-center gap-2">
            <span class="text-green-400">&#x2193;</span> Downstream
          </h2>
          <div class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4 overflow-auto max-h-[70vh]">
            <TraceTree
              v-if="downstreamNode"
              :node="downstreamNode"
              :depth="0"
              direction="downstream"
              :max-depth="depth"
              :load-children="loadChildren"
              :should-include-node="shouldIncludeNode"
              :hide-module="hideModule"
            />
            <div v-else class="text-n-500 text-sm">No downstream calls</div>
          </div>
        </div>

        <!-- Upstream -->
        <div>
          <h2 class="text-lg font-bold uppercase tracking-wide mb-3 flex items-center gap-2">
            <span class="text-accent">&#x2191;</span> Upstream
            <span v-if="upstreamSourceLabel" class="text-xs font-normal text-n-500">
              ({{ upstreamSourceLabel }})
            </span>
          </h2>
          <div class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4 overflow-auto max-h-[70vh]">
            <div v-if="upstreamRoots.length">
              <TraceNode
                v-for="(node, i) in upstreamRoots"
                :key="i"
                :node="node"
                :depth="0"
                direction="upstream"
                :max-depth="depth"
                :load-children="loadChildren"
                :should-include-node="shouldIncludeNode"
                :hide-module="hideModule"
              />
            </div>
            <div v-else class="text-n-500 text-sm">No upstream callers found</div>
          </div>
        </div>
      </div>
    </div>

    <!-- Initial state -->
    <div v-else class="text-center py-16 text-n-500">
      <p class="text-lg uppercase tracking-wide">Trace function call chains</p>
      <p class="mt-2 text-sm">Enter a function name to see upstream callers and downstream calls</p>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { api } from '@/api/client'
import type {
  ClassHandledEndpoint,
  ClassIntegration,
  ClassResult,
  FunctionIntegration,
  FunctionResult,
  Repo,
  TraceExpandResponse,
  TraceResponse,
  TraceNode as TraceNodeType,
} from '@/types'
import TraceTree from '@/components/trace/TraceTree.vue'
import TraceNode from '@/components/trace/TraceNode.vue'
import FunctionCard from '@/components/search/FunctionCard.vue'
import { useWorkspace } from '@/composables/useWorkspace'

const router = useRouter()
const route = useRoute()
const { workspaceId } = useWorkspace()
let traceRequest: AbortController | null = null

const functionName = ref('')
const noTests = ref(true)
const depth = ref(4)
const excludeStr = ref('min.js,dist,build,lib/')
const maxNodes = ref(2000)
const repoOptions = ref<string[]>([])
const selectedRepoScopes = ref<string[]>([])
const resolveCalls = ref(true)
const hideModule = ref(true)
const loading = ref(false)
const error = ref<string | null>(null)
const result = ref<TraceResponse | null>(null)
const selectedMatch = ref(0)
const traceCache = ref(new Map<string, TraceResponse>())
const TRACE_CACHE_VERSION = '2'
const cacheHit = ref(false)
const showOptions = ref(false)
const rootIntegrations = ref<FunctionIntegration[]>([])
const loadingRootIntegrations = ref(false)
const rootIntegrationError = ref('')
const methodSuggestions = ref<FunctionResult[]>([])
const matchedClass = ref<ClassResult | null>(null)
const classIntegrations = ref<ClassIntegration[]>([])
const classHandledEndpoints = ref<ClassHandledEndpoint[]>([])
const loadingClassIntegrations = ref(false)
const classIntegrationError = ref('')
const edgeTypeFilters = ref({
  call: true,
  http: true,
  sqs: true,
  eventbridge: true,
  resolve: true,
})
const resolutionFilters = ref({
  high: true,
  medium: true,
  low: true,
})


const exclude = computed(() =>
  excludeStr.value.split(',').map(s => s.trim()).filter(Boolean)
)

const normalizedRepoScope = computed(() =>
  [...selectedRepoScopes.value]
    .map((repo) => repo.trim())
    .filter(Boolean)
    .sort((a, b) => a.localeCompare(b))
)

const scopedRepoSet = computed(() => new Set(normalizedRepoScope.value))

// Group matches by repo, preserving original index
const matchesByRepo = computed(() => {
  if (!result.value?.matches) return {}
  const grouped: Record<string, Array<{ index: number; file: string; qualified_id: string }>> = {}
  result.value.matches.forEach((match, idx) => {
    if (!grouped[match.repo]) {
      grouped[match.repo] = []
    }
    grouped[match.repo].push({
      index: idx,
      file: match.file,
      qualified_id: match.qualified_id,
    })
  })
  return grouped
})

// Extract filename from path
function getFileName(filePath: string): string {
  const parts = filePath.split('/')
  return parts[parts.length - 1] || filePath
}

const downstreamNode = computed(() => {
  const res = result.value
  if (!res || !res.downstream?.length) return null
  if (res.downstream.length === res.matches.length) {
    return res.downstream[selectedMatch.value] ?? res.downstream[0]
  }
  return res.downstream[0]
})

function shouldIncludeNode(node: TraceResponse['downstream'][number], isRoot: boolean) {
  if (isRoot) return true
  if (scopedRepoSet.value.size > 0 && node.repo && !scopedRepoSet.value.has(node.repo)) {
    return false
  }
  if (node.edge_type && !edgeTypeFilters.value[node.edge_type]) {
    return false
  }
  if (node.confidence && !resolutionFilters.value[node.confidence]) {
    return false
  }
  return true
}

const upstreamRoots = computed(() => {
  const nodes = result.value?.upstream ?? []
  return nodes.filter((node) => shouldIncludeNode(node, false))
})

const upstreamSourceLabel = computed(() => {
  const source = result.value?.upstreamSource
  if (source === 'id') return 'resolved IDs'
  if (source === 'name') return 'name match'
  return ''
})

const traceWarnings = computed(() => result.value?.warnings ?? [])

function formatPct(value?: number): string {
  if (value == null || Number.isNaN(value)) return '0.0%'
  return `${(value * 100).toFixed(1)}%`
}

function edgeTypeLabel(type: string): string {
  if (type === 'http') return 'HTTP'
  if (type === 'sqs') return 'SQS'
  if (type === 'eventbridge') return 'EventBridge'
  if (type === 'resolve') return 'resolved'
  if (type === 'call') return 'call'
  return type
}

const traceInsights = computed(() => {
  const response = result.value
  if (!response) return [] as string[]
  const quality = response.quality?.downstream
  if (!quality) {
    return [
      `Tracing reached ${response.stats.downstreamNodes} downstream and ${response.stats.upstreamNodes} upstream nodes.`,
    ]
  }

  const edgeCounts = quality.edgeCounts || {}
  const dominantEdge = Object.entries(edgeCounts)
    .sort((a, b) => b[1] - a[1])[0]
  const dominantEdgeText = dominantEdge && dominantEdge[1] > 0
    ? `${edgeTypeLabel(dominantEdge[0])} edges dominate (${dominantEdge[1]}).`
    : 'No dominant edge type identified.'

  const lines: string[] = [
    `Graph breadth: ${response.stats.downstreamNodes} downstream, ${response.stats.upstreamNodes} upstream nodes.`,
    `Resolution quality: direct ${formatPct(quality.directRatio)}, inferred ${formatPct(quality.resolvedRatio)}, name-based ${formatPct(quality.nameBasedRatio)}.`,
    `${quality.crossServiceCount} cross-service edges detected; ${dominantEdgeText}`,
  ]
  return lines
})


const selectedMatchInfo = computed(() => {
  const res = result.value
  if (!res || !res.matches?.length) return null
  return res.matches[selectedMatch.value] ?? res.matches[0]
})

const rootIntegrationCallerId = computed(() => {
  if (selectedMatchInfo.value?.qualified_id) {
    return selectedMatchInfo.value.qualified_id
  }
  if (downstreamNode.value?.caller_id) {
    return downstreamNode.value.caller_id
  }
  return ''
})

function integrationKey(integration: FunctionIntegration): string {
  return [
    integration.method,
    integration.path,
    integration.targetRepo,
    integration.targetHandler ?? '',
    integration.lineNumber,
    integration.resolution ?? '',
  ].join('|')
}

function classIntegrationKey(integration: ClassIntegration): string {
  return [
    integration.sourceFunction,
    integration.method,
    integration.path,
    integration.targetRepo,
    integration.targetHandler ?? '',
    integration.lineNumber,
    integration.resolution,
  ].join('|')
}

function classHandledEndpointKey(endpoint: ClassHandledEndpoint): string {
  return [endpoint.method, endpoint.path, endpoint.handler ?? ''].join('|')
}

function shortMethodName(name: string): string {
  const parts = name.split('.')
  return parts[parts.length - 1] || name
}

function looksLikeClassInput(): boolean {
  return !functionName.value.includes('.')
}

function shouldLoadClassFallback(response: TraceResponse): boolean {
  return looksLikeClassInput() &&
    response.stats.downstreamNodes === 0 &&
    response.stats.upstreamNodes === 0
}

function resetClassFallbackState() {
  methodSuggestions.value = []
  matchedClass.value = null
  classIntegrations.value = []
  classHandledEndpoints.value = []
  classIntegrationError.value = ''
  loadingClassIntegrations.value = false
}

async function loadRootIntegrations() {
  const callerId = rootIntegrationCallerId.value
  const controller = traceRequest
  rootIntegrations.value = []
  rootIntegrationError.value = ''
  loadingRootIntegrations.value = false
  if (!callerId) {
    return
  }
  loadingRootIntegrations.value = true
  try {
    const response = await api.functionIntegrations(callerId, controller?.signal)
    if (controller !== traceRequest || controller?.signal.aborted || callerId !== rootIntegrationCallerId.value) return
    rootIntegrations.value = response.integrations ?? []
  } catch {
    if (controller !== traceRequest || controller?.signal.aborted || callerId !== rootIntegrationCallerId.value) return
    rootIntegrationError.value = 'Could not load connected integrations.'
  } finally {
    if (controller === traceRequest && callerId === rootIntegrationCallerId.value) loadingRootIntegrations.value = false
  }
}

function clearRepoScope() {
  selectedRepoScopes.value = []
}

function knownRootRepos(matchId?: string): string[] {
  const repos = new Set<string>()
  if (matchId) {
    const parts = matchId.split(':')
    if (parts[0]) repos.add(parts[0])
  }
  if (selectedMatchInfo.value?.repo) {
    repos.add(selectedMatchInfo.value.repo)
  }
  const activeResult = result.value
  if (activeResult && activeResult.function === functionName.value) {
    for (const match of activeResult.matches ?? []) {
      if (match.repo) repos.add(match.repo)
    }
  }
  return Array.from(repos)
}

function traceIncludeRepos(matchId?: string): string[] | undefined {
  if (normalizedRepoScope.value.length === 0) return undefined
  const repos = new Set(normalizedRepoScope.value)
  for (const repo of knownRootRepos(matchId)) {
    repos.add(repo)
  }
  return Array.from(repos).sort((a, b) => a.localeCompare(b))
}

async function loadChildren(node: TraceNodeType, direction: 'downstream' | 'upstream'): Promise<TraceExpandResponse> {
  if (!node.caller_id) {
    return {
      children: [],
      appliedMaxNodes: maxNodes.value,
      completeness: {
        returnedNodes: 0,
        availableNodes: 0,
        exactAvailableNodes: true,
        truncated: false,
      },
    }
  }
  return api.traceExpand({
    callerId: node.caller_id,
    direction,
    depth: node.depth ?? 0,
    maxDepth: depth.value,
    noTests: noTests.value,
    resolve: resolveCalls.value,
    exclude: exclude.value,
    includeRepos: normalizedRepoScope.value.length ? normalizedRepoScope.value : undefined,
    maxNodes: maxNodes.value,
  }, traceRequest?.signal)
}

async function trace(matchId?: string, matchIndex?: number, force = false) {
  if (!functionName.value.trim()) return
  traceRequest?.abort()
  const controller = new AbortController()
  traceRequest = controller
  loading.value = false
  error.value = null
  rootIntegrations.value = []
  rootIntegrationError.value = ''
  loadingRootIntegrations.value = false

  selectedMatch.value = matchIndex ?? 0
  resetClassFallbackState()
  if (typeof matchId !== 'string') {
    matchId = undefined
  }

  const cacheKey = buildCacheKey(matchId)
  const query = currentTraceQuery(matchId)
  if (!force) {
    const cached = traceCache.value.get(cacheKey)
    if (cached) {
      result.value = cached
      cacheHit.value = true
      void loadRootIntegrations()
      if (shouldLoadClassFallback(cached)) {
        void loadClassFallback()
      }
      return
    }
  }

  loading.value = true
  error.value = null
  cacheHit.value = false

  try {
    const response = await api.trace({
      function: functionName.value,
      match: matchId,
      depth: depth.value,
      noTests: noTests.value,
      resolve: resolveCalls.value,
      exclude: exclude.value,
      includeRepos: traceIncludeRepos(matchId),
      maxNodes: maxNodes.value,
    }, controller.signal)
    if (controller.signal.aborted || traceRequest !== controller) return
    result.value = response
    traceCache.value.set(cacheKey, response)
    void loadRootIntegrations()
    if (shouldLoadClassFallback(response)) {
      void loadClassFallback()
    }
    // Update URL
    router.replace({ query })
  } catch (e) {
    if (controller.signal.aborted || traceRequest !== controller) return
    error.value = e instanceof Error ? e.message : 'Trace failed'
  } finally {
    if (traceRequest === controller) loading.value = false
  }
}

async function loadClassFallback() {
  const controller = traceRequest
  const requestedName = functionName.value
  resetClassFallbackState()
  try {
    const response = await api.search(requestedName, 'keyword', 20, undefined, undefined, controller?.signal)
    if (controller !== traceRequest || controller?.signal.aborted) return
    methodSuggestions.value = (response.results.functions ?? []).filter((fn) =>
      fn.name.startsWith(`${requestedName}.`)
    )
    matchedClass.value =
      (response.results.classes ?? []).find((cls) => cls.name === requestedName) ??
      (response.results.classes ?? []).find((cls) => cls.name.endsWith(requestedName)) ??
      null
    if (matchedClass.value) {
      loadingClassIntegrations.value = true
      try {
        const integrationResponse = await api.classIntegrations(matchedClass.value.id, controller?.signal)
        if (controller !== traceRequest || controller?.signal.aborted) return
        classIntegrations.value = integrationResponse.integrations
        classHandledEndpoints.value = integrationResponse.handledEndpoints ?? []
        if (classIntegrations.value.length && methodSuggestions.value.length) {
          const integrationMethods = new Set(classIntegrations.value.map((item) => item.sourceFunction))
          methodSuggestions.value = [...methodSuggestions.value].sort((left, right) => {
            const leftHasIntegration = integrationMethods.has(left.name) ? 1 : 0
            const rightHasIntegration = integrationMethods.has(right.name) ? 1 : 0
            if (leftHasIntegration !== rightHasIntegration) {
              return rightHasIntegration - leftHasIntegration
            }
            return (right.richness ?? 0) - (left.richness ?? 0) || left.name.localeCompare(right.name)
          })
        }
      } catch {
        if (controller !== traceRequest || controller?.signal.aborted) return
        classIntegrationError.value = 'Could not load class integrations.'
      } finally {
        if (controller === traceRequest) loadingClassIntegrations.value = false
      }
    }
  } catch {
    if (controller === traceRequest && !controller?.signal.aborted) resetClassFallbackState()
  }
}

function openImpact(name: string) {
  router.push({ path: '/impact', query: { fn: name } })
}

function traceSuggestedMethod(fn: FunctionResult) {
  functionName.value = fn.name
  trace(fn.callerId, undefined, true)
}

function selectMatch(idx: number) {
  selectedMatch.value = idx
  if (result.value && result.value.matches.length > 1) {
    const matchId = result.value.matches[idx]?.qualified_id
    if (matchId) {
      trace(matchId, idx)
    }
  }
}

function buildCacheKey(matchId?: string): string {
  const normalizedExclude = [...exclude.value].sort().join('|')
  return [
    TRACE_CACHE_VERSION,
    workspaceId.value,
    functionName.value.trim(),
    String(depth.value),
    String(noTests.value),
    String(resolveCalls.value),
    String(maxNodes.value),
    normalizedExclude,
    normalizedRepoScope.value.join('|'),
    matchId ?? '',
  ].join('::')
}

function currentTraceQuery(matchId?: string) {
  return {
    fn: functionName.value,
    depth: String(depth.value),
    maxNodes: String(maxNodes.value),
    resolve: resolveCalls.value ? '1' : '0',
    noTests: noTests.value ? '1' : '0',
    exclude: excludeStr.value,
    ...(normalizedRepoScope.value.length ? { repos: normalizedRepoScope.value.join(',') } : {}),
    ...(matchId ? { match: matchId } : {}),
  }
}

function traceCountLabel(direction: 'downstream' | 'upstream') {
  const completeness = result.value?.completeness?.[direction]
  if (!completeness) return '0 nodes'
  if (completeness.truncated) {
    if (completeness.exactAvailableNodes) {
      return `${completeness.returnedNodes} shown / ${completeness.availableNodes} available`
    }
    return `${completeness.returnedNodes} shown / more than ${completeness.availableNodes} available`
  }
  return `${completeness.returnedNodes} nodes`
}

function nextMaxNodes(multiplier: number) {
  return Math.min(maxNodes.value * multiplier, 10000)
}

function rerunWithMaxNodes(nextValue: number) {
  maxNodes.value = nextValue
  const matchId = selectedMatchInfo.value?.qualified_id
  trace(matchId, selectedMatch.value, true)
}

async function loadRepoOptions() {
  try {
    const response = await api.listRepos()
    repoOptions.value = (response.repos ?? [])
      .map((repo: Repo) => repo.name)
      .filter(Boolean)
      .sort((a, b) => a.localeCompare(b))
  } catch {
    repoOptions.value = []
  }
}

onMounted(() => {
  void loadRepoOptions()
  const fn = route.query.fn as string
  const depthQuery = Number(route.query.depth)
  const maxNodesQuery = Number(route.query.maxNodes)
  const resolveQuery = route.query.resolve as string | undefined
  const noTestsQuery = route.query.noTests as string | undefined
  const excludeQuery = route.query.exclude as string | undefined
  const reposQuery = route.query.repos as string | undefined
  const matchQuery = route.query.match as string | undefined
  if (fn) {
    functionName.value = fn
    if (!Number.isNaN(depthQuery) && depthQuery > 0) depth.value = depthQuery
    if (!Number.isNaN(maxNodesQuery) && maxNodesQuery > 0) maxNodes.value = maxNodesQuery
    if (resolveQuery === '0' || resolveQuery === '1') resolveCalls.value = resolveQuery === '1'
    if (noTestsQuery === '0' || noTestsQuery === '1') noTests.value = noTestsQuery === '1'
    if (typeof excludeQuery === 'string') excludeStr.value = excludeQuery
    if (typeof reposQuery === 'string' && reposQuery.trim()) {
      selectedRepoScopes.value = reposQuery.split(',').map((repo) => repo.trim()).filter(Boolean)
    }
    trace(matchQuery)
  }
})

watch(workspaceId, () => {
  traceRequest?.abort()
  traceRequest = null
  traceCache.value.clear()
  result.value = null
  cacheHit.value = false
  loading.value = false
  error.value = null
  rootIntegrations.value = []
  rootIntegrationError.value = ''
  loadingRootIntegrations.value = false
  resetClassFallbackState()
  void loadRepoOptions()
})

onUnmounted(() => {
  traceRequest?.abort()
  traceRequest = null
})
</script>
