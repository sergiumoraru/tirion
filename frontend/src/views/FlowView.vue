<template>
  <div class="max-w-7xl mx-auto p-4">
    <div class="mb-6">
      <div class="flex flex-col gap-3">
        <div class="flex gap-3">
          <input
            v-model="startInput"
            @keyup.enter="runFlow"
            type="text"
            placeholder="e.g. /pages/save or PagesController.save (queue:/job:/scheduled:/eventbridge: supported)"
            class="flex-1 px-4 py-3 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none focus:outline-none focus:border-accent text-black dark:text-n-300 placeholder-n-500 font-mono"
          />
          <button
            @click="runFlow"
            :disabled="loading || !startInput.trim()"
            class="px-6 py-3 bg-accent hover:bg-accent/80 disabled:opacity-50 disabled:cursor-not-allowed rounded-none font-medium text-white uppercase tracking-wide transition-colors h-fit"
          >
            {{ loading ? 'Tracing...' : 'Flow' }}
          </button>
        </div>

        <div class="flex flex-wrap gap-4 text-sm">
          <div class="flex items-center gap-2">
            <label class="text-n-400">Start:</label>
            <select
              v-model="startMode"
              class="bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none px-2 py-1 text-black dark:text-n-300"
            >
              <option value="auto">Auto</option>
              <option value="queue">Queue</option>
              <option value="job">Job</option>
              <option value="scheduled">Scheduled</option>
              <option value="eventbridge">EventBridge</option>
            </select>
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
          </div>
          <div class="flex items-center gap-2">
            <label class="text-n-400">Max hops:</label>
            <input
              type="number"
              v-model.number="maxHops"
              min="10"
              max="5000"
              class="w-20 px-2 py-1 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-black dark:text-n-300"
            />
          </div>
          <div class="flex items-center gap-2">
            <label class="text-n-400">Mode:</label>
            <select
              v-model="strictMode"
              class="bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none px-2 py-1 text-black dark:text-n-300"
            >
              <option :value="true">Strict</option>
              <option :value="false">Exploratory</option>
            </select>
          </div>
          <label class="flex items-center gap-2 text-n-300">
            <input
              v-model="includeRelatedEntities"
              type="checkbox"
              class="h-4 w-4 rounded-none border-gray-300 dark:border-n-600 text-accent"
            />
            <span>Include related entities</span>
            <span
              class="inline-flex items-center justify-center w-4 h-4 text-[10px] rounded-full border border-n-500 text-n-400"
              title="Useful for high-recall exploration (indirect data relationships). Can be noisy for clean flow narratives."
              aria-label="Include related entities help"
            >
              i
            </span>
          </label>
        </div>
      </div>
    </div>

    <div v-if="loading" class="text-center py-12">
      <div class="animate-spin w-8 h-8 border-2 border-accent border-t-transparent rounded-full mx-auto"></div>
      <p class="mt-3 text-n-500 uppercase text-xs tracking-widest">Building flow...</p>
    </div>

    <div v-else-if="error" class="border-2 border-red-500 bg-red-50 dark:bg-red-950 rounded-none p-4 text-red-600 dark:text-red-400">
      {{ error }}
    </div>

    <div v-else-if="result">
      <div class="mb-4 text-sm text-n-300">
        <span v-if="result.workspace">Workspace: <span class="font-semibold">{{ result.workspace.id }}</span> · </span>
        <span v-if="result.repoContext?.length">Indexed repos: <span class="font-semibold">{{ result.repoContext.length }}</span> · </span>
        Roots: <span class="font-semibold">{{ result.stats.roots }}</span> ·
        Hops:
        <span class="font-semibold">{{ result.stats.hops }}</span>
        <span v-if="hasBroaderServerFlow" class="text-n-500">
          shown of <span class="font-semibold text-n-300">{{ availableHopLabel }}</span> candidate hops
        </span> ·
        Time: <span class="font-semibold">{{ result.stats.duration }}</span>
      </div>
      <div
        v-if="flowWarnings.length"
        class="mb-4 border-2 border-amber-500 bg-amber-50 dark:bg-amber-950/40 rounded-none p-4 text-amber-900 dark:text-amber-200"
      >
        <div class="text-xs uppercase tracking-widest font-semibold mb-2">Flow Warnings</div>
        <div class="space-y-1 text-sm">
          <div v-for="warning in flowWarnings" :key="warning">
            {{ warning }}
          </div>
        </div>
        <div v-if="suggestedMaxHopOptions.length" class="flex flex-wrap gap-2 mt-3">
          <button
            v-for="nextMax in suggestedMaxHopOptions"
            :key="nextMax"
            @click="rerunWithMaxHops(nextMax)"
            class="px-3 py-1 border border-amber-700 dark:border-amber-300 hover:bg-amber-100 dark:hover:bg-amber-900 uppercase tracking-wide text-xs"
          >
            Retry at {{ nextMax }} hops
          </button>
        </div>
      </div>
      <div
        v-if="flowModeNotes.length"
        class="mb-4 border-2 border-sky-500 bg-sky-50 dark:bg-sky-950/30 rounded-none p-4 text-sky-900 dark:text-sky-200"
      >
        <div class="text-xs uppercase tracking-widest font-semibold mb-2">Flow Mode</div>
        <div class="space-y-1 text-sm">
          <div v-for="note in flowModeNotes" :key="note">
            {{ note }}
          </div>
        </div>
        <div class="flex flex-wrap gap-2 mt-3">
          <button
            v-if="canSwitchToExploratory"
            @click="rerunWithStrictMode(false)"
            class="px-3 py-1 border border-sky-700 dark:border-sky-300 hover:bg-sky-100 dark:hover:bg-sky-900 uppercase tracking-wide text-xs"
          >
            Show exploratory flow
          </button>
          <button
            v-if="canSwitchToStrict"
            @click="rerunWithStrictMode(true)"
            class="px-3 py-1 border border-sky-700 dark:border-sky-300 hover:bg-sky-100 dark:hover:bg-sky-900 uppercase tracking-wide text-xs"
          >
            Show strict narrative
          </button>
        </div>
      </div>
      <div
        v-if="hasRootScopeFilter && rootScopeMode === 'matched'"
        class="mb-4 text-xs text-n-500"
      >
        Focused view: {{ scopedRoots.length }} of {{ allRoots.length }} roots, {{ scopedHops.length }} of {{ result.hops.length }} hops.
      </div>

      <section v-if="result.candidates?.length" class="mb-6 border-y border-gray-300 dark:border-n-600 py-4">
        <h2 class="text-sm font-semibold mb-3">Endpoint Candidates</h2>
        <ul class="divide-y divide-gray-200 dark:divide-n-700">
          <li
            v-for="candidate in result.candidates"
            :key="[candidate.entity, candidate.from.repo, candidate.from.file, candidate.from.handler, candidate.endpoint.repo, candidate.endpoint.file, candidate.endpoint.handler, candidate.endpoint.method, candidate.endpoint.path, candidate.reason].join('|')"
            class="py-3 flex items-start justify-between gap-4"
          >
            <div class="min-w-0 break-words">
              <div class="font-mono text-sm">{{ formatEndpointLabel(candidate.endpoint) }}</div>
              <div class="text-xs text-n-500 mt-1">{{ candidate.endpoint.repo }} · {{ candidate.endpoint.file }}:{{ candidate.endpoint.line }}</div>
              <div class="text-xs mt-1">{{ candidate.entity }} · {{ candidate.reason }}</div>
              <div class="text-xs text-n-500 mt-1">From {{ candidate.from.repo }} · {{ candidate.from.handler }}</div>
            </div>
            <button
              v-if="candidate.endpoint.handler"
              class="shrink-0 text-sm underline"
              @click="openTrace(candidate.endpoint)"
            >Trace</button>
          </li>
        </ul>
      </section>

      <div
        v-if="visibleRootIntegrations.length"
        class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4 mb-6"
      >
        <div class="text-xs text-n-500 uppercase tracking-widest">Connected Integrations</div>
        <div class="text-xs text-n-500 mb-3">Matched or inferred cross-repo HTTP integrations for the current roots.</div>
        <div class="space-y-3">
          <div
            v-for="group in visibleRootIntegrations"
            :key="group.rootKey"
            class="border border-gray-300 dark:border-n-600 rounded-none bg-gray-50 dark:bg-n-900/40 p-3"
          >
            <div class="font-mono text-sm text-black dark:text-n-300">{{ formatEndpointLabel(group.root) }}</div>
            <div class="text-xs text-n-500 mt-1">{{ formatEndpointMeta(group.root) }}</div>
            <div class="mt-3 space-y-2">
              <div
                v-for="integration in group.integrations"
                :key="flowIntegrationKey(integration)"
                class="text-sm"
              >
                <div class="font-mono text-black dark:text-n-300">
                  {{ integration.method }} {{ integration.path }}
                </div>
                <div class="text-xs text-n-500 mt-0.5">
                  {{ integration.targetRepo }}
                  <span v-if="integration.targetHandler"> · {{ integration.targetHandler }}</span>
                  <span v-if="integration.clientType"> · via {{ integration.clientType }}</span>
                  <span v-if="integration.resolution === 'inferred'"> · inferred from source</span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div
        v-if="visibleRootDataAccess.length"
        class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4 mb-6"
      >
        <div class="text-xs text-n-500 uppercase tracking-widest">Indexed Data Access</div>
        <div class="text-xs text-n-500 mb-1">Persistence entities this function touches.</div>
        <div class="text-[11px] text-n-500 mb-3">Entity names are inferred from indexed SQL/JPA/mapper access. This view does not yet show the exact SQL statement.</div>
        <div class="space-y-3">
          <div
            v-for="group in visibleRootDataAccess"
            :key="group.rootKey"
            class="border border-gray-300 dark:border-n-600 rounded-none bg-gray-50 dark:bg-n-900/40 p-3"
          >
            <div class="font-mono text-sm text-black dark:text-n-300">{{ formatEndpointLabel(group.root) }}</div>
            <div class="text-xs text-n-500 mt-1">{{ formatEndpointMeta(group.root) }}</div>
            <div class="mt-2 text-xs text-n-500">
              {{ formatDataAccessSummary(group) }}
            </div>
            <div v-if="group.writes.length" class="mt-3">
              <div class="text-[11px] uppercase tracking-widest text-red-700 dark:text-red-300 mb-2">Writes These Entities</div>
              <div class="flex flex-wrap gap-2">
                <div
                  v-for="access in group.writes"
                  :key="`write:${access.entityName}`"
                  class="px-2 py-1 border border-red-400 dark:border-red-700 rounded-none text-xs font-mono text-black dark:text-n-100 bg-red-50 dark:bg-red-950/40"
                >
                  <span class="text-red-700 dark:text-red-200">{{ access.entityName }}</span>
                  <span v-if="access.lines.length" class="text-n-500"> · lines {{ access.lines.join(', ') }}</span>
                </div>
              </div>
            </div>
            <div v-if="group.reads.length" class="mt-3">
              <div class="text-[11px] uppercase tracking-widest text-emerald-700 dark:text-emerald-300 mb-2">Reads These Entities</div>
              <div class="flex flex-wrap gap-2">
                <div
                  v-for="access in group.reads"
                  :key="`read:${access.entityName}`"
                  class="px-2 py-1 border border-emerald-400 dark:border-emerald-700 rounded-none text-xs font-mono text-black dark:text-n-100 bg-emerald-50 dark:bg-emerald-950/40"
                >
                  <span class="text-emerald-700 dark:text-emerald-200">{{ access.entityName }}</span>
                  <span v-if="access.lines.length" class="text-n-500"> · lines {{ access.lines.join(', ') }}</span>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div v-if="narratives.length" class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4 mb-6">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div class="text-xs text-n-500 uppercase tracking-widest">Flow narrative</div>
          <div class="flex flex-wrap items-center gap-4 text-sm text-n-300">
            <label
              v-if="canFocusNarratives"
              class="flex items-center gap-2"
            >
              <input
                v-model="focusNarrativeCalls"
                type="checkbox"
                class="h-4 w-4 rounded-none border-gray-300 dark:border-n-600 text-accent"
              />
              <span class="text-n-300">Focus call path</span>
            </label>
            <span
              v-if="canFocusNarratives && canSwitchNarrativeRoots"
              class="h-4 w-px bg-n-600"
            />
            <div
              v-if="canSwitchNarrativeRoots"
              class="flex items-center gap-2"
            >
              <label for="narrative-view-mode" class="text-n-400">Narrative view:</label>
              <select
                id="narrative-view-mode"
                v-model="narrativeRootMode"
                class="bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none px-2 py-1 text-black dark:text-n-300"
              >
                <option value="primary">Primary narrative</option>
                <option value="all">All narratives</option>
              </select>
            </div>
          </div>
        </div>
        <div class="mt-2 text-xs text-n-500">
          Internal-call collapsing below is display-only. Hop counts above reflect the full server response after Flow mode reductions.
        </div>
        <div class="mt-3 space-y-4 text-sm">
          <div v-for="display in narrativeDisplays" :key="display.key">
            <div class="font-mono mb-1">
              {{ narrativeRootLabel(display.root) }}
            </div>
            <div class="text-sm text-n-400 mb-2">
              <span class="font-semibold text-n-300">Steps: {{ display.visibleStepCount }} / {{ display.totalStepCount }}</span>
              <span v-if="display.hiddenHelperCount > 0">
                {{ display.helpersExpanded ? ` Includes ${display.hiddenHelperCount} same-class calls.` : ` ${display.hiddenHelperCount} same-class calls are hidden.` }}
              </span>
              <span v-else>
                No same-class calls collapsed.
              </span>
              <span v-if="display.compactedInternalCount > 0">
                {{ ` ${display.compactedInternalCount} internal call steps compacted.` }}
              </span>
              <button
                v-if="display.hiddenHelperCount > 0"
                @click="toggleNarrativeHelpers(display.key)"
                class="text-accent hover:underline ml-2"
              >
                {{ display.helpersExpanded ? 'Collapse same-class calls' : 'Show all steps' }}
              </button>
            </div>
            <div class="space-y-3">
              <div v-for="(phase, phaseIdx) in display.phases" :key="`${display.key}-phase-${phaseIdx}`">
                <div class="text-xs text-n-500 uppercase tracking-widest mb-1">{{ phase.label }}</div>
                <div
                  v-for="(group, groupIdx) in groupStepsByOrigin(phase.steps)"
                  :key="`${display.key}-${phaseIdx}-g${groupIdx}`"
                  class="mb-2"
                >
                  <div
                    v-if="group.fromHandler"
                    class="text-sm font-mono font-semibold text-n-200 mb-0.5"
                  >
                    {{ group.fromHandler }}
                    <span v-if="group.fromRepo" class="text-n-500 text-xs font-normal ml-1">[{{ group.fromRepo }}]</span>
                  </div>
                  <ul class="space-y-0.5 text-n-300 list-none pl-4">
                    <li
                      v-for="(step, stepIdx) in group.items"
                      :key="`${display.key}-${phaseIdx}-g${groupIdx}-${stepIdx}`"
                      class="break-words font-mono text-sm"
                    >
                      {{ narrativeGroupItemLabel(step, group.fromHandler) }}
                    </li>
                  </ul>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div v-if="visibleCallers.length" class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4 mb-6">
        <div class="text-xs text-n-500 uppercase tracking-widest">Callers</div>
        <div class="text-xs text-n-500 mb-3">Where this endpoint is called from.</div>
        <div class="space-y-2">
          <div
            v-for="caller in visibleCallers"
            :key="rootKey(caller)"
            class="text-sm text-n-300 border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-900/40"
          >
            <div class="font-mono break-words">
              {{ formatEndpointLabel(caller) }}
            </div>
            <div class="text-xs text-n-500 mt-1 break-words">
              {{ formatEndpointMeta(caller) }}
            </div>
          </div>
        </div>
      </div>

      <div v-if="flowSummaries.length" class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4 mb-6">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div class="text-xs text-n-500 uppercase tracking-widest">
            {{ narratives.length ? 'Business summary' : 'Flow summary' }}
          </div>
          <div class="flex items-center gap-2 text-xs text-n-400">
            <span>{{ flowSummaries.length }} groups</span>
            <label class="text-n-500">Top</label>
            <select
              v-model="summaryLimit"
              class="bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none px-2 py-0.5 text-black dark:text-n-300"
            >
              <option :value="3">3</option>
              <option :value="5">5</option>
              <option :value="10">10</option>
              <option :value="20">20</option>
              <option value="all">All</option>
            </select>
            <button
              v-if="hasMoreSummaries"
              @click="summaryLimit = 'all'"
              class="text-accent hover:underline"
            >
              Show all
            </button>
            <span v-else-if="flowSummaries.length" class="text-n-500">All shown</span>
          </div>
        </div>
        <div class="mt-3 space-y-2 text-sm">
          <div v-for="summary in visibleFlowSummaries" :key="summary" class="break-words">
            {{ summary }}
          </div>
        </div>
      </div>

      <div class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4 mb-6">
        <div class="flex flex-wrap items-center justify-between gap-3 mb-1">
          <h2 class="text-lg font-bold uppercase tracking-wide">Roots</h2>
          <div
            v-if="hasRootScopeFilter"
            class="flex items-center gap-2 text-xs text-n-300"
          >
            <label class="text-n-400">Root list</label>
            <select
              v-model="rootScopeMode"
              class="bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none px-2 py-1 text-black dark:text-n-300"
            >
              <option value="matched">Matched start</option>
              <option value="all">All discovered roots</option>
            </select>
            <span class="text-n-500">{{ scopedRoots.length }} / {{ allRoots.length }}</span>
          </div>
        </div>
        <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Starting endpoints or functions.</div>
        <div v-if="scopedRoots.length === 0" class="text-n-500 text-sm">No roots found.</div>
        <div v-else class="space-y-2">
          <div
            v-for="root in scopedRoots"
            :key="rootKey(root)"
            class="text-sm text-n-300 border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-900/40"
          >
            <div class="flex items-center justify-between gap-3">
              <div class="font-mono break-words">
                {{ formatEndpointLabel(root) }}
              </div>
              <button
                v-if="root.handler"
                @click="openTrace(root)"
                class="text-xs text-accent hover:underline uppercase tracking-wide"
              >
                Trace
              </button>
            </div>
            <div class="text-xs text-n-500 mt-1 break-words">
              {{ formatEndpointMeta(root) }}
            </div>
          </div>
        </div>
      </div>

      <div class="space-y-6">
        <div v-for="group in groupedHops" :key="group.depth">
          <div class="mb-2 flex flex-wrap items-center justify-between gap-2">
            <div class="text-sm text-n-400 uppercase tracking-wide">Hop {{ group.depth + 1 }}</div>
            <div
              v-if="isDepthCollapsed(group)"
              class="text-xs text-n-500 flex items-center gap-2"
            >
              <span>
                Showing {{ visibleHopsForDepth(group).length }} of {{ group.hops.length }} groups
                (related-entity groups collapsed)
              </span>
              <button
                @click="expandDepth(group.depth)"
                class="text-accent hover:underline"
              >
                Show all
              </button>
            </div>
          </div>
          <div class="space-y-3">
            <div
              v-for="(hop, index) in visibleHopsForDepth(group)"
              :key="hopKey(hop, index)"
              class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4"
            >
              <div class="grid grid-cols-1 lg:grid-cols-[1fr_auto_1fr] gap-4 items-start min-w-0">
                <div class="min-w-0">
                  <div class="text-xs text-n-500 mb-1 uppercase tracking-wide">
                    From
                    <span v-if="hop.from.length > 1" class="ml-1 text-n-500">({{ hop.from.length }})</span>
                  </div>
                  <div class="space-y-2 max-h-48 overflow-auto pr-2">
                    <div v-for="source in hop.from" :key="rootKey(source)" class="min-w-0">
                      <div class="font-mono break-words">
                        {{ formatEndpointLabel(source) }}
                      </div>
                      <div class="text-xs text-n-500 mt-1 break-words">
                        {{ formatEndpointMeta(source) }}
                      </div>
                      <button
                        v-if="source.handler"
                        @click="openTrace(source)"
                        class="text-xs text-accent hover:underline mt-2 uppercase tracking-wide"
                      >
                        Trace
                      </button>
                    </div>
                  </div>
                </div>

                <div class="flex flex-col items-center gap-1 text-center min-w-0">
                  <span
                    :class="viaBadgeClass(hop.via)"
                    class="px-2 py-0.5 rounded-none text-xs font-semibold"
                  >
                    {{ formatViaLabel(hop.via) }}
                  </span>
                  <div class="text-xs text-n-500 break-words max-w-[14rem]">
                    {{ formatViaDetail(hop.via) }}
                  </div>
                </div>

                <div class="min-w-0">
                  <div class="text-xs text-n-500 mb-1 uppercase tracking-wide">
                    To
                    <span v-if="hop.to.length > 1" class="ml-1 text-n-500">({{ hop.to.length }})</span>
                  </div>
                  <div class="max-h-48 overflow-auto pr-2">
                    <div v-for="target in hop.to" :key="rootKey(target)" class="min-w-0 mb-2 last:mb-0">
                      <div class="font-mono break-words">
                        {{ formatEndpointLabel(target) }}
                      </div>
                      <div class="text-xs text-n-500 mt-1 break-words">
                        {{ formatEndpointMeta(target) }}
                      </div>
                      <button
                        v-if="target.handler"
                        @click="openTrace(target)"
                        class="text-xs text-accent hover:underline mt-2 uppercase tracking-wide"
                      >
                        Trace
                      </button>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
        <div v-if="groupedHops.length === 0" class="text-n-500 text-sm">
          No cross-service hops found. Try increasing depth or using an endpoint path.
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onUnmounted, ref, shallowRef, watch } from 'vue'
import { useRouter } from 'vue-router'
import { api } from '@/api/client'
import { useWorkspace } from '@/composables/useWorkspace'
import type {
  FlowResponse,
  FlowEndpoint,
  FlowHop,
  FlowVia,
  FlowNarrative,
  FlowNarrativeStep,
  FunctionDataAccess,
  FunctionIntegration,
} from '@/types'

