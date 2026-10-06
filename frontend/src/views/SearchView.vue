<template>
  <div class="max-w-5xl mx-auto p-4">
    <!-- Search Bar -->
    <div class="mb-6">
      <div class="flex gap-3">
        <input
          v-model="query"
          @keyup.enter="performSearch"
          type="text"
          placeholder="Search functions, classes, endpoints, data entities, external symbols..."
          class="flex-1 px-4 py-3 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none focus:outline-none focus:border-accent text-black dark:text-n-300 placeholder-n-500 font-mono"
          autofocus
        />
        <select
          v-model="mode"
          class="px-4 py-3 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-black dark:text-n-300 focus:outline-none focus:border-accent"
        >
          <option value="keyword">Keyword</option>
          <option value="trigram">Fuzzy</option>
        </select>
        <button
          @click="performSearch"
          :disabled="loading"
          class="px-6 py-3 bg-accent hover:bg-accent/80 disabled:opacity-50 disabled:cursor-not-allowed rounded-none font-medium text-white uppercase tracking-wide transition-colors"
        >
          {{ loading ? 'Searching...' : 'Search' }}
        </button>
      </div>

      <!-- Filters -->
      <div class="flex gap-4 mt-3 text-sm">
        <div class="flex items-center gap-2">
          <label class="text-n-400 uppercase text-xs tracking-wide">Repo:</label>
          <select
            v-model="repoFilter"
            class="px-3 py-1.5 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-black dark:text-n-300 min-w-56"
          >
            <option value="">All repos</option>
            <option v-for="repo in repoOptions" :key="repo" :value="repo">{{ repo }}</option>
          </select>
        </div>
        <div class="flex items-center gap-2">
          <label class="text-n-400 uppercase text-xs tracking-wide">Limit:</label>
          <input
            v-model.number="limit"
            type="number"
            min="1"
            max="50"
            class="px-3 py-1.5 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-black dark:text-n-300 w-20"
          />
        </div>
      </div>
    </div>

    <!-- Loading -->
    <div v-if="loading" class="text-center py-12">
      <div class="animate-spin w-8 h-8 border-2 border-accent border-t-transparent mx-auto"></div>
      <p class="mt-3 text-n-500 uppercase text-xs tracking-widest">Searching...</p>
    </div>

    <!-- Error -->
    <div v-else-if="error" class="border-2 border-red-500 bg-red-50 dark:bg-red-950 rounded-none p-4 text-red-600 dark:text-red-400">
      {{ error }}
    </div>

    <!-- Results -->
    <div v-else-if="results">
      <div v-if="integrationLoadError" class="mb-4 flex flex-wrap items-center gap-3 text-sm text-amber-700 dark:text-amber-300" role="status">
        <span>{{ integrationLoadError }}</span>
        <button class="underline disabled:opacity-50" :disabled="integrationLoading" @click="hydrateVisibleSearchIntegrations(results)">
          {{ integrationLoading ? 'Retrying...' : 'Retry integrations' }}
        </button>
      </div>
      <!-- Stats -->
      <div class="flex items-start justify-between mb-4 text-sm text-n-500 gap-4">
        <div class="space-y-1">
          <div>{{ searchStatsLabel() }}</div>
          <div v-if="results.workspace" class="text-xs font-mono">
            Workspace {{ results.workspace.id }}
            <span v-if="results.repoContext?.length"> · {{ results.repoContext.length }} indexed repos</span>
          </div>
          <div v-if="results.stats.truncated" class="text-xs text-n-500">
            More results are available. Use the section pagers below.
          </div>
        </div>
        <span v-if="results.warnings?.length" class="text-yellow-500">
          {{ results.warnings.join(', ') }}
        </span>
      </div>

      <div v-if="results.workspaceHints?.length" class="mb-4 border-2 border-amber-500/60 bg-amber-50 dark:bg-amber-950/30 p-3 text-xs">
        <div class="font-bold uppercase tracking-wide text-amber-800 dark:text-amber-300">Found in another workspace</div>
        <div v-for="hint in results.workspaceHints" :key="hint.kind + hint.query" class="mt-2 space-y-1 font-mono text-n-700 dark:text-n-300">
          <div v-for="match in hint.foundIn" :key="[match.workspaceId, match.repo, match.sha, match.file, match.symbol].join('|')">
            {{ match.workspaceId }} · {{ match.repo }} · {{ match.branch }} @ {{ match.sha.slice(0, 8) }}
            <span v-if="match.symbol"> · {{ match.symbol }}</span>
          </div>
        </div>
      </div>

      <!-- Schedules -->
      <section v-if="results.results?.schedules?.length" class="mb-8">
        <h2 class="text-lg font-bold uppercase tracking-wide mb-3 flex items-center gap-2">
          <span class="w-2 h-2 bg-orange-500"></span>
          EventBridge Schedules
          <span class="text-sm font-normal text-n-500">({{ bucketCountLabel('schedules') }})</span>
        </h2>
        <div class="space-y-2">
          <div
            v-for="sched in results.results.schedules"
            :key="sched.id"
            class="p-3 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600"
          >
            <div class="flex items-center justify-between">
              <div class="font-mono font-bold text-sm text-black dark:text-n-200">{{ sched.ruleName }}</div>
              <span
                class="text-xs px-2 py-0.5 font-mono uppercase tracking-wide"
                :class="sched.state === 'ENABLED' ? 'bg-green-100 text-green-700 dark:bg-green-900 dark:text-green-300' : 'bg-n-200 text-n-600 dark:bg-n-700 dark:text-n-400'"
              >{{ sched.state }}</span>
            </div>
            <div class="mt-1 text-xs text-n-500 font-mono">
              {{ sched.scheduleExpression }} → {{ sched.targetType.toUpperCase() }}:{{ sched.targetName }}
            </div>
          </div>
        </div>
        <div class="mt-3 flex items-center justify-between text-xs text-n-500">
          <span>{{ bucketRangeLabel('schedules') }}</span>
          <div class="flex items-center gap-2">
            <button
              @click="pageBucket('schedules', -1)"
              :disabled="!canPageBucketPrev('schedules') || loading"
              class="px-2 py-1 border border-gray-300 dark:border-n-600 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800"
            >
              Prev
            </button>
            <button
              @click="pageBucket('schedules', 1)"
              :disabled="!canPageBucketNext('schedules') || loading"
              class="px-2 py-1 border border-gray-300 dark:border-n-600 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800"
            >
              Next
            </button>
          </div>
        </div>
      </section>

      <!-- Functions -->
      <section v-if="results.results?.functions?.length" class="mb-8">
        <h2 class="text-lg font-bold uppercase tracking-wide mb-3 flex items-center gap-2">
          <span class="w-2 h-2 bg-accent"></span>
          Functions
          <span class="text-sm font-normal text-n-500">({{ bucketCountLabel('functions') }})</span>
        </h2>
        <div class="space-y-2">
          <FunctionCard
            v-for="fn in results.results.functions"
            :key="fn.id"
            :fn="fn"
            :snapshot-label="snapshotLabel(fn.snapshotId, fn.repo)"
            :prefetched-integrations="fn.callerId ? functionIntegrationMap[fn.callerId] : undefined"
            @trace="openTrace(fn.name)"
            @impact="openImpact(fn.name)"
          />
        </div>
        <div class="mt-3 flex items-center justify-between text-xs text-n-500">
          <span>{{ bucketRangeLabel('functions') }}</span>
          <div class="flex items-center gap-2">
            <button
              @click="pageBucket('functions', -1)"
              :disabled="!canPageBucketPrev('functions') || loading"
              class="px-2 py-1 border border-gray-300 dark:border-n-600 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800"
            >
              Prev
            </button>
            <button
              @click="pageBucket('functions', 1)"
              :disabled="!canPageBucketNext('functions') || loading"
              class="px-2 py-1 border border-gray-300 dark:border-n-600 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800"
            >
              Next
            </button>
          </div>
        </div>
      </section>

      <!-- Data Entities -->
      <section v-if="results.results?.dataEntities?.length" class="mb-8">
        <h2 class="text-lg font-bold uppercase tracking-wide mb-3 flex items-center gap-2">
          <span class="w-2 h-2 bg-accent"></span>
          Data Entities
          <span class="text-sm font-normal text-n-500">({{ bucketCountLabel('dataEntities') }})</span>
        </h2>
        <div class="space-y-2">
          <DataEntityCard
            v-for="entity in results.results.dataEntities"
            :key="entity.id"
            :entity="entity"
            :snapshot-label="snapshotLabel(entity.snapshotId, entity.repo)"
            @trace="openTrace(entity.caller || entity.name)"
            @impact="openImpact(entity.caller || entity.name)"
          />
        </div>
        <div class="mt-3 flex items-center justify-between text-xs text-n-500">
          <span>{{ bucketRangeLabel('dataEntities') }}</span>
          <div class="flex items-center gap-2">
            <button
              @click="pageBucket('dataEntities', -1)"
              :disabled="!canPageBucketPrev('dataEntities') || loading"
              class="px-2 py-1 border border-gray-300 dark:border-n-600 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800"
            >
              Prev
            </button>
            <button
              @click="pageBucket('dataEntities', 1)"
              :disabled="!canPageBucketNext('dataEntities') || loading"
              class="px-2 py-1 border border-gray-300 dark:border-n-600 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800"
            >
              Next
            </button>
          </div>
        </div>
      </section>

      <!-- External Symbols -->
      <section v-if="results.results?.externalSymbols?.length" class="mb-8">
        <h2 class="text-lg font-bold uppercase tracking-wide mb-3 flex items-center gap-2">
          <span class="w-2 h-2 bg-accent"></span>
          External Symbols
          <span class="text-sm font-normal text-n-500">({{ bucketCountLabel('externalSymbols') }})</span>
        </h2>
        <div class="space-y-2">
          <ExternalSymbolCard
            v-for="symbol in results.results.externalSymbols"
            :key="symbol.id"
            :symbol="symbol"
            :snapshot-label="snapshotLabel(symbol.snapshotId, symbol.repo)"
            @trace="openTrace(symbol.caller || symbol.name)"
            @impact="openImpact(symbol.caller || symbol.name)"
          />
        </div>
        <div class="mt-3 flex items-center justify-between text-xs text-n-500">
          <span>{{ bucketRangeLabel('externalSymbols') }}</span>
          <div class="flex items-center gap-2">
            <button
              @click="pageBucket('externalSymbols', -1)"
              :disabled="!canPageBucketPrev('externalSymbols') || loading"
              class="px-2 py-1 border border-gray-300 dark:border-n-600 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800"
            >
              Prev
            </button>
            <button
              @click="pageBucket('externalSymbols', 1)"
              :disabled="!canPageBucketNext('externalSymbols') || loading"
              class="px-2 py-1 border border-gray-300 dark:border-n-600 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800"
            >
              Next
            </button>
          </div>
        </div>
      </section>

      <!-- GraphQL Operations -->
      <section v-if="results.results?.graphqlOperations?.length" class="mb-8">
        <h2 class="text-lg font-bold uppercase tracking-wide mb-3 flex items-center gap-2">
          <span class="w-2 h-2 bg-purple-500"></span>
          GraphQL Operations
          <span class="text-sm font-normal text-n-500">({{ bucketCountLabel('graphqlOperations') }})</span>
        </h2>
        <div class="space-y-2">
          <div
            v-for="op in results.results.graphqlOperations"
            :key="op.id"
            class="border border-n-300 dark:border-n-700 p-3 bg-white dark:bg-n-900"
          >
            <div class="flex items-start justify-between gap-3">
              <div>
                <div class="font-mono text-sm font-semibold">{{ op.name }}</div>
                <div class="text-xs text-n-500 font-mono">{{ op.operationType }} · {{ op.repo }} · {{ op.file }}:{{ op.line }}</div>
              </div>
              <span class="text-[10px] uppercase tracking-widest text-n-500 border border-n-300 dark:border-n-700 px-2 py-1">
                {{ snapshotLabel(op.snapshotId, op.repo) }}
              </span>
            </div>
          </div>
        </div>
        <div class="mt-3 flex items-center justify-between text-xs text-n-500">
          <span>{{ bucketRangeLabel('graphqlOperations') }}</span>
          <div class="flex items-center gap-2">
            <button
              @click="pageBucket('graphqlOperations', -1)"
              :disabled="!canPageBucketPrev('graphqlOperations') || loading"
              class="px-2 py-1 border border-gray-300 dark:border-n-600 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800"
            >
              Prev
            </button>
            <button
              @click="pageBucket('graphqlOperations', 1)"
              :disabled="!canPageBucketNext('graphqlOperations') || loading"
              class="px-2 py-1 border border-gray-300 dark:border-n-600 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800"
            >
              Next
            </button>
          </div>
        </div>
      </section>

      <!-- Azure Triggers -->
      <section v-if="results.results?.azureTriggers?.length" class="mb-8">
        <h2 class="text-lg font-bold uppercase tracking-wide mb-3 flex items-center gap-2">
          <span class="w-2 h-2 bg-sky-500"></span>
          Azure Triggers
          <span class="text-sm font-normal text-n-500">({{ bucketCountLabel('azureTriggers') }})</span>
        </h2>
        <div class="space-y-2">
          <div
            v-for="trigger in results.results.azureTriggers"
            :key="trigger.id"
            class="border border-n-300 dark:border-n-700 p-3 bg-white dark:bg-n-900"
          >
            <div class="flex items-start justify-between gap-3">
              <div>
                <div class="font-mono text-sm font-semibold">{{ trigger.functionName }}</div>
                <div class="text-xs text-n-500 font-mono">
                  {{ trigger.triggerType }} · {{ trigger.repo }} · {{ trigger.file }}:{{ trigger.line }}
                </div>
                <div v-if="trigger.route || trigger.resourceName" class="text-xs text-n-500 font-mono mt-1">
                  {{ trigger.route || trigger.resourceName }}
                </div>
              </div>
              <span class="text-[10px] uppercase tracking-widest text-n-500 border border-n-300 dark:border-n-700 px-2 py-1">
                {{ snapshotLabel(trigger.snapshotId, trigger.repo) }}
              </span>
            </div>
          </div>
        </div>
        <div class="mt-3 flex items-center justify-between text-xs text-n-500">
          <span>{{ bucketRangeLabel('azureTriggers') }}</span>
          <div class="flex items-center gap-2">
            <button
              @click="pageBucket('azureTriggers', -1)"
              :disabled="!canPageBucketPrev('azureTriggers') || loading"
              class="px-2 py-1 border border-gray-300 dark:border-n-600 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800"
            >
              Prev
            </button>
            <button
              @click="pageBucket('azureTriggers', 1)"
              :disabled="!canPageBucketNext('azureTriggers') || loading"
              class="px-2 py-1 border border-gray-300 dark:border-n-600 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800"
            >
              Next
            </button>
          </div>
        </div>
      </section>

      <!-- Endpoints -->
      <section v-if="results.results?.endpoints?.length" class="mb-8">
        <h2 class="text-lg font-bold uppercase tracking-wide mb-3 flex items-center gap-2">
          <span class="w-2 h-2 bg-yellow-500"></span>
          Endpoints
          <span class="text-sm font-normal text-n-500">({{ bucketCountLabel('endpoints') }})</span>
        </h2>
        <div class="space-y-2">
          <EndpointCard
            v-for="ep in results.results.endpoints"
            :key="ep.id"
            :endpoint="ep"
            :snapshot-label="snapshotLabel(ep.snapshotId, ep.repo)"
            @trace="openTrace(ep.handler)"
          />
        </div>
        <div class="mt-3 flex items-center justify-between text-xs text-n-500">
          <span>{{ bucketRangeLabel('endpoints') }}</span>
          <div class="flex items-center gap-2">
            <button
              @click="pageBucket('endpoints', -1)"
              :disabled="!canPageBucketPrev('endpoints') || loading"
              class="px-2 py-1 border border-gray-300 dark:border-n-600 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800"
            >
              Prev
            </button>
            <button
              @click="pageBucket('endpoints', 1)"
              :disabled="!canPageBucketNext('endpoints') || loading"
              class="px-2 py-1 border border-gray-300 dark:border-n-600 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800"
            >
              Next
            </button>
          </div>
        </div>
      </section>

      <!-- Classes -->
      <section v-if="results.results?.classes?.length" class="mb-8">
        <h2 class="text-lg font-bold uppercase tracking-wide mb-3 flex items-center gap-2">
          <span class="w-2 h-2 bg-green-500"></span>
          Classes
          <span class="text-sm font-normal text-n-500">({{ bucketCountLabel('classes') }})</span>
        </h2>
        <div class="space-y-2">
          <ClassCard
            v-for="cls in results.results.classes"
            :key="cls.id"
            :cls="cls"
            :snapshot-label="snapshotLabel(cls.snapshotId, cls.repo)"
            :prefetched-integrations="classIntegrationMap[cls.id]?.integrations"
            :prefetched-handled-endpoints="classIntegrationMap[cls.id]?.handledEndpoints"
          />
        </div>
        <div class="mt-3 flex items-center justify-between text-xs text-n-500">
          <span>{{ bucketRangeLabel('classes') }}</span>
          <div class="flex items-center gap-2">
            <button
              @click="pageBucket('classes', -1)"
              :disabled="!canPageBucketPrev('classes') || loading"
              class="px-2 py-1 border border-gray-300 dark:border-n-600 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800"
            >
              Prev
            </button>
            <button
              @click="pageBucket('classes', 1)"
              :disabled="!canPageBucketNext('classes') || loading"
              class="px-2 py-1 border border-gray-300 dark:border-n-600 disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800"
            >
              Next
            </button>
          </div>
        </div>
      </section>

      <!-- No results -->
      <div
        v-if="results.stats.totalResults === 0"
        class="text-center py-12 text-n-500 uppercase tracking-widest text-xs"
      >
        No results found for "{{ results.query }}"
      </div>
    </div>

    <!-- Initial state with repo graph -->
    <div v-else class="text-center text-n-500">
      <p class="text-lg uppercase tracking-wide">Search across your codebase</p>
      <p class="mt-2 text-sm">Find functions, classes, endpoints, data entities, and external symbols</p>

      <!-- Repo Dependencies Graph -->
      <div class="mt-8">
        <div class="flex items-center justify-center gap-6 mb-4 text-xs uppercase tracking-wide">
          <div class="flex items-center gap-2">
            <div class="flex items-center gap-1">
              <span
                v-for="color in graphLegend.repository"
                :key="color"
                class="w-3 h-3 rounded-full border border-black/20 dark:border-white/15"
                :style="{ backgroundColor: color }"
              ></span>
            </div>
            <span>Repository</span>
          </div>
          <div class="flex items-center gap-2">
            <span class="flex items-center">
              <span class="w-8 h-[2px]" :style="{ backgroundColor: graphLegend.http + graphLegend.edgeAlpha }"></span>
              <span
                class="w-0 h-0 border-y-[4px] border-y-transparent border-l-[6px]"
                :style="{ borderLeftColor: graphLegend.http + graphLegend.edgeAlpha }"
              ></span>
            </span>
            <span>HTTP Calls</span>
          </div>
          <div class="flex items-center gap-2">
            <span class="flex items-center">
              <span class="w-8 h-[2px]" :style="{ backgroundColor: graphLegend.sqs + graphLegend.edgeAlpha }"></span>
              <span
                class="w-0 h-0 border-y-[4px] border-y-transparent border-l-[6px]"
                :style="{ borderLeftColor: graphLegend.sqs + graphLegend.edgeAlpha }"
              ></span>
            </span>
            <span>SQS Queue</span>
          </div>
        </div>
        <div class="relative h-[600px] bg-gray-50 dark:bg-n-900/50 rounded-none border-2 border-black dark:border-n-600">
          <div ref="graphContainer" class="absolute inset-0"></div>
          <div
            v-if="graphLoading"
            class="absolute inset-0 flex items-center justify-center bg-white/80 dark:bg-n-900/80"
          >
            <div class="animate-spin w-6 h-6 border-2 border-purple-500 border-t-transparent"></div>
          </div>
          <div
            v-else-if="graphError"
            class="absolute inset-0 flex items-center justify-center bg-white/80 dark:bg-n-900/80 text-red-400 text-sm"
          >
            {{ graphError }}
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, shallowRef, onMounted, onBeforeUnmount, watch, nextTick } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import Graph from 'graphology'
import forceAtlas2 from 'graphology-layout-forceatlas2'
import Sigma from 'sigma'
import drawHover from 'sigma/rendering/canvas/hover'
import type { Settings } from 'sigma/settings'
import type { NodeDisplayData, PartialButFor } from 'sigma/types'
import { api } from '@/api/client'
import type {
  FunctionIntegration,
  Repo,
  SearchBucketOffsets,
  SearchBucketStats,
  SearchIntegrationsClassPayload,
  SearchResponse,
} from '@/types'
import { filterRepoGraph, getCachedRepoGraphLayout, getCachedRepoGraphResponse, repoNodeColor, setCachedRepoGraphLayout, setCachedRepoGraphResponse } from '@/lib/repoGraphCache'
import type { RepoGraphLayoutEdge, RepoGraphLayoutNode } from '@/lib/repoGraphCache'
import FunctionCard from '@/components/search/FunctionCard.vue'
import ClassCard from '@/components/search/ClassCard.vue'
import EndpointCard from '@/components/search/EndpointCard.vue'
import DataEntityCard from '@/components/search/DataEntityCard.vue'
import ExternalSymbolCard from '@/components/search/ExternalSymbolCard.vue'