const router = useRouter()
const { workspaceId } = useWorkspace()
let flowRequest: AbortController | null = null

const startInput = ref('')
const startMode = ref<'auto' | 'queue' | 'job' | 'scheduled' | 'eventbridge'>('auto')
const depth = ref(5)
const maxHops = ref(500)
const strictMode = ref(true)
const loading = ref(false)
const error = ref<string | null>(null)
const result = shallowRef<FlowResponse | null>(null)
const summaryLimit = ref<number | 'all'>(5)
const includeRelatedEntities = ref(false)
const expandedRelatedDepths = ref<Record<number, boolean>>({})
const lastRequestedStart = ref('')
const rootScopeMode = ref<'matched' | 'all'>('matched')
const narrativeRootMode = ref<'primary' | 'all'>('primary')
const expandedNarrativeHelpers = ref<Record<string, boolean>>({})
const focusNarrativeCalls = ref(true)
const rootDataAccess = ref<Record<string, FunctionDataAccess[]>>({})
const rootIntegrations = ref<Record<string, FunctionIntegration[]>>({})
const relatedHopPreviewLimit = 8

type NarrativePhase = {
  label: string
  steps: FlowNarrativeStep[]
}

type NarrativeDisplay = {
  key: string
  root: FlowEndpoint
  phases: NarrativePhase[]
  hiddenHelperCount: number
  compactedInternalCount: number
  totalStepCount: number
  visibleStepCount: number
  helpersExpanded: boolean
}