const router = useRouter()
const route = useRoute()
const defaultLimit = 25

const query = ref('')
const mode = ref<'keyword' | 'trigram'>('keyword')
const repoFilter = ref('')
const repoOptions = ref<string[]>([])
const limit = ref(defaultLimit)
const loading = ref(false)
const error = ref<string | null>(null)
const results = ref<SearchResponse | null>(null)
const bucketOffsets = ref<SearchBucketOffsets>(defaultBucketOffsets())
const functionIntegrationMap = ref<Record<string, FunctionIntegration[]>>({})
const classIntegrationMap = ref<Record<number, SearchIntegrationsClassPayload>>({})
const integrationLoadError = ref<string | null>(null)
const integrationLoading = ref(false)
let searchController: AbortController | null = null
let searchHydrationController: AbortController | null = null

// Repo graph state
const graphContainer = ref<HTMLDivElement | null>(null)

function snapshotLabel(snapshotId?: number, _repo?: string): string {
  if (!snapshotId) {
    return 'legacy / unscoped'
  }
  const context = results.value?.repoContext?.find((item) => item.snapshotId === snapshotId)
  if (!context) return ''
  const sha = context.sha ? context.sha.slice(0, 8) : ''
  return `${context.branch || 'unknown'}${sha ? ` @ ${sha}` : ''}`
}

function normalizeRepoGraphLayout(layout: { nodes: RepoGraphLayoutNode[]; edges: RepoGraphLayoutEdge[] }) {
  if (!graphContainer.value || layout.nodes.length === 0) {
    return layout
  }

  const width = graphContainer.value.clientWidth || 900
  const height = graphContainer.value.clientHeight || 560
  const minX = Math.min(...layout.nodes.map((node) => node.x))
  const maxX = Math.max(...layout.nodes.map((node) => node.x))
  const minY = Math.min(...layout.nodes.map((node) => node.y))
  const maxY = Math.max(...layout.nodes.map((node) => node.y))
  const spanX = Math.max(1, maxX - minX)
  const spanY = Math.max(1, maxY - minY)
  const paddingX = Math.max(60, width * 0.08)
  const paddingY = Math.max(50, height * 0.1)
  const targetWidth = Math.max(1, width - paddingX * 2)
  const targetHeight = Math.max(1, height - paddingY * 2)
  const scaleX = targetWidth / spanX
  const scaleY = targetHeight / spanY
  const offsetX = (width - spanX * scaleX) / 2 - minX * scaleX
  const offsetY = (height - spanY * scaleY) / 2 - minY * scaleY

  return {
    ...layout,
    nodes: layout.nodes.map((node) => ({
      ...node,
      x: node.x * scaleX + offsetX,
      y: node.y * scaleY + offsetY,
    })),
  }
}
const graphLoading = ref(true)
const graphError = ref<string | null>(null)
const renderer = shallowRef<Sigma | null>(null)
const graph = shallowRef<Graph | null>(null)
let themeObserver: MutationObserver | null = null
const currentGraphTheme = ref<'menu' | 'dark' | 'light'>(graphThemeMode())