const narratives = computed(() => {
  const list = result.value?.narratives ?? []
  if (!list.length) return []
  const scopeKeys = new Set(scopedRoots.value.map((root) => rootKey(root)))
  const scopedList =
    hasRootScopeFilter.value && rootScopeMode.value === 'matched'
      ? list.filter((narrative) => scopeKeys.has(rootKey(narrative.root)))
      : list
  const sorted = [...scopedList]
  sorted.sort((a, b) => {
    const aMethod = a.root.method || ''
    const bMethod = b.root.method || ''
    if (aMethod === bMethod) return 0
    if (!aMethod) return 1
    if (!bMethod) return -1
    return aMethod.localeCompare(bMethod)
  })
  return sorted
})

const canSwitchNarrativeRoots = computed(() => {
  const narrativeCount = narratives.value.length
  const rootCount = scopedRoots.value.length
  return narrativeCount > 1 || rootCount > 1
})

const flowWarnings = computed(() => result.value?.warnings ?? [])

const availableHopLabel = computed(() => {
  const completeness = result.value?.completeness
  if (!completeness) return ''
  const count = String(completeness.availableHops)
  return completeness.exactAvailableHops ? count : `>=${count}`
})

const hasBroaderServerFlow = computed(() => {
  const current = result.value
  const completeness = current?.completeness
  if (!current || !completeness) return false
  return completeness.availableHops > current.stats.hops
})