type SearchBucketKey = 'functions' | 'classes' | 'endpoints' | 'dataEntities' | 'externalSymbols' | 'schedules' | 'graphqlOperations' | 'azureTriggers'

function defaultBucketOffsets(): SearchBucketOffsets {
  return { functions: 0, classes: 0, endpoints: 0, dataEntities: 0, externalSymbols: 0, schedules: 0, graphqlOperations: 0, azureTriggers: 0 }
}

function normalizeBucketOffsets(offsets?: SearchBucketOffsets): Required<SearchBucketOffsets> {
  return {
    functions: offsets?.functions ?? 0,
    classes: offsets?.classes ?? 0,
    endpoints: offsets?.endpoints ?? 0,
    dataEntities: offsets?.dataEntities ?? 0,
    externalSymbols: offsets?.externalSymbols ?? 0,
    schedules: offsets?.schedules ?? 0,
    graphqlOperations: offsets?.graphqlOperations ?? 0,
    azureTriggers: offsets?.azureTriggers ?? 0,
  }
}

function searchQueryState(currentOffsets: SearchBucketOffsets) {
  const normalized = normalizeBucketOffsets(currentOffsets)
  const nextQuery: Record<string, string> = {
    q: query.value,
    mode: mode.value,
    limit: String(limit.value),
  }
  if (repoFilter.value.trim()) nextQuery.repo = repoFilter.value.trim()
  if (normalized.functions > 0) nextQuery.functionsOffset = String(normalized.functions)
  if (normalized.classes > 0) nextQuery.classesOffset = String(normalized.classes)
  if (normalized.endpoints > 0) nextQuery.endpointsOffset = String(normalized.endpoints)
  if (normalized.dataEntities > 0) nextQuery.dataEntitiesOffset = String(normalized.dataEntities)
  if (normalized.externalSymbols > 0) nextQuery.externalSymbolsOffset = String(normalized.externalSymbols)
  if (normalized.schedules > 0) nextQuery.schedulesOffset = String(normalized.schedules)
  if (normalized.graphqlOperations > 0) nextQuery.graphqlOperationsOffset = String(normalized.graphqlOperations)
  if (normalized.azureTriggers > 0) nextQuery.azureTriggersOffset = String(normalized.azureTriggers)
  return nextQuery
}