const flowModeNotes = computed(() => {
  const current = result.value
  if (!current) return [] as string[]
  const assumptions = current.assumptions
  if (!assumptions) return [] as string[]
  const notes: string[] = []
  if (assumptions.strictModeReducedHops && assumptions.strictModeReducedHops > 0) {
    notes.push(
      `Strict mode reduced the visible graph by ${assumptions.strictModeReducedHops} hops to keep one narrative path per root.`
    )
  }
  if (assumptions.internalHopsPruned && assumptions.internalHopsPruned > 0) {
    notes.push(
      `Exploratory flow pruned ${assumptions.internalHopsPruned} internal call hops after discovery to keep the cross-service view readable.`
    )
  }
  if (!assumptions.includeInternalCalls) {
    notes.push('Exploratory flow omits most internal call hops unless the start point is a queue, job, or scheduled trigger.')
  }
  if (assumptions.includeRelatedEntities) {
    notes.push('Related-entity expansion is enabled. Indirect data relationships may add extra hops.')
  }
  return notes
})

const suggestedMaxHopOptions = computed(() => {
  const completeness = result.value?.completeness
  if (!completeness?.truncated) return [] as number[]
  const candidates = [maxHops.value * 2, 1000, 2000, 5000]
  return Array.from(new Set(candidates))
    .filter((value) => Number.isFinite(value) && value > maxHops.value && value <= 5000)
    .sort((a, b) => a - b)
    .slice(0, 3)
})

const canSwitchToExploratory = computed(() => {
  const assumptions = result.value?.assumptions
  if (!assumptions) return false
  return assumptions.strictMode === true
})

const canSwitchToStrict = computed(() => {
  const assumptions = result.value?.assumptions
  if (!assumptions) return false
  return assumptions.strictMode === false
})

const visibleNarratives = computed(() => {
  if (!narratives.value.length) return [] as FlowNarrative[]
  if (narrativeRootMode.value === 'all' || narratives.value.length === 1) return narratives.value
  return [selectPrimaryNarrative(narratives.value)]
})

const canFocusNarratives = computed(() => {
  return visibleNarratives.value.some((narrative) =>
    narrative.steps.some((step) => isCompactableInternalCall(step))
  )
})

const narrativeDisplays = computed((): NarrativeDisplay[] => {
  return visibleNarratives.value.map((narrative) => {
    const key = rootKey(narrative.root)
    const helpersExpanded = expandedNarrativeHelpers.value[key] === true
    const hiddenHelperCount = narrative.steps.filter((step) => isLowSignalHelperCall(step)).length
    let visibleSteps = helpersExpanded
      ? narrative.steps
      : narrative.steps.filter((step) => !isLowSignalHelperCall(step))
    let compactedInternalCount = 0
    if (focusNarrativeCalls.value) {
      const compacted = compactInternalCallRuns(visibleSteps)
      visibleSteps = compacted.steps
      compactedInternalCount = compacted.compactedCount
    }
    return {
      key,
      root: narrative.root,
      phases: groupNarrativeByPhase(visibleSteps, narrative.root),
      hiddenHelperCount,
      compactedInternalCount,
      totalStepCount: narrative.steps.length,
      visibleStepCount: visibleSteps.length,
      helpersExpanded,
    }
  })
})

const callers = computed(() => {
  const list = result.value?.callers ?? []
  if (!list.length) return []
  const byKey = new Map<string, FlowEndpoint>()
  const normalizeSnapshotFile = (path?: string) => {
    if (!path) return ''
    const prefix = '.codebase-snapshots/'
    if (!path.includes(prefix)) return path
    const idx = path.indexOf(prefix)
    const rest = path.slice(idx + prefix.length)
    const slash = rest.indexOf('/')
    if (slash === -1) return path
    return rest.slice(slash + 1)
  }
  for (const caller of list) {
    const key = `${caller.repo}:${caller.method}:${caller.path}:${caller.handler}:${normalizeSnapshotFile(caller.file)}`
    const existing = byKey.get(key)
    if (!existing) {
      byKey.set(key, caller)
      continue
    }
    const existingIsSnapshot = existing.file?.includes('.codebase-snapshots')
    const nextIsSnapshot = caller.file?.includes('.codebase-snapshots')
    if (existingIsSnapshot && !nextIsSnapshot) {
      byKey.set(key, caller)
    }
  }
  return Array.from(byKey.values())
})

const visibleCallers = computed(() => {
  if (!callers.value.length) return [] as FlowEndpoint[]
  if (!hasRootScopeFilter.value || !requestedEndpointStart.value.ok || rootScopeMode.value === 'all') {
    return callers.value
  }
  const filtered = callers.value.filter((caller) =>
    endpointMatchesRequestedStart(caller, requestedEndpointStart.value.method, requestedEndpointStart.value.path)
  )
  return filtered.length ? filtered : callers.value
})

const allRoots = computed(() => result.value?.roots ?? [])

const requestedEndpointStart = computed(() => parseFlowStartInput(lastRequestedStart.value))

const matchedRoots = computed(() => {
  if (!requestedEndpointStart.value.ok) return allRoots.value
  return allRoots.value.filter((root) =>
    endpointMatchesRequestedStart(root, requestedEndpointStart.value.method, requestedEndpointStart.value.path)
  )
})

const hasRootScopeFilter = computed(() => {
  if (!requestedEndpointStart.value.ok) return false
  return matchedRoots.value.length > 0 && matchedRoots.value.length < allRoots.value.length
})

const scopedRoots = computed(() => {
  if (!hasRootScopeFilter.value || rootScopeMode.value === 'all') {
    return allRoots.value
  }
  return matchedRoots.value
})