function syncBucketOffsetsFromResults(response: SearchResponse) {
  bucketOffsets.value = {
    functions: response.stats.buckets.functions.offset,
    classes: response.stats.buckets.classes.offset,
    endpoints: response.stats.buckets.endpoints.offset,
    dataEntities: response.stats.buckets.dataEntities.offset,
    externalSymbols: response.stats.buckets.externalSymbols.offset,
    schedules: response.stats.buckets.schedules.offset,
    graphqlOperations: response.stats.buckets.graphqlOperations.offset,
    azureTriggers: response.stats.buckets.azureTriggers.offset,
  }
}

async function search(nextOffsets: SearchBucketOffsets = bucketOffsets.value) {
  if (!query.value.trim()) return

  searchController?.abort()
  const controller = new AbortController()
  searchController = controller
  loading.value = true
  error.value = null
  functionIntegrationMap.value = {}
  classIntegrationMap.value = {}
  integrationLoadError.value = null
  integrationLoading.value = false
  searchHydrationController?.abort()
  searchHydrationController = null
  const normalizedOffsets = normalizeBucketOffsets(nextOffsets)
  const requestQuery = searchQueryState(normalizedOffsets)

  try {
    const response = await api.search(
      query.value,
      mode.value,
      limit.value,
      repoFilter.value || undefined,
      undefined,
      controller.signal,
      'richness',
      normalizedOffsets
    )
    if (searchController !== controller || controller.signal.aborted) return
    results.value = response
    syncBucketOffsetsFromResults(response)
    router.replace({ query: requestQuery })
    void hydrateVisibleSearchIntegrations(response)
  } catch (e) {
    if (searchController !== controller || controller.signal.aborted) return
    error.value = e instanceof Error ? e.message : 'Search failed'
  } finally {
    if (searchController === controller) {
      searchController = null
      loading.value = false
    }
  }
}