const visibleRootDataAccess = computed(() => {
  return scopedRoots.value
    .map((root) => ({
      root,
      rootKey: rootKey(root),
      ...summarizeDataAccess(rootDataAccess.value[rootKey(root)] ?? []),
    }))
    .filter((group) => group.reads.length > 0 || group.writes.length > 0)
})

const visibleRootIntegrations = computed(() => {
  return scopedRoots.value
    .map((root) => ({
      root,
      rootKey: rootKey(root),
      integrations: rootIntegrations.value[rootKey(root)] ?? [],
    }))
    .filter((group) => group.integrations.length > 0)
})

type AccessSummary = {
  entityName: string
  access: 'read' | 'write'
  lines: number[]
}

function summarizeDataAccess(accesses: FunctionDataAccess[]): { reads: AccessSummary[]; writes: AccessSummary[] } {
  const grouped = new Map<string, AccessSummary>()
  for (const access of accesses) {
    const normalizedAccess: 'read' | 'write' = access.access === 'write' ? 'write' : 'read'
    const key = `${normalizedAccess}:${access.entityName}`
    const existing = grouped.get(key)
    if (existing) {
      if (access.lineNumber > 0 && !existing.lines.includes(access.lineNumber)) {
        existing.lines.push(access.lineNumber)
        existing.lines.sort((a, b) => a - b)
      }
      continue
    }
    grouped.set(key, {
      entityName: access.entityName,
      access: normalizedAccess,
      lines: access.lineNumber > 0 ? [access.lineNumber] : [],
    })
  }

  const values = Array.from(grouped.values()).sort((a, b) => a.entityName.localeCompare(b.entityName))
  return {
    reads: values.filter((value) => value.access === 'read'),
    writes: values.filter((value) => value.access === 'write'),
  }
}

function formatDataAccessSummary(group: { reads: AccessSummary[]; writes: AccessSummary[] }): string {
  const parts: string[] = []
  if (group.writes.length) {
    parts.push(`writes ${group.writes.length} ${group.writes.length === 1 ? 'entity' : 'entities'}`)
  }
  if (group.reads.length) {
    parts.push(`reads ${group.reads.length} ${group.reads.length === 1 ? 'entity' : 'entities'}`)
  }
  return parts.length ? `This function ${parts.join(' and ')}.` : 'No indexed data access found for this root.'
}

function flowIntegrationKey(integration: FunctionIntegration): string {
  return [
    integration.method,
    integration.path,
    integration.targetRepo,
    integration.targetHandler ?? '',
    integration.lineNumber,
    integration.resolution,
  ].join('|')
}
const narrativeRootLabel = (root: FlowEndpoint) => {
  if (root.path && root.method) {
    return `${root.method} ${root.path}`
  }
  if (root.path) {
    const roots = scopedRoots.value
    const methods = roots
      .filter((r) => r.path === root.path && r.method)
      .map((r) => r.method as string)
    const uniq = Array.from(new Set(methods))
    if (uniq.length) {
      return `${uniq.join('/')} ${root.path}`
    }
  }
  return formatEndpointLabel(root)
}

function toggleNarrativeHelpers(key: string): void {
  expandedNarrativeHelpers.value = {
    ...expandedNarrativeHelpers.value,
    [key]: !expandedNarrativeHelpers.value[key],
  }
}

function callerIdForEndpoint(endpoint: FlowEndpoint): string {
  if (!endpoint.repo || !endpoint.file || !endpoint.handler) return ''
  return `${endpoint.repo}:${endpoint.file}:${endpoint.handler}`
}

async function loadRootDataAccess(roots: FlowEndpoint[], controller: AbortController): Promise<void> {
  const uniqueRoots = roots.filter((root, index, items) => index === items.findIndex((candidate) => rootKey(candidate) === rootKey(root)))
  const loaded = await Promise.all(
    uniqueRoots.map(async (root) => {
      const callerId = callerIdForEndpoint(root)
      if (!callerId) {
        return { key: rootKey(root), accesses: [] as FunctionDataAccess[] }
      }
      try {
        const response = await api.functionDataAccess(callerId, controller.signal)
        return { key: rootKey(root), accesses: response.accesses }
      } catch {
        return { key: rootKey(root), accesses: [] as FunctionDataAccess[] }
      }
    })
  )
  if (controller.signal.aborted || flowRequest !== controller) return
  rootDataAccess.value = Object.fromEntries(loaded.map((entry) => [entry.key, entry.accesses]))
}

async function loadRootIntegrations(roots: FlowEndpoint[], controller: AbortController): Promise<void> {
  const uniqueRoots = roots.filter((root, index, items) => index === items.findIndex((candidate) => rootKey(candidate) === rootKey(root)))
  const loaded = await Promise.all(
    uniqueRoots.map(async (root) => {
      const callerId = callerIdForEndpoint(root)
      if (!callerId) {
        return { key: rootKey(root), integrations: [] as FunctionIntegration[] }
      }
      try {
        const response = await api.functionIntegrations(callerId, controller.signal)
        return { key: rootKey(root), integrations: response.integrations }
      } catch {
        return { key: rootKey(root), integrations: [] as FunctionIntegration[] }
      }
    })
  )
  if (controller.signal.aborted || flowRequest !== controller) return
  rootIntegrations.value = Object.fromEntries(loaded.map((entry) => [entry.key, entry.integrations]))
}

async function runFlow() {
  const start = startInput.value.trim()
  if (!start) return
  flowRequest?.abort()
  const controller = new AbortController()
  flowRequest = controller
  const normalizedStart = startMode.value === 'auto' || start.includes(':')
    ? start
    : `${startMode.value}:${start}`
  const parsedStart = parseFlowStartInput(normalizedStart)
  loading.value = true
  error.value = null
  result.value = null
  rootDataAccess.value = {}
  rootIntegrations.value = {}
  lastRequestedStart.value = normalizedStart
  rootScopeMode.value = parsedStart.ok ? 'matched' : 'all'
  expandedRelatedDepths.value = {}
  narrativeRootMode.value = 'primary'
  expandedNarrativeHelpers.value = {}

  try {
    const response = await api.flow({
      start: normalizedStart,
      depth: depth.value,
      maxHops: maxHops.value,
      includeRelatedEntities: includeRelatedEntities.value,
      strictMode: strictMode.value,
    }, controller.signal)
    if (controller.signal.aborted || flowRequest !== controller) return
    result.value = response
    await Promise.all([
      loadRootDataAccess(response.roots ?? [], controller),
      loadRootIntegrations(response.roots ?? [], controller),
    ])
  } catch (e) {
    if (controller.signal.aborted || flowRequest !== controller) return
    error.value = e instanceof Error ? e.message : 'Failed to build flow'
  } finally {
    if (flowRequest === controller) {
      flowRequest = null
      loading.value = false
    }
  }
}

async function rerunWithMaxHops(nextMax: number): Promise<void> {
  if (loading.value || nextMax === maxHops.value) return
  maxHops.value = nextMax
  await runFlow()
}

async function rerunWithStrictMode(nextStrictMode: boolean): Promise<void> {
  if (loading.value || nextStrictMode === strictMode.value) return
  strictMode.value = nextStrictMode
  await runFlow()
}

function openTrace(endpoint: FlowEndpoint) {
  const match = callerIdForEndpoint(endpoint)
  router.push({ name: 'trace', query: { fn: endpoint.handler, ...(match ? { match } : {}) } })
}

watch(workspaceId, () => {
  flowRequest?.abort()
  flowRequest = null
  loading.value = false
  error.value = null
  result.value = null
  rootDataAccess.value = {}
  rootIntegrations.value = {}
  lastRequestedStart.value = ''
  expandedRelatedDepths.value = {}
  expandedNarrativeHelpers.value = {}
}, { flush: 'sync' })

onUnmounted(() => {
  flowRequest?.abort()
  flowRequest = null
})

function formatEndpointLabel(endpoint: FlowEndpoint): string {
  if (endpoint.method === 'REQUEST' && endpoint.path) {
    return endpoint.path
  }
  if (endpoint.method && endpoint.path) {
    return `${endpoint.method} ${endpoint.path}`
  }
  if (endpoint.handler) return endpoint.handler
  return endpoint.path || endpoint.file || 'unknown'
}

function formatEndpointMeta(endpoint: FlowEndpoint): string {
  const parts: string[] = []
  if (endpoint.repo) parts.push(endpoint.repo)
  if (endpoint.handler && endpoint.handler !== formatEndpointLabel(endpoint)) parts.push(endpoint.handler)
  if (endpoint.file) {
    const suffix = endpoint.line ? `${endpoint.file}:${endpoint.line}` : endpoint.file
    parts.push(suffix)
  }
  return parts.join(' · ')
}

type ParsedFlowStart = {
  ok: boolean
  method: string
  path: string
}

function normalizeFlowPath(path: string): string {
  let normalized = path.trim()
  if (!normalized) return ''
  const queryIdx = normalized.indexOf('?')
  if (queryIdx >= 0) normalized = normalized.slice(0, queryIdx)
  if (!normalized.startsWith('/')) normalized = `/${normalized}`
  while (normalized.includes('//')) normalized = normalized.replace('//', '/')
  if (normalized.length > 1) normalized = normalized.replace(/\/+$/, '')
  return normalized.toLowerCase()
}

function normalizeFlowMethod(method: string): string {
  return method.trim().toUpperCase()
}