function performSearch() {
  bucketOffsets.value = defaultBucketOffsets()
  void search(bucketOffsets.value)
}

function bucketStats(key: SearchBucketKey): SearchBucketStats | null {
  return results.value?.stats.buckets[key] ?? null
}

function bucketCountLabel(key: SearchBucketKey): string {
  const stats = bucketStats(key)
  if (!stats) return '0'
  if (stats.exactTotal) return `${stats.returned} of ${stats.total}`
  return String(stats.returned)
}

function bucketRangeLabel(key: SearchBucketKey): string {
  const stats = bucketStats(key)
  if (!stats || stats.returned === 0 || stats.total === 0) return 'No results on this page'
  const start = Math.min(stats.offset + 1, stats.total)
  const end = Math.min(stats.offset + stats.returned, stats.total)
  if (stats.exactTotal) return `Showing ${start}-${end} of ${stats.total}`
  return `Showing ${start}-${end}`
}

function canPageBucketPrev(key: SearchBucketKey): boolean {
  const stats = bucketStats(key)
  return Boolean(stats && stats.offset > 0)
}

function canPageBucketNext(key: SearchBucketKey): boolean {
  const stats = bucketStats(key)
  return Boolean(stats && stats.hasMore)
}

function pageBucket(key: SearchBucketKey, direction: -1 | 1) {
  if (!results.value) return
  const stats = results.value.stats.buckets[key]
  const nextOffset = Math.max(0, stats.offset + direction * stats.limit)
  const nextOffsets = {
    ...normalizeBucketOffsets(bucketOffsets.value),
    [key]: nextOffset,
  }
  void search(nextOffsets)
}

function searchStatsLabel(): string {
  if (!results.value) return ''
  if (results.value.stats.exactTotal) {
    return `Showing ${results.value.stats.returnedResults} of ${results.value.stats.totalResults} results in ${results.value.stats.searchTime}`
  }
  return `Showing ${results.value.stats.returnedResults} results in ${results.value.stats.searchTime}`
}

async function hydrateVisibleSearchIntegrations(response: SearchResponse) {
  const functionCallerIds = (response.results.functions ?? [])
    .map((fn) => fn.callerId?.trim() ?? '')
    .filter((callerId, index, list) => callerId.length > 0 && list.indexOf(callerId) === index)
  const classIds = (response.results.classes ?? [])
    .map((cls) => cls.id)
    .filter((classId, index, list) => classId > 0 && list.indexOf(classId) === index)

  if (functionCallerIds.length === 0 && classIds.length === 0) {
    return
  }

  const controller = new AbortController()
  searchHydrationController?.abort()
  searchHydrationController = controller
  integrationLoading.value = true

  try {
    const hydration = await api.searchIntegrations({ functionCallerIds, classIds }, controller.signal)
    if (searchHydrationController !== controller) {
      return
    }

    const nextFunctionMap = { ...functionIntegrationMap.value }
    let missing = 0
    for (const callerId of functionCallerIds) {
      if (Object.prototype.hasOwnProperty.call(hydration.functions, callerId)) {
        nextFunctionMap[callerId] = hydration.functions[callerId]
      } else {
        missing++
      }
    }
    functionIntegrationMap.value = nextFunctionMap

    const nextClassMap = { ...classIntegrationMap.value }
    for (const classId of classIds) {
      if (Object.prototype.hasOwnProperty.call(hydration.classes, String(classId))) {
        nextClassMap[classId] = hydration.classes[String(classId)]
      } else {
        missing++
      }
    }
    classIntegrationMap.value = nextClassMap
    integrationLoadError.value = missing > 0 || hydration.errors?.length
      ? 'Some integration details could not be loaded.'
      : null
  } catch (err) {
    if (err instanceof DOMException && err.name === 'AbortError') {
      return
    }
    if (searchHydrationController === controller) {
      integrationLoadError.value = 'Integration details could not be loaded.'
    }
  } finally {
    if (searchHydrationController === controller) {
      searchHydrationController = null
      integrationLoading.value = false
    }
  }
}

function openTrace(functionName: string) {
  router.push({ name: 'trace', query: { fn: functionName } })
}

function openImpact(functionName: string) {
  router.push({ name: 'impact', query: { fn: functionName } })
}

function graphThemeMode() {
  const root = document.documentElement
  if (root.classList.contains('theme-menuconfig')) return 'menu'
  if (root.classList.contains('dark')) return 'dark'
  return 'light'
}

function graphLegendPalette(theme: 'menu' | 'dark' | 'light') {
  if (theme === 'menu') {
    return {
      repository: ['#2f54c9', '#1a8a6e', '#b85c2f'],
      http: '#003300',
      sqs: '#6b0000',
      edgeAlpha: '88',
    }
  }
  if (theme === 'dark') {
    return {
      repository: ['#a855f7', '#06b6d4', '#10b981'],
      http: '#22d3ee',
      sqs: '#f43f5e',
      edgeAlpha: '88',
    }
  }
  return {
    repository: ['#a855f7', '#06b6d4', '#10b981'],
    http: '#0f766e',
    sqs: '#db2777',
    edgeAlpha: 'cc',
  }
}

const graphLegend = computed(() => graphLegendPalette(currentGraphTheme.value))

function graphLabelColor(theme: 'menu' | 'dark' | 'light') {
  if (theme === 'menu') return '#111a36'
  if (theme === 'dark') return '#e2e8f0'
  return '#24324a'
}

function graphHoverLabelColor() {
  return '#111827'
}

function graphEdgeAlpha(theme: 'menu' | 'dark' | 'light') {
  return theme === 'light' ? 'cc' : '88'
}

function edgeColor(type: string) {
  const theme = graphThemeMode()
  if (theme === 'menu') {
    switch (type) {
      case 'HTTP_CALLS':
        return '#003300' // forest green (compensates for 53% edge opacity)
      case 'SQS':
        return '#6b0000' // dark crimson
      default:
        return '#6f84c4'
    }
  }
  if (theme === 'light') {
    switch (type) {
      case 'HTTP_CALLS':
        return '#0f766e'
      case 'SQS':
        return '#db2777'
      default:
        return '#64748b'
    }
  }
  switch (type) {
    case 'HTTP_CALLS':
      return '#22d3ee' // cyan-400
    case 'SQS':
      return '#f43f5e' // rose-500
    default:
      return '#64748b'
  }
}

function applyRepoGraphTheme() {
  if (!graph.value || !renderer.value) return

  const theme = graphThemeMode()
  currentGraphTheme.value = theme
  const menuTheme = theme === 'menu'
  const edgeAlpha = graphEdgeAlpha(theme)
  const g = graph.value

  g.forEachNode((node) => {
    const label = g.getNodeAttribute(node, 'label') as string
    g.setNodeAttribute(node, 'color', repoNodeColor(label, menuTheme))
  })

  g.forEachEdge((edge) => {
    const relationType = (g.getEdgeAttribute(edge, 'relationType') as string | undefined) ?? 'CONTAINS'
    g.setEdgeAttribute(edge, 'color', edgeColor(relationType) + edgeAlpha)
  })

  renderer.value.setSetting('labelColor', { color: graphLabelColor(theme) })
  renderer.value.setSetting('hoverRenderer', (context, data, settings) => {
    drawHover(context, data, { ...settings, labelColor: { color: graphHoverLabelColor() } })
  })
  renderer.value.refresh()
}

// Custom label renderer — randomly align labels in 4 directions based on name hash
function drawLabelMultiDir(
  context: CanvasRenderingContext2D,
  data: PartialButFor<NodeDisplayData, 'x' | 'y' | 'size' | 'label' | 'color'>,
  settings: Settings,
) {
  if (!data.label) return
  const size = settings.labelSize
  const font = `${settings.labelWeight} ${size}px ${settings.labelFont}`
  const color = (settings.labelColor as { color: string }).color || '#fff'

  context.fillStyle = color
  context.font = font

  // Hash label to pick direction: 0=right, 1=bottom, 2=left, 3=top
  let h = 0
  for (let i = 0; i < data.label.length; i++) h = (h * 31 + data.label.charCodeAt(i)) | 0
  const dir = Math.abs(h) % 4
  const pad = data.size + 3

  switch (dir) {
    case 0: // right
      context.textAlign = 'left'
      context.fillText(data.label, data.x + pad, data.y + size / 3)
      break
    case 1: // bottom
      context.textAlign = 'center'
      context.fillText(data.label, data.x, data.y + pad + size)
      break
    case 2: // left
      context.textAlign = 'right'
      context.fillText(data.label, data.x - pad, data.y + size / 3)
      break
    case 3: // top
      context.textAlign = 'center'
      context.fillText(data.label, data.x, data.y - pad)
      break
  }
}