function parseFlowStartInput(start: string): ParsedFlowStart {
  const trimmed = start.trim()
  if (!trimmed) return { ok: false, method: '', path: '' }
  const lower = trimmed.toLowerCase()
  if (lower.startsWith('queue:') || lower.startsWith('job:') || lower.startsWith('scheduled:') || lower.startsWith('eventbridge:')) {
    return { ok: false, method: '', path: '' }
  }
  const tokens = trimmed.split(/\s+/)
  if (tokens.length >= 2) {
    const method = normalizeFlowMethod(tokens[0])
    if (['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'REQUEST'].includes(method)) {
      return { ok: true, method, path: normalizeFlowPath(tokens[1]) }
    }
  }
  if (trimmed.startsWith('/')) {
    return { ok: true, method: '', path: normalizeFlowPath(trimmed) }
  }
  return { ok: false, method: '', path: '' }
}

function endpointPathMatches(requestPath: string, endpointPath: string): boolean {
  const requested = normalizeFlowPath(requestPath)
  const endpoint = normalizeFlowPath(endpointPath)
  if (!requested || !endpoint) return false
  if (requested === endpoint) return true

  const templateToPattern = (value: string) =>
    `^${value
      .replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
      .replace(/\\\{[^/]+\\\}/g, '[^/]+')
      .replace(/:[^/]+/g, '[^/]+')}$`

  const endpointPattern = templateToPattern(endpoint)
  if (new RegExp(endpointPattern).test(requested)) return true

  const requestPattern = templateToPattern(requested)
  return new RegExp(requestPattern).test(endpoint)
}

function endpointMatchesRequestedStart(endpoint: FlowEndpoint, method: string, path: string): boolean {
  if (!endpoint.path) return false
  if (!endpointPathMatches(path, endpoint.path)) return false
  const requestedMethod = normalizeFlowMethod(method)
  if (!requestedMethod) return true
  const endpointMethod = normalizeFlowMethod(endpoint.method || '')
  if (!endpointMethod) return false
  if (endpointMethod === 'REQUEST') return true
  return endpointMethod === requestedMethod
}

function formatViaLabel(via: FlowVia): string {
  if (via.type === 'sqs') return 'SQS'
  if (via.type === 'data') return 'DATA'
  if (via.type === 'call') return 'CALL'
  return 'HTTP'
}

function formatViaDetail(via: FlowVia): string {
  if (via.type === 'sqs') return via.queue || 'queue'
  if (via.type === 'data') {
    const entity = via.entity ? via.entity : 'entity'
    return entity.charAt(0).toUpperCase() + entity.slice(1)
  }
  if (via.type === 'call') return 'internal call'
  const method = via.method || 'REQUEST'
  const path = via.path || ''
  const extras: string[] = []
  if (via.clientType) extras.push(via.clientType)
  if (via.external) extras.push('external')
  const extraLabel = extras.length ? ` · ${extras.join(' · ')}` : ''
  return `${method} ${path}${extraLabel}`.trim()
}

function viaBadgeClass(via: FlowVia): string {
  if (via.type === 'sqs') return 'bg-yellow-900 text-yellow-300'
  if (via.type === 'data') return 'bg-purple-900 text-purple-300'
  if (via.type === 'call') return 'bg-slate-800 text-slate-200'
  if (via.external) return 'bg-rose-900 text-rose-300'
  return 'bg-blue-900 text-blue-300'
}

function rootKey(root: FlowEndpoint): string {
  return `${root.repo}:${root.file}:${root.handler}:${root.method}:${root.path}:${root.line}`
}

function endpointNodeKey(endpoint: FlowEndpoint): string {
  return [
    (endpoint.repo || '').toLowerCase(),
    (endpoint.file || '').toLowerCase(),
    (endpoint.handler || '').toLowerCase(),
    (endpoint.method || '').toUpperCase(),
    normalizeFlowPath(endpoint.path || ''),
  ].join('|')
}

function viaKey(via: FlowVia): string {
  return [
    via.type,
    via.method,
    via.path,
    via.queue,
    via.clientType,
    via.entity,
    via.access,
    via.external ? '1' : '0',
  ].join('|')
}

function hopKey(hop: GroupedHop, index: number): string {
  return `${hop.depth}:${hop.key}:${viaKey(hop.via)}:${index}`
}

function flowSummaryForHop(hop: FlowHop, root?: FlowEndpoint): string {
  if (hop.via.type === 'data') {
    const entityLabel = hop.via.entity || 'data'
    const rootRepo = hop.from.repo || 'this service'
    const toRepo = hop.to.repo || 'another service'
    const toLabel = formatEndpointLabel(hop.to)
    return `${rootRepo} shares indexed data access to ${entityLabel} with ${toRepo} at ${toLabel}`
  }

  const rootLabel = root ? formatEndpointLabel(root) : formatEndpointLabel(hop.from)
  const rootRepo = root?.repo || hop.from.repo
  const viaLabel = formatViaLabel(hop.via)
  const viaDetail = formatViaDetail(hop.via)
  const toLabel = formatEndpointLabel(hop.to)
  const toRepo = hop.to.repo
  return `${rootLabel}${rootRepo ? ` (${rootRepo})` : ''} \u2192 ${viaLabel}${
    viaDetail ? ` ${viaDetail}` : ''
  } \u2192 ${toLabel}${toRepo ? ` (${toRepo})` : ''}`
}

function flowSummaryRank(hop: FlowHop): number {
  let score = 0
  if (hop.via.type === 'http') score += 100
  else if (hop.via.type === 'sqs') score += 95
  else if (hop.via.type === 'data') score += 85
  else if (hop.via.type === 'call') score += 20

  if (hop.via.external) score += 20
  if (hop.from.repo && hop.to.repo && hop.from.repo !== hop.to.repo) score += 25
  if (hop.from.path || hop.from.method) score += 8
  if (hop.to.path || hop.to.method) score += 12

  // Internal helper calls are low-value for business summary.
  if (
    hop.via.type === 'call' &&
    hop.from.repo === hop.to.repo &&
    !hop.from.path &&
    !hop.from.method &&
    !hop.to.path &&
    !hop.to.method
  ) {
    score -= 45
  }

  // Prefer earlier hops when two summaries have similar signal.
  score -= hop.depth
  return score
}

function formatHandlerLabel(endpoint: FlowEndpoint): string {
  if (endpoint.handler) return endpoint.handler
  return formatEndpointLabel(endpoint)
}

function splitHandlerName(handler: string): { receiver: string; method: string } {
  const raw = handler.trim()
  if (!raw) return { receiver: '', method: '' }
  const idx = raw.lastIndexOf('.')
  if (idx === -1) return { receiver: '', method: raw }
  return { receiver: raw.slice(0, idx), method: raw.slice(idx + 1) }
}

function canonicalReceiver(handler: string): string {
  const { receiver } = splitHandlerName(handler)
  return receiver.trim()
}

function narrativeStepSignalScore(step: FlowNarrativeStep): number {
  if (step.kind === 'caller' || step.kind === 'data-store') return 10
  if (step.kind === 'branch') return 8
  const via = step.via
  if (!via) return 4
  if (via.type !== 'call') return 9

  let score = 1
  if (step.to.path || step.to.method) score += 5
  if ((step.evidence?.length || 0) > 1) score += 1
  if (step.from.repo && step.to.repo && step.from.repo !== step.to.repo) score += 3
  return score
}

function isLowSignalHelperCall(step: FlowNarrativeStep): boolean {
  if (step.kind !== 'hop') return false
  if (!step.via || step.via.type !== 'call') return false
  if (step.to.path || step.to.method) return false
  const fromReceiver = canonicalReceiver(step.from.handler || '')
  const toReceiver = canonicalReceiver(step.to.handler || '')
  return Boolean(
    fromReceiver && fromReceiver === toReceiver &&
    step.from.repo && step.from.repo === step.to.repo &&
    step.from.file && step.from.file === step.to.file
  )
}

type NarrativeCompaction = {
  steps: FlowNarrativeStep[]
  compactedCount: number
}

function isCompactableInternalCall(step: FlowNarrativeStep): boolean {
  if (step.kind !== 'hop' && step.kind !== 'internal') return false
  if (!step.via || step.via.type !== 'call') return false
  return !step.to.path && !step.to.method
}

function compactInternalCallRuns(steps: FlowNarrativeStep[]): NarrativeCompaction {
  if (steps.length <= 2) return { steps, compactedCount: 0 }
  const compacted: FlowNarrativeStep[] = []
  let compactedCount = 0
  let idx = 0
  for (; idx < steps.length; ) {
    const current = steps[idx]
    if (!isCompactableInternalCall(current)) {
      compacted.push(current)
      idx += 1
      continue
    }
    let end = idx + 1
    for (; end < steps.length; end += 1) {
      if (!isCompactableInternalCall(steps[end])) break
    }
    const run = steps.slice(idx, end)
    if (run.length <= 2) {
      compacted.push(...run)
    } else {
      compacted.push(run[0], run[run.length - 1])
      compactedCount += run.length - 2
    }
    idx = end
  }
  return { steps: compacted, compactedCount }
}

function selectPrimaryNarrative(items: FlowNarrative[]): FlowNarrative {
  return items.reduce((best, current) => {
    const bestScore = narrativePriority(best)
    const currentScore = narrativePriority(current)
    if (currentScore > bestScore) return current
    if (currentScore < bestScore) return best
    const bestMethod = best.root.method || ''
    const currentMethod = current.root.method || ''
    if (!bestMethod) return current
    if (!currentMethod) return best
    return currentMethod.localeCompare(bestMethod) < 0 ? current : best
  })
}

function narrativePriority(narrative: FlowNarrative): number {
  let score = 0
  for (const step of narrative.steps) {
    score += narrativeStepSignalScore(step)
    if (step.via && step.via.type !== 'call') score += 3
    if (step.to.path) score += 2
  }
  return score
}

function inferNarrativePhase(step: FlowNarrativeStep, root: FlowEndpoint, active: string): string {
  if (step.kind === 'caller') return 'Incoming'
  if (step.kind === 'data-store') return 'Persistence'
  if (step.kind === 'branch') return active || 'Decision'
  const via = step.via
  if (!via) return active || 'Internal Processing'
  if (via.type === 'http') return 'HTTP Handoff'
  if (via.type === 'sqs') return 'Queue Handoff'
  if (via.type === 'data') {
    if (step.to.path) return 'Data-to-Endpoint Transition'
    return 'Data Operation'
  }
  if (step.to.path && (!root.path || step.to.path !== root.path)) {
    return 'Endpoint Transition'
  }
  return active || 'Internal Processing'
}

function groupNarrativeByPhase(steps: FlowNarrativeStep[], root: FlowEndpoint): NarrativePhase[] {
  if (!steps.length) return []
  const phases: NarrativePhase[] = []
  let activePhase = ''
  for (const step of steps) {
    const phase = inferNarrativePhase(step, root, activePhase)
    activePhase = phase
    const last = phases[phases.length - 1]
    if (!last || last.label !== phase) {
      phases.push({ label: phase, steps: [step] })
      continue
    }
    last.steps.push(step)
  }
  return phases
}

type NarrativeStepGroup = {
  fromHandler: string
  fromRepo: string
  items: FlowNarrativeStep[]
}

function groupStepsByOrigin(steps: FlowNarrativeStep[]): NarrativeStepGroup[] {
  const groups: NarrativeStepGroup[] = []
  for (const step of steps) {
    const handler = step.from?.handler || ''
    const repo = step.from?.repo || ''
    const last = groups[groups.length - 1]
    if (last && last.fromHandler === handler && last.fromRepo === repo) {
      last.items.push(step)
    } else {
      groups.push({ fromHandler: handler, fromRepo: repo, items: [step] })
    }
  }
  return groups
}

function narrativeGroupItemLabel(step: FlowNarrativeStep, groupFrom: string): string {
  if (step.kind === 'caller') {
    const caller = formatHandlerLabel(step.from)
    const callerMeta = formatEndpointMeta(step.from)
    const meta = callerMeta ? ` (${callerMeta})` : ''
    return `↘ ${caller}${meta}`
  }
  if (step.kind === 'branch') {
    return `⎇ ${(step.condition || 'unknown').trim()}`
  }
  if (step.kind === 'data-store') {
    const entity = step.via?.entity || formatEndpointLabel(step.to)
    return `◆ DATA ${entity} saved`
  }

  const via = step.via
  const toHandler = step.to?.handler || ''
  const toRepo = step.to?.repo || ''
  const fromRepo = step.from?.repo || ''
  const repoTag = toRepo && toRepo !== fromRepo ? ` [${toRepo}]` : ''

  if (via?.type === 'data') {
    const entity = via.entity || 'data'
    const table = via.table ? ` (${via.table})` : ''
    const target = formatHandlerLabel(step.to)
    return `◆ DATA ${entity}${table} → ${target}${repoTag}`
  }
  if (via?.type === 'http') {
    const target = formatEndpointLabel(step.to)
    return `→ HTTP ${target}${repoTag}`
  }
  if (via?.type === 'sqs') {
    const queue = via.queue || via.entity || 'queue'
    const target = formatHandlerLabel(step.to)
    return `→ SQS ${queue} → ${target}${repoTag}`
  }

  // Internal call — simplify by removing same-class prefix
  const { receiver: fromReceiver } = splitHandlerName(groupFrom)
  const { receiver: toReceiver, method: toMethod } = splitHandlerName(toHandler)
  if (fromReceiver && toReceiver && fromReceiver === toReceiver) {
    return `→ ${toMethod}${repoTag}`
  }
  return `→ ${toHandler || formatEndpointLabel(step.to)}${repoTag}`
}

type GroupedHop = {
  depth: number
  key: string
  via: FlowVia
  to: FlowEndpoint[]
  from: FlowEndpoint[]
  relatedCollapsed: boolean
}

const scopedHops = computed(() => {
  if (!result.value) return [] as FlowHop[]
  const hopList = result.value.hops ?? []
  if (!hopList.length) return [] as FlowHop[]
  if (!hasRootScopeFilter.value || rootScopeMode.value === 'all') return hopList

  const seed = new Set(scopedRoots.value.map((root) => endpointNodeKey(root)))
  if (!seed.size) return hopList

  const ordered = [...hopList].sort((a, b) => a.depth - b.depth)
  const reachable = new Set(seed)
  const filtered: FlowHop[] = []
  for (const hop of ordered) {
    const fromKey = endpointNodeKey(hop.from)
    if (!reachable.has(fromKey)) continue
    filtered.push(hop)
    reachable.add(endpointNodeKey(hop.to))
  }
  return filtered
})

const groupedHops = computed(() => {
  if (!result.value) return [] as Array<{ depth: number; hops: GroupedHop[] }>
  const hopList = scopedHops.value
  const groups = new Map<number, Map<string, GroupedHop>>()

  for (const hop of hopList) {
    if (!groups.has(hop.depth)) groups.set(hop.depth, new Map())
    const depthGroup = groups.get(hop.depth)!
    const isRelatedData = includeRelatedEntities.value && hop.via?.type === 'data'
    const key = isRelatedData
      ? [
          'related',
          hop.depth,
          (hop.via.entity || '').toLowerCase(),
          (hop.via.access || '').toLowerCase(),
          (hop.to.repo || '').toLowerCase(),
        ].join('|')
      : `strict|${rootKey(hop.to)}|${viaKey(hop.via)}`
    if (!depthGroup.has(key)) {
      depthGroup.set(key, {
        depth: hop.depth,
        key,
        via: hop.via,
        to: [],
        from: [],
        relatedCollapsed: isRelatedData,
      })
    }
    const grouped = depthGroup.get(key)!
    if (!grouped.from.find((f) => rootKey(f) === rootKey(hop.from))) {
      grouped.from.push(hop.from)
    }
    if (!grouped.to.find((t) => rootKey(t) === rootKey(hop.to))) {
      grouped.to.push(hop.to)
    }
  }

  const sortEndpoints = (items: FlowEndpoint[]) =>
    [...items].sort((a, b) => endpointSortKey(a).localeCompare(endpointSortKey(b)))

  return Array.from(groups.entries())
    .sort((a, b) => a[0] - b[0])
    .map(([depthValue, hops]) => ({
      depth: depthValue,
      hops: Array.from(hops.values())
        .map((hop) => ({
          ...hop,
          from: sortEndpoints(hop.from),
          to: sortEndpoints(hop.to),
        }))
        .sort((a, b) => {
          const viaDiff = viaPriority(a.via) - viaPriority(b.via)
          if (viaDiff !== 0) return viaDiff
          if (a.relatedCollapsed !== b.relatedCollapsed) return a.relatedCollapsed ? 1 : -1
          if (a.from.length !== b.from.length) return b.from.length - a.from.length
          if (a.to.length !== b.to.length) return b.to.length - a.to.length
          return a.key.localeCompare(b.key)
        }),
    }))
})

function endpointSortKey(endpoint: FlowEndpoint): string {
  return [
    (endpoint.repo || '').toLowerCase(),
    (endpoint.file || '').toLowerCase(),
    (endpoint.handler || '').toLowerCase(),
    (endpoint.method || '').toUpperCase(),
    (endpoint.path || '').toLowerCase(),
    endpoint.line || 0,
  ].join('|')
}

function viaPriority(via: FlowVia): number {
  if (via.type === 'call') return 0
  if (via.type === 'http') return 1
  if (via.type === 'sqs') return 2
  if (via.type === 'data') return 3
  return 4
}

function visibleHopsForDepth(group: { depth: number; hops: GroupedHop[] }): GroupedHop[] {
  if (!includeRelatedEntities.value) return group.hops
  if (expandedRelatedDepths.value[group.depth]) return group.hops
  const related = group.hops.filter((hop) => hop.relatedCollapsed)
  if (related.length <= relatedHopPreviewLimit) return group.hops
  const nonRelated = group.hops.filter((hop) => !hop.relatedCollapsed)
  return [...nonRelated, ...related.slice(0, relatedHopPreviewLimit)]
}

function isDepthCollapsed(group: { depth: number; hops: GroupedHop[] }): boolean {
  if (!includeRelatedEntities.value) return false
  if (expandedRelatedDepths.value[group.depth]) return false
  const related = group.hops.filter((hop) => hop.relatedCollapsed)
  return related.length > relatedHopPreviewLimit
}

function expandDepth(depthValue: number): void {
  expandedRelatedDepths.value = { ...expandedRelatedDepths.value, [depthValue]: true }
}

const flowSummaries = computed(() => {
  if (!result.value) return [] as string[]
  const roots = scopedRoots.value
  const hopList = scopedHops.value
  if (hopList.length === 0) return []

  const dataHops = hopList.filter((hop) => hop.via?.type === 'data')
  if (dataHops.length) {
    const groups = new Map<
      string,
      {
        entity: string
        rootRepo: string
        toRepo: string
        endpoints: Set<string>
        minDepth: number
      }
    >()
    for (const hop of dataHops) {
      const entityLabel = hop.via?.entity || 'data'
      const rootRepo = hop.from.repo || 'this service'
      const toRepo = hop.to.repo || 'another service'
      const key = `${rootRepo}|${toRepo}|${entityLabel}`
      const label = formatEndpointLabel(hop.to)
      if (!groups.has(key)) {
        groups.set(key, {
          entity: entityLabel,
          rootRepo,
          toRepo,
          endpoints: new Set(),
          minDepth: hop.depth,
        })
      }
      const group = groups.get(key)
      if (group) {
        group.minDepth = Math.min(group.minDepth, hop.depth)
        group.endpoints.add(label)
      }
    }

    const orderedGroups = Array.from(groups.values()).sort((a, b) => {
      if (a.minDepth !== b.minDepth) return a.minDepth - b.minDepth
      return a.endpoints.size - b.endpoints.size
    })

    return orderedGroups.map((group) => {
      const targets = Array.from(group.endpoints)
        .sort((a, b) => a.localeCompare(b))
      return `${group.rootRepo} shares indexed data access to ${group.entity} with ${group.toRepo} at ${targets.join(
        ', '
      )}`
    })
  }

  const ranked = [...hopList].sort((a, b) => {
    const scoreDiff = flowSummaryRank(b) - flowSummaryRank(a)
    if (scoreDiff !== 0) return scoreDiff
    return a.depth - b.depth
  })

  const summaries = new Map<
    string,
    {
      text: string
      score: number
      depth: number
    }
  >()
  for (const hop of ranked) {
    const root = roots.find((r) => r.repo && hop.from.repo && r.repo === hop.from.repo) ?? roots[0]
    const text = flowSummaryForHop(hop, root)
    const score = flowSummaryRank(hop)
    const existing = summaries.get(text)
    if (!existing || score > existing.score || (score === existing.score && hop.depth < existing.depth)) {
      summaries.set(text, {
        text,
        score,
        depth: hop.depth,
      })
    }
  }
  return Array.from(summaries.values())
    .sort((a, b) => {
      if (a.score !== b.score) return b.score - a.score
      if (a.depth !== b.depth) return a.depth - b.depth
      return a.text.localeCompare(b.text)
    })
    .map((item) => item.text)
})

const visibleFlowSummaries = computed(() => {
  if (!flowSummaries.value.length) return []
  if (summaryLimit.value === 'all') return flowSummaries.value
  return flowSummaries.value.slice(0, summaryLimit.value)
})

const hasMoreSummaries = computed(() => {
  if (summaryLimit.value === 'all') return false
  return flowSummaries.value.length > summaryLimit.value
})
</script>