async function loadRepoGraph() {
  graphLoading.value = true
  graphError.value = null

  try {
    let layout = getCachedRepoGraphLayout()
    const shouldComputeLayout = !layout
    if (!layout) {
      const response = getCachedRepoGraphResponse() ?? await api.getRepoDependencies()
      setCachedRepoGraphResponse(response)
      layout = filterRepoGraph(response)
    }

    if (layout.nodes.length === 0) {
      graphError.value = 'No repo dependencies found'
      return
    }

    // Wait for DOM to be ready
    await nextTick()

    if (!graphContainer.value) {
      graphError.value = 'Container not ready'
      return
    }

    graph.value = new Graph({ type: 'directed', multi: true })
    const g = graph.value
    const theme = graphThemeMode()
    const menuTheme = theme === 'menu'
    const edgeAlpha = theme === 'light' ? 'cc' : '88'

    layout.nodes.forEach((node: RepoGraphLayoutNode) => {
      g.addNode(node.id, {
        label: node.name,
        size: node.size,
        color: repoNodeColor(node.name, menuTheme),
        x: node.x,
        y: node.y,
      })
    })

    layout.edges.forEach((edge: RepoGraphLayoutEdge, index: number) => {
      if (!g.hasNode(edge.source) || !g.hasNode(edge.target)) return
      const key = `${edge.source}->${edge.target}-${edge.type}-${index}`
      g.addEdgeWithKey(key, edge.source, edge.target, {
        relationType: edge.type,
        color: edgeColor(edge.type) + edgeAlpha,
        size: Math.min(1 + edge.count / 20, 3),
        type: 'arrow',
      })
    })

    if (shouldComputeLayout) {
      forceAtlas2.assign(g, {
        iterations: 400,
        settings: {
          gravity: 0.08,
          scalingRatio: 120,
          strongGravityMode: false,
          barnesHutOptimize: true,
        },
      })

      layout = normalizeRepoGraphLayout({
        ...layout,
        nodes: layout.nodes.map((node: RepoGraphLayoutNode) => ({
          ...node,
          x: g.getNodeAttribute(node.id, 'x') as number,
          y: g.getNodeAttribute(node.id, 'y') as number,
        })),
      })
      setCachedRepoGraphLayout(layout)
    } else {
      layout = normalizeRepoGraphLayout(layout)
    }

    layout.nodes.forEach((node: RepoGraphLayoutNode) => {
      if (!g.hasNode(node.id)) return
      g.setNodeAttribute(node.id, 'x', node.x)
      g.setNodeAttribute(node.id, 'y', node.y)
    })

    renderer.value = new Sigma(g, graphContainer.value, {
      renderLabels: true,
      labelDensity: 0.5,
      labelRenderedSizeThreshold: 9,
      labelSize: 12,
      labelWeight: 'bold',
      labelColor: { color: graphLabelColor(theme) },
      labelRenderer: drawLabelMultiDir,
      hoverRenderer: (context, data, settings) => {
        drawHover(context, data, { ...settings, labelColor: { color: graphHoverLabelColor() } })
      },
    })

    // Click to filter search
    renderer.value.on('clickNode', ({ node }) => {
      const nodeData = g.getNodeAttributes(node)
      repoFilter.value = nodeData.label as string
    })

    // Enable node dragging
    let draggedNode: string | null = null
    let isDragging = false

    renderer.value.on('downNode', (e) => {
      isDragging = true
      draggedNode = e.node
      renderer.value?.getCamera().disable()
    })

    renderer.value.getMouseCaptor().on('mousemovebody', (e) => {
      if (!isDragging || !draggedNode || !renderer.value) return
      const pos = renderer.value.viewportToGraph(e)
      g.setNodeAttribute(draggedNode, 'x', pos.x)
      g.setNodeAttribute(draggedNode, 'y', pos.y)
    })

    renderer.value.getMouseCaptor().on('mouseup', () => {
      if (isDragging) {
        isDragging = false
        draggedNode = null
        renderer.value?.getCamera().enable()
      }
    })
  } catch (e) {
    graphError.value = e instanceof Error ? e.message : 'Failed to load repo graph'
  } finally {
    graphLoading.value = false
  }
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

// Reload graph when results are cleared
watch(results, (newResults) => {
  if (!newResults && !renderer.value && graphContainer.value) {
    loadRepoGraph()
  }
})

onMounted(() => {
  void loadRepoOptions()
  currentGraphTheme.value = graphThemeMode()
  themeObserver = new MutationObserver(() => {
    currentGraphTheme.value = graphThemeMode()
    applyRepoGraphTheme()
  })
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['class'] })
  // Load from URL params
  const q = route.query.q as string
  const m = route.query.mode as string
  const repo = route.query.repo as string
  const routeLimit = route.query.limit as string
  if (q) {
    query.value = q
    if (m && ['keyword', 'trigram'].includes(m)) {
      mode.value = m as typeof mode.value
    }
    if (repo) {
      repoFilter.value = repo
    }
    if (routeLimit) {
      const parsedLimit = Number(routeLimit)
      if (!Number.isNaN(parsedLimit) && parsedLimit > 0) {
        limit.value = parsedLimit
      }
    }
    bucketOffsets.value = {
      functions: Number(route.query.functionsOffset ?? 0) || 0,
      classes: Number(route.query.classesOffset ?? 0) || 0,
      endpoints: Number(route.query.endpointsOffset ?? 0) || 0,
      dataEntities: Number(route.query.dataEntitiesOffset ?? 0) || 0,
      externalSymbols: Number(route.query.externalSymbolsOffset ?? 0) || 0,
      schedules: Number(route.query.schedulesOffset ?? 0) || 0,
      graphqlOperations: Number(route.query.graphqlOperationsOffset ?? 0) || 0,
      azureTriggers: Number(route.query.azureTriggersOffset ?? 0) || 0,
    }
    void search(bucketOffsets.value)
  } else {
    // Load repo graph on initial state
    void loadRepoGraph()
  }
})

onBeforeUnmount(() => {
  searchController?.abort()
  searchController = null
  searchHydrationController?.abort()
  themeObserver?.disconnect()
  themeObserver = null
  renderer.value?.kill()
  renderer.value = null
  graph.value = null
})
</script>
