<template>
  <div class="max-w-7xl mx-auto p-4">
    <div class="mb-6">
      <div class="flex flex-col gap-3">
        <div class="flex gap-3">
          <textarea
            v-model="functionsInput"
            @keydown.enter.exact.prevent="runImpact()"
            placeholder="Function names (one per line or comma-separated)"
            class="flex-1 px-4 py-3 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none focus:outline-none focus:border-accent text-black dark:text-n-300 placeholder-gray-400 dark:placeholder-n-500 font-mono min-h-[90px]"
          />
          <button
            @click="runImpact()"
            :disabled="loading"
            class="px-6 py-3 bg-accent hover:bg-accent/80 disabled:bg-accent/40 disabled:cursor-not-allowed rounded-none font-medium uppercase tracking-wide transition-colors h-fit text-white"
          >
            {{ loading ? 'Analyzing...' : 'Impact' }}
          </button>
        </div>

        <div class="relative">
          <input
            v-model="searchQuery"
            @input="queueSearch"
            type="text"
            placeholder="Search exact functions to add (e.g., ResourceService.load)"
            class="w-full px-4 py-2 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none focus:outline-none focus:border-accent text-black dark:text-n-300 placeholder-gray-400 dark:placeholder-n-500 font-mono"
          />
          <div class="mt-2 text-xs text-n-500">
            Picker selections are added as exact function IDs. The textarea above remains broad name-based input.
          </div>
          <div v-if="searchQuery" class="mt-2 bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none overflow-hidden">
            <div v-if="searchLoading" class="px-3 py-2 text-xs text-n-500">Searching...</div>
            <div v-else-if="searchError" class="px-3 py-2 text-xs text-red-600 dark:text-red-400">{{ searchError }}</div>
            <div v-else-if="searchResults.length === 0" class="px-3 py-2 text-xs text-n-500">
              No matches found.
            </div>
            <div
              v-else-if="searchBucket"
              class="flex flex-wrap items-center justify-between gap-2 px-3 py-2 text-xs text-n-500 border-b border-gray-300 dark:border-n-600"
            >
              <div>{{ searchBucketSummary }}</div>
              <div class="flex gap-2">
                <button
                  @click="changeSearchPage(-1)"
                  :disabled="!canGoToPreviousSearchPage"
                  class="px-2 py-1 border border-gray-300 dark:border-n-600 rounded-none disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800/60"
                >
                  Prev
                </button>
                <button
                  @click="changeSearchPage(1)"
                  :disabled="!canGoToNextSearchPage"
                  class="px-2 py-1 border border-gray-300 dark:border-n-600 rounded-none disabled:opacity-40 disabled:cursor-not-allowed hover:bg-gray-100 dark:hover:bg-n-800/60"
                >
                  Next
                </button>
              </div>
            </div>
            <button
              v-for="fn in searchResults"
              :key="fn.id"
              @click="addExactFunctionTarget(fn)"
              class="w-full text-left px-3 py-2 text-sm text-black dark:text-n-300 hover:bg-gray-100 dark:hover:bg-n-800/60 border-t border-gray-300 dark:border-n-600"
            >
              <div class="font-mono">{{ fn.name }}</div>
              <div class="text-xs text-n-500">{{ fn.repo }} · {{ fn.file }}</div>
            </button>
          </div>
        </div>

        <div
          v-if="selectedFunctionTargets.length"
          class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-3"
        >
          <div class="text-xs text-n-500 uppercase tracking-widest mb-2">Exact Targets</div>
          <div class="space-y-2">
            <div
              v-for="target in selectedFunctionTargets"
              :key="target.id"
              class="flex items-start justify-between gap-3 text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-900/40"
            >
              <div>
                <div class="font-mono text-black dark:text-n-300">{{ target.name }}</div>
                <div class="text-xs text-n-500">{{ target.repo }} · {{ target.file }}</div>
              </div>
              <button
                @click="removeExactFunctionTarget(target.id)"
                class="text-xs uppercase tracking-wide text-n-500 hover:text-black dark:hover:text-n-300"
              >
                Remove
              </button>
            </div>
          </div>
        </div>

        <div class="flex flex-wrap gap-4 text-sm">
          <label class="flex items-center gap-2 text-n-400">
            <input type="checkbox" v-model="noTests" class="rounded-none h-4 w-4 bg-white dark:bg-n-800 border-gray-300 dark:border-n-600" />
            Hide tests
          </label>
          <label class="flex items-center gap-2 text-n-400">
            <input type="checkbox" v-model="resolveCalls" class="rounded-none h-4 w-4 bg-white dark:bg-n-800 border-gray-300 dark:border-n-600" />
            Resolve DI/impl
          </label>
          <div class="flex items-center gap-2">
            <label class="text-n-400 text-xs uppercase tracking-wide">Depth:</label>
            <input
              type="number"
              v-model.number="depth"
              min="1"
              max="10"
              class="w-16 px-2 py-1 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-black dark:text-n-300"
            />
          </div>
          <div class="flex items-center gap-2">
            <label class="text-n-400 text-xs uppercase tracking-wide">Max nodes:</label>
            <input
              type="number"
              v-model.number="maxNodes"
              min="100"
              max="10000"
              class="w-20 px-2 py-1 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-black dark:text-n-300"
            />
          </div>
          <button
            @click="showRangeTools = !showRangeTools"
            type="button"
            class="px-3 py-1.5 border border-gray-300 dark:border-n-600 rounded-none text-xs uppercase tracking-wide text-black dark:text-n-300 hover:bg-gray-100 dark:hover:bg-n-900"
          >
            {{ showRangeTools ? 'Hide Ranges / Diff' : 'Target Ranges / Diff' }}
          </button>
        </div>

        <div
          v-if="showRangeTools"
          class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-3"
        >
          <div class="text-sm text-n-400 mb-2 uppercase tracking-wide">File ranges (optional)</div>
          <div class="grid grid-cols-1 md:grid-cols-12 gap-2">
            <input
              v-model="rangeRepo"
              type="text"
              placeholder="repo (optional)"
              class="md:col-span-2 px-2 py-1 bg-gray-50 dark:bg-n-900 border border-gray-300 dark:border-n-600 rounded-none text-black dark:text-n-300"
            />
            <input
              v-model="rangePath"
              type="text"
              placeholder="path (e.g., src/main/.../Foo.java)"
              class="md:col-span-6 px-2 py-1 bg-gray-50 dark:bg-n-900 border border-gray-300 dark:border-n-600 rounded-none text-black dark:text-n-300"
            />
            <input
              v-model.number="rangeStart"
              type="number"
              placeholder="start"
              class="md:col-span-2 px-2 py-1 bg-gray-50 dark:bg-n-900 border border-gray-300 dark:border-n-600 rounded-none text-black dark:text-n-300"
            />
            <input
              v-model.number="rangeEnd"
              type="number"
              placeholder="end"
              class="md:col-span-2 px-2 py-1 bg-gray-50 dark:bg-n-900 border border-gray-300 dark:border-n-600 rounded-none text-black dark:text-n-300"
            />
            <div class="md:col-span-12 flex justify-end">
              <button
                @click="addRange()"
                class="px-3 py-1.5 bg-gray-100 dark:bg-n-900 hover:bg-gray-200 dark:hover:bg-n-600 rounded-none text-sm text-black dark:text-n-300 border border-gray-300 dark:border-n-600 uppercase tracking-wide"
              >
                Add range
              </button>
            </div>
          </div>
          <div v-if="ranges.length" class="mt-3 space-y-1">
            <div
              v-for="(range, index) in ranges"
              :key="rangeKey(range, index)"
              class="flex items-center justify-between text-xs text-black dark:text-n-300 bg-gray-50 dark:bg-n-900/50 border border-gray-300 dark:border-n-600 rounded-none px-2 py-1"
            >
              <span class="font-mono">
                {{ range.repo ? range.repo + ':' : '' }}{{ range.path }} [{{ range.startLine }}-{{ range.endLine }}]
              </span>
              <button @click="removeRange(index)" class="text-n-400 hover:text-black dark:hover:text-n-300">Remove</button>
            </div>
          </div>

          <div class="mt-4 border-t border-gray-300 dark:border-n-600 pt-3">
            <div class="text-xs text-n-500 mb-2 uppercase tracking-widest">Paste git diff to auto-add ranges</div>
            <textarea
              v-model="diffInput"
              placeholder="git diff -U0 main...HEAD"
              class="w-full px-3 py-2 bg-gray-50 dark:bg-n-900 border border-gray-300 dark:border-n-600 rounded-none text-black dark:text-n-300 font-mono text-xs min-h-[90px]"
            ></textarea>
            <div class="flex flex-wrap items-center justify-between gap-2 mt-2">
              <input
                v-model="diffRepo"
                type="text"
                placeholder="repo for diff (optional)"
                class="px-2 py-1 bg-gray-50 dark:bg-n-900 border border-gray-300 dark:border-n-600 rounded-none text-black dark:text-n-300 text-xs"
              />
              <button
                @click="addDiffRanges"
                class="px-3 py-1.5 bg-gray-100 dark:bg-n-900 hover:bg-gray-200 dark:hover:bg-n-600 rounded-none text-xs text-black dark:text-n-300 border border-gray-300 dark:border-n-600 uppercase tracking-wide"
              >
                Add ranges from diff
              </button>
            </div>
            <div v-if="diffMessage" class="mt-2 text-xs text-n-500">{{ diffMessage }}</div>
          </div>
        </div>
      </div>
    </div>

    <div v-if="loading" class="text-center py-12">
      <div class="animate-spin w-8 h-8 border-2 border-accent border-t-transparent mx-auto"></div>
      <p class="mt-3 text-n-400">Building impact report...</p>
    </div>

    <div v-else-if="error" class="border-2 border-red-500 bg-red-50 dark:bg-red-950 rounded-none p-4 text-red-600 dark:text-red-400">
      {{ error }}
    </div>

      <div v-else-if="result">
        <div
          v-if="impactWarnings.length"
          class="mb-4 border-2 border-amber-500 bg-amber-50 dark:bg-amber-950 rounded-none p-4 text-amber-700 dark:text-amber-300"
        >
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div>
              <div class="text-xs uppercase tracking-widest font-semibold mb-2">Impact Warnings</div>
              <div class="space-y-1 text-sm">
                <div v-for="warning in impactWarnings" :key="warning">{{ warning }}</div>
                <div v-if="result.completeness.truncated" class="text-xs text-amber-800/80 dark:text-amber-200/80">
                  Current cap: {{ result.completeness.appliedMaxNodes }} nodes per upstream/downstream tree. Summary counts below reflect the current returned graph.
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

        <div class="mb-3 text-sm text-n-400">
          Current returned blast radius touches
          <span class="font-semibold text-black dark:text-n-300">{{ entrypoints.length }}</span> entrypoints,
          <span class="font-semibold text-black dark:text-n-300">{{ httpCalls.length }}</span> HTTP calls,
          <span class="font-semibold text-black dark:text-n-300">{{ queues.length }}</span> queues,
          <span v-if="ebSchedules.length">
            <span class="font-semibold text-black dark:text-n-300">{{ ebSchedules.length }}</span> schedules,
          </span>
          and <span class="font-semibold text-black dark:text-n-300">{{ repos.length }}</span> repos.
        </div>
        <div class="flex flex-wrap items-center gap-6 mb-4 text-sm text-n-400">
          <span v-if="result.workspace">Workspace: {{ result.workspace.id }}</span>
          <span v-if="result.repoContext?.length">Indexed repos: {{ result.repoContext.length }}</span>
          <span>Roots: {{ result.stats.roots }}</span>
          <span>Downstream: {{ impactDirectionSummary('downstream') }}</span>
          <span>Upstream: {{ impactDirectionSummary('upstream') }}</span>
          <span>Total report: {{ result.stats.totalNodes }} nodes</span>
          <span>Time: {{ result.stats.impactTime }}</span>
        </div>

      <div
        v-if="visibleRootIntegrations.length"
        class="mb-4 bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4"
      >
        <div class="text-xs text-n-500 uppercase tracking-widest mb-2">Connected Integrations</div>
        <div class="text-xs text-n-500 mb-3">Matched or inferred cross-repo HTTP integrations for the selected root functions.</div>
        <div class="space-y-3">
          <div
            v-for="group in visibleRootIntegrations"
            :key="group.callerId"
            class="border border-gray-300 dark:border-n-600 rounded-none px-3 py-3 bg-gray-50 dark:bg-n-900/40"
          >
            <div class="font-mono text-sm text-black dark:text-n-300">{{ group.name }}</div>
            <div class="text-xs text-n-500 mt-1">{{ group.repo }} · {{ group.file }}</div>
            <div class="mt-3 space-y-2">
              <div
                v-for="integration in group.integrations"
                :key="impactIntegrationKey(group.callerId, integration)"
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

      <div class="mb-4 text-xs text-n-500 space-y-0.5">
        <div><strong class="text-n-400">Score</strong> — relevance (0–1) based on call-graph distance. Depth 1 ≈ 0.85, deeper = lower.</div>
        <div><strong class="text-n-400">Fanout</strong> — number of downstream paths from the entrypoint to the target. 1 = direct chain, 2+ = branching.</div>
      </div>

      <div
        v-if="impactInsights.length"
        class="mb-4 bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4"
      >
        <div class="text-xs text-n-500 uppercase tracking-widest mb-2">Impact Summary</div>
        <div class="space-y-1 text-sm text-n-300">
          <div v-for="insight in impactInsights" :key="insight">
            {{ insight }}
          </div>
        </div>
      </div>

      <div
        v-if="changedTargets.length"
        class="mb-4 bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4"
      >
        <div class="text-xs text-n-500 uppercase tracking-widest mb-2">Changed Targets</div>
        <div class="space-y-2">
          <div
            v-for="group in changedTargets"
            :key="group.repo + ':' + group.file"
            class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-900/40"
          >
            <div class="flex items-center justify-between gap-3">
              <span class="font-mono text-black dark:text-n-300">{{ group.repo }}:{{ group.file }}</span>
              <span class="text-xs text-n-400">{{ group.count }} root{{ group.count === 1 ? '' : 's' }}</span>
            </div>
            <div class="mt-2 text-xs text-n-500 whitespace-normal break-words">
              {{ group.functions.join(', ') }}
            </div>
          </div>
        </div>
      </div>

      <div
        v-if="changeAreas.length"
        class="mb-4 bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4"
      >
        <div class="text-xs text-n-500 uppercase tracking-widest mb-2">What Changed</div>
        <div class="text-sm text-n-300">
          Primary change areas: {{ changeAreas.join(', ') }}.
        </div>
        <div class="mt-1 text-xs text-n-500">
          Impact signals: {{ entrypoints.length }} entrypoints, {{ httpCalls.length }} outbound HTTP calls, {{ queues.length }} queues.
        </div>
      </div>

      <div
        v-if="reportNodes.length || reportEdges.length"
        class="mb-6 bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4"
      >
        <div class="flex flex-wrap items-center justify-between gap-3 mb-3">
          <div>
            <div class="text-xs text-n-500 uppercase tracking-widest mb-1">Report Graph</div>
            <div class="text-sm text-n-300">
              {{ reportNodes.length }} nodes, {{ reportEdges.length }} edges.
            </div>
          </div>
          <div class="flex flex-wrap gap-2 text-xs text-n-500">
            <span
              v-for="bucket in reportTypeSummary"
              :key="bucket.label"
              class="border border-gray-300 dark:border-n-600 px-2 py-1 rounded-none"
            >
              {{ bucket.label }}: {{ bucket.count }}
            </span>
          </div>
        </div>

        <div class="grid grid-cols-1 xl:grid-cols-2 gap-6">
          <div>
            <div class="text-xs text-n-500 uppercase tracking-widest mb-2">Nodes</div>
            <div class="max-h-[26rem] overflow-auto border border-gray-300 dark:border-n-600 rounded-none">
              <div
                v-for="node in reportNodes"
                :key="node.id"
                class="px-3 py-2 border-t first:border-t-0 border-gray-300 dark:border-n-600 bg-gray-50 dark:bg-n-900/40"
              >
                <div class="flex items-center justify-between gap-3">
                  <div class="font-mono text-sm text-black dark:text-n-300 break-words">
                    {{ formatNodeLabel(node) }}
                  </div>
                  <div class="text-[10px] uppercase tracking-wide text-n-500 whitespace-nowrap">
                    {{ node.type }}
                  </div>
                </div>
                <div class="mt-1 text-xs text-n-500 break-all">{{ node.id }}</div>
              </div>
            </div>
          </div>

          <div>
            <div class="text-xs text-n-500 uppercase tracking-widest mb-2">Edges</div>
            <div class="max-h-[26rem] overflow-auto border border-gray-300 dark:border-n-600 rounded-none">
              <div
                v-for="edge in reportEdges"
                :key="edge.key"
                class="px-3 py-2 border-t first:border-t-0 border-gray-300 dark:border-n-600 bg-gray-50 dark:bg-n-900/40"
              >
                <div class="flex items-center justify-between gap-3">
                  <div class="text-[10px] uppercase tracking-wide text-n-500 whitespace-nowrap">
                    {{ edge.type }}
                  </div>
                  <div v-if="edge.confidence" class="text-[10px] uppercase tracking-wide text-n-500 whitespace-nowrap">
                    {{ edge.confidence }}
                  </div>
                </div>
                <div class="mt-1 text-sm text-black dark:text-n-300 break-words">
                  {{ edge.sourceLabel }}
                </div>
                <div class="text-xs text-n-500 my-1">-&gt;</div>
                <div class="text-sm text-black dark:text-n-300 break-words">
                  {{ edge.targetLabel }}
                </div>
                <div v-if="edge.evidence" class="mt-2 text-xs text-n-500 break-words">
                  {{ edge.evidence }}
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div class="grid grid-cols-1 lg:grid-cols-2 gap-6">
        <div class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4">
          <h2 class="text-lg font-bold uppercase tracking-wide mb-1 text-black dark:text-n-300">Entrypoints</h2>
          <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Upstream endpoints that reach this code.</div>
          <div v-if="entrypoints.length === 0" class="text-n-500 text-sm">
            No entrypoints found.
          </div>
          <div v-else class="space-y-2">
            <div
              v-for="entry in entrypoints"
              :key="entry.repo + entry.path + entry.handler"
              class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-900/40"
            >
              <div class="flex items-center justify-between">
                <span class="font-mono text-black dark:text-n-300">{{ entry.method }} {{ entry.path }}</span>
                <span class="text-xs text-n-400">score {{ formatScore(entry.score) }}</span>
              </div>
              <div class="text-xs text-n-400 mt-1">
                {{ entry.repo }} · {{ entry.handler }} · depth {{ entry.minDepth }} · fanout {{ entry.fanout }}
              </div>
              <div v-if="entrypointEvidence(entry)" class="mt-2 text-xs text-n-500 whitespace-normal break-words">
                evidence: {{ entrypointEvidence(entry) }}
              </div>
            </div>
          </div>
        </div>

        <div class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4">
          <h2 class="text-lg font-bold uppercase tracking-wide mb-1 text-black dark:text-n-300">HTTP Calls</h2>
          <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Outbound HTTP client calls.</div>
          <div v-if="httpCalls.length === 0" class="text-n-500 text-sm">
            No HTTP calls found.
          </div>
          <div v-else class="space-y-2">
            <div
              v-for="call in httpCalls"
              :key="call.method + call.path"
              class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-900/40"
            >
              <div class="flex items-center justify-between">
                <span class="font-mono text-black dark:text-n-300">{{ call.method }} {{ call.path }}</span>
                <span class="text-xs text-n-400">score {{ formatScore(call.score) }}</span>
              </div>
              <div class="text-xs text-n-400 mt-1">
                depth {{ call.minDepth }} · fanout {{ call.fanout }}
              </div>
              <div v-if="call.matches?.length" class="text-xs text-n-500 mt-2">
                <div>matches:</div>
                <div class="mt-1 space-y-1">
                <div
                  v-for="match in call.matches"
                  :key="matchKey(match)"
                  class="font-mono text-xs text-n-300 leading-snug whitespace-normal break-normal"
                >
                    <template v-for="(segment, index) in splitMatchLabel(match)" :key="index">
                      <span>{{ segment }}</span>
                      <wbr />
                    </template>
                  </div>
                </div>
              </div>
              <div v-if="httpEvidence(call)" class="mt-2 text-xs text-n-500 whitespace-normal break-words">
                evidence: {{ httpEvidence(call) }}
              </div>
            </div>
          </div>
        </div>

        <div class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4">
          <h2 class="text-lg font-bold uppercase tracking-wide mb-1 text-black dark:text-n-300">Queues</h2>
          <div class="text-xs text-n-500 uppercase tracking-widest mb-3">SQS sends/consumes touched.</div>
          <div v-if="queues.length === 0" class="text-n-500 text-sm">
            No queues found.
          </div>
          <div v-else class="space-y-2">
            <div
              v-for="queue in queues"
              :key="queue.name"
              class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-900/40"
            >
              <div class="flex items-center justify-between">
                <span class="font-mono text-black dark:text-n-300">{{ queue.name }}</span>
                <span class="text-xs text-n-400">score {{ formatScore(queue.score) }}</span>
              </div>
              <div class="text-xs text-n-400 mt-1">
                depth {{ queue.minDepth }} · fanout {{ queue.fanout }}
              </div>
              <div v-if="queueEvidence(queue.name)" class="mt-2 text-xs text-n-500 whitespace-normal break-words">
                evidence: {{ queueEvidence(queue.name) }}
              </div>
            </div>
          </div>
        </div>

        <div v-if="ebSchedules.length" class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4">
          <h2 class="text-lg font-bold uppercase tracking-wide mb-1 text-black dark:text-n-300">EventBridge Schedules</h2>
          <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Cron/rate schedules that trigger this code.</div>
          <div class="space-y-2">
            <div
              v-for="schedule in ebSchedules"
              :key="schedule.name"
              class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-900/40"
            >
              <div class="flex items-center justify-between">
                <span class="font-mono text-black dark:text-n-300">{{ schedule.name }}</span>
                <span class="text-xs text-n-400">score {{ formatScore(schedule.score) }}</span>
              </div>
              <div class="text-xs text-n-400 mt-1">
                <span v-if="schedule.schedule">{{ schedule.schedule }} · </span>
                <span v-if="schedule.state">{{ schedule.state }} · </span>
                depth {{ schedule.minDepth }} · fanout {{ schedule.fanout }}
              </div>
            </div>
          </div>
        </div>

        <div class="bg-white dark:bg-n-800 border-2 border-black dark:border-n-600 rounded-none p-4">
          <h2 class="text-lg font-bold uppercase tracking-wide mb-1 text-black dark:text-n-300">Repos</h2>
          <div class="text-xs text-n-500 uppercase tracking-widest mb-3">Repos touched by the trace.</div>
          <div v-if="repos.length === 0" class="text-n-500 text-sm">
            No repos found.
          </div>
          <div v-else class="space-y-2">
            <div
              v-for="repo in repos"
              :key="repo.name"
              class="text-sm border border-gray-300 dark:border-n-600 rounded-none px-3 py-2 bg-gray-50 dark:bg-n-900/40"
            >
              <div class="flex items-center justify-between">
                <span class="font-mono text-black dark:text-n-300">{{ repo.name }}</span>
                <span class="text-xs text-n-400">score {{ formatScore(repo.score) }}</span>
              </div>
              <div class="text-xs text-n-400 mt-1">
                depth {{ repo.minDepth }} · fanout {{ repo.fanout }}
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted, watch } from 'vue'
import { useRoute } from 'vue-router'
import { api, ApiError } from '@/api/client'
import { useWorkspace } from '@/composables/useWorkspace'
import type { FunctionIntegration, ImpactRange, ImpactResponse, SearchBucketStats } from '@/types'

interface ImpactFunctionSearchResult {
  id: number
  name: string
  repo: string
  file: string
}

interface RootGroup {
  repo: string
  file: string
  count: number
  functions: string[]
}

interface ImpactEvidencePaths {
  entrypoints: Record<string, string>
  http: Record<string, string>
  queues: Record<string, string>
}

interface ReportTypeBucket {
  label: string
  count: number
}

interface ImpactReportEdgeView {
  key: string
  type: string
  confidence?: string
  sourceLabel: string
  targetLabel: string
  evidence: string
}

const functionsInput = ref('')
const { workspaceId } = useWorkspace()
let impactAbort: AbortController | null = null
const noTests = ref(true)
const resolveCalls = ref(true)
const depth = ref(4)
const maxNodes = ref(2000)
const showRangeTools = ref(false)

const rangeRepo = ref('')
const rangePath = ref('')
const rangeStart = ref<number | null>(null)
const rangeEnd = ref<number | null>(null)
const ranges = ref<ImpactRange[]>([])
const diffInput = ref('')
const diffRepo = ref('')
const diffMessage = ref('')

const loading = ref(false)
const error = ref('')
const result = ref<ImpactResponse | null>(null)
const rootIntegrations = ref<Record<string, FunctionIntegration[]>>({})
const entrypoints = computed(() => result.value?.summary?.entrypoints ?? [])
const httpCalls = computed(() => result.value?.summary?.httpCalls ?? [])
const queues = computed(() => result.value?.summary?.queues ?? [])
const ebSchedules = computed(() => result.value?.summary?.eventBridgeSchedules ?? [])
const repos = computed(() => result.value?.summary?.repos ?? [])
const impactWarnings = computed(() => result.value?.warnings ?? [])
const changedTargets = computed<RootGroup[]>(() => groupRootsByFile(result.value?.roots ?? [], result.value?.report?.nodes ?? []))
const changeAreas = computed(() => summarizeChangeAreas(changedTargets.value))
const impactEvidence = computed<ImpactEvidencePaths>(() => buildImpactEvidencePaths(result.value))
const reportNodes = computed(() => result.value?.report?.nodes ?? [])
const reportEdges = computed<ImpactReportEdgeView[]>(() => {
  if (!result.value) return []
  const nodeIndex = new Map(reportNodes.value.map((node) => [node.id, node]))
  return result.value.report.edges.map((edge, index) => ({
    key: `${edge.source}:${edge.target}:${edge.type}:${index}`,
    type: edge.type,
    confidence: edge.confidence,
    sourceLabel: formatNodeLabel(nodeIndex.get(edge.source) ?? {
      id: edge.source,
      type: 'unknown',
      name: edge.source,
    }),
    targetLabel: formatNodeLabel(nodeIndex.get(edge.target) ?? {
      id: edge.target,
      type: 'unknown',
      name: edge.target,
    }),
    evidence: formatEdgeEvidence(edge.evidence),
  }))
})
const reportTypeSummary = computed<ReportTypeBucket[]>(() => {
  const buckets = new Map<string, number>()
  for (const node of reportNodes.value) {
    const label = node.type || 'unknown'
    buckets.set(label, (buckets.get(label) ?? 0) + 1)
  }
  return Array.from(buckets.entries())
    .map(([label, count]) => ({ label, count }))
    .sort((a, b) => (a.count === b.count ? a.label.localeCompare(b.label) : b.count - a.count))
})

type ImpactRootIntegrationGroup = {
  callerId: string
  name: string
  repo: string
  file: string
  integrations: FunctionIntegration[]
}

const impactRootIntegrationGroups = computed<ImpactRootIntegrationGroup[]>(() => {
  if (!result.value) return []
  const nodes = new Map(result.value.report.nodes.map((node) => [node.id, node]))
  return rootNodeIDs(result.value.roots, nodes)
    .map((id) => nodes.get(id))
    .filter((node): node is NonNullable<typeof node> => Boolean(node && node.type === 'function' && node.repo && node.file && node.name))
    .map((node) => {
      const callerId = `${node.repo}:${node.file}:${node.name}`
      return {
        callerId,
        name: node.name,
        repo: node.repo!,
        file: node.file!,
        integrations: rootIntegrations.value[callerId] ?? [],
      }
    })
})

const visibleRootIntegrations = computed(() =>
  impactRootIntegrationGroups.value.filter((group) => group.integrations.length > 0)
)

const impactInsights = computed(() => {
  if (!result.value) return [] as string[]
  const insights: string[] = []

  const coverageParts = [
    `${entrypoints.value.length} entrypoints`,
    `${httpCalls.value.length} outbound HTTP calls`,
    `${queues.value.length} queues`,
  ]
  if (ebSchedules.value.length) {
    coverageParts.push(`${ebSchedules.value.length} EventBridge schedules`)
  }
  coverageParts.push(`${repos.value.length} repos`)
  insights.push(`Coverage: ${coverageParts.join(', ')}.`)

  const highestRiskEntrypoint = [...entrypoints.value].sort((a, b) => b.score - a.score)[0]
  if (highestRiskEntrypoint) {
    insights.push(
      `Top entrypoint by score: ${highestRiskEntrypoint.method} ${highestRiskEntrypoint.path} (${highestRiskEntrypoint.repo}, score ${formatScore(highestRiskEntrypoint.score)}).`
    )
  }

  const widestHttp = [...httpCalls.value].sort((a, b) => b.fanout - a.fanout || a.minDepth - b.minDepth)[0]
  if (widestHttp) {
    insights.push(
      `Widest outbound HTTP edge: ${widestHttp.method} ${widestHttp.path} (fanout ${widestHttp.fanout}, depth ${widestHttp.minDepth}).`
    )
  }

  const firstQueue = [...queues.value].sort((a, b) => a.minDepth - b.minDepth || b.fanout - a.fanout)[0]
  if (firstQueue) {
    insights.push(
      `Earliest queue touchpoint: ${firstQueue.name} (depth ${firstQueue.minDepth}, fanout ${firstQueue.fanout}).`
    )
  }

  const firstSchedule = [...ebSchedules.value].sort((a, b) => b.score - a.score)[0]
  if (firstSchedule) {
    insights.push(
      `EventBridge trigger: ${firstSchedule.name}${firstSchedule.schedule ? ` (${firstSchedule.schedule})` : ''}.`
    )
  }

  const mostImpactedRepo = [...repos.value].sort((a, b) => b.score - a.score)[0]
  if (mostImpactedRepo) {
    insights.push(
      `Most impacted repo: ${mostImpactedRepo.name} (score ${formatScore(mostImpactedRepo.score)}).`
    )
  }

  return insights
})

const searchQuery = ref('')
const searchResults = ref<ImpactFunctionSearchResult[]>([])
const searchBucket = ref<SearchBucketStats | null>(null)
const selectedFunctionTargets = ref<ImpactFunctionSearchResult[]>([])
const searchLoading = ref(false)
const searchError = ref('')
const searchLimit = 25
let searchTimer: number | null = null
let searchAbort: AbortController | null = null
const route = useRoute()

const canGoToPreviousSearchPage = computed(() => (searchBucket.value?.offset ?? 0) > 0)
const canGoToNextSearchPage = computed(() => Boolean(searchBucket.value?.hasMore))
const searchBucketSummary = computed(() => {
  const bucket = searchBucket.value
  if (!bucket) return ''
  const start = bucket.total === 0 ? 0 : bucket.offset + 1
  const end = bucket.offset + bucket.returned
  return `Showing ${start}-${end} of ${bucket.total} matching functions`
})

function parseFunctions(): string[] {
  return functionsInput.value
    .split(/[\n,]/)
    .map((name) => name.trim())
    .filter(Boolean)
}

function impactIntegrationKey(callerId: string, integration: FunctionIntegration) {
  return [
    callerId,
    integration.method,
    integration.path,
    integration.targetRepo,
    integration.targetHandler ?? '',
    integration.lineNumber,
    integration.resolution,
  ].join('|')
}

function addFunctionsFromQuery(value: unknown) {
  if (!value) {
    return
  }
  const raw = Array.isArray(value) ? value : [value]
  const candidates = raw
    .flatMap((entry) => String(entry).split(','))
    .map((name) => name.trim())
    .filter(Boolean)
  if (candidates.length === 0) {
    return
  }
  const existing = parseFunctions()
  const merged = [...existing]
  for (const name of candidates) {
    if (!merged.includes(name)) {
      merged.push(name)
    }
  }
  functionsInput.value = merged.join('\n')
}

function addRange() {
  if (!rangePath.value || rangeStart.value == null || rangeEnd.value == null) {
    return
  }
  addRangeIfNew({
    repo: rangeRepo.value.trim() || undefined,
    path: rangePath.value.trim(),
    startLine: Number(rangeStart.value),
    endLine: Number(rangeEnd.value),
  })
  rangePath.value = ''
  rangeStart.value = null
  rangeEnd.value = null
}

function removeRange(index: number) {
  ranges.value.splice(index, 1)
}

function rangeKey(range: ImpactRange, index: number) {
  return `${range.repo || 'any'}:${range.path}:${range.startLine}:${range.endLine}:${index}`
}

function formatScore(score: number) {
  return score.toFixed(2)
}

function matchKey(match: { repo: string; handler: string }) {
  return `${match.repo}:${match.handler}`
}

function splitMatchLabel(match: { repo: string; handler: string }) {
  return splitForWrap(`${match.repo}.${match.handler}`)
}

function entrypointEvidence(entry: { repo: string; handler: string }) {
  return impactEvidence.value.entrypoints[entrypointNodeKey(entry.repo, entry.handler)] || ''
}

function httpEvidence(call: { method: string; path: string }) {
  return impactEvidence.value.http[httpKey(call.method, call.path)] || ''
}

function queueEvidence(name: string) {
  return impactEvidence.value.queues[name] || ''
}

function formatEdgeEvidence(evidence?: { source?: string; detail?: string }) {
  if (!evidence?.source && !evidence?.detail) return ''
  if (evidence?.source && evidence?.detail) return `${evidence.source}: ${evidence.detail}`
  return evidence?.detail || evidence?.source || ''
}

function splitForWrap(value: string) {
  const parts = value.split(/([./_-])/g).filter((segment) => segment.length > 0)
  const segments: string[] = []
  for (const part of parts) {
    if (part.length === 1 && (part === '.' || part === '/' || part === '_' || part === '-')) {
      segments.push(part)
      continue
    }
    segments.push(...splitCamel(part))
  }
  return segments
}

function splitCamel(value: string) {
  const segments: string[] = []
  let current = ''
  for (let index = 0; index < value.length; index += 1) {
    const char = value[index]
    const prev = index > 0 ? value[index - 1] : ''
    if (index > 0 && isLowerOrDigit(prev) && isUpper(char)) {
      if (current.length > 0) {
        segments.push(current)
      }
      current = char
      continue
    }
    current += char
  }
  if (current.length > 0) {
    segments.push(current)
  }
  return segments
}

function isUpper(value: string) {
  return value >= 'A' && value <= 'Z'
}

function isLowerOrDigit(value: string) {
  return (value >= 'a' && value <= 'z') || (value >= '0' && value <= '9')
}

function addRangeIfNew(range: ImpactRange) {
  const key = `${range.repo || ''}:${range.path}:${range.startLine}:${range.endLine}`
  const existing = new Set(
    ranges.value.map((r) => `${r.repo || ''}:${r.path}:${r.startLine}:${r.endLine}`)
  )
  if (existing.has(key)) {
    return
  }
  ranges.value.push(range)
}

function addDiffRanges() {
  diffMessage.value = ''
  if (!diffInput.value.trim()) {
    diffMessage.value = 'Paste a diff to add ranges.'
    return
  }
  const parsed = parseUnifiedDiff(diffInput.value, diffRepo.value.trim())
  if (parsed.length === 0) {
    diffMessage.value = 'No hunks found.'
    return
  }
  for (const range of parsed) {
    addRangeIfNew(range)
  }
  diffMessage.value = `Added ${parsed.length} ranges.`
}

function impactDirectionSummary(direction: 'downstream' | 'upstream') {
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

async function rerunWithMaxNodes(nextValue: number) {
  if (loading.value || nextValue === maxNodes.value) {
    return
  }
  maxNodes.value = nextValue
  await runImpact()
}

function resetSearchResults() {
  searchResults.value = []
  searchBucket.value = null
  searchLoading.value = false
}

function queueSearch() {
  searchAbort?.abort()
  searchAbort = null
  searchError.value = ''
  if (searchTimer) {
    window.clearTimeout(searchTimer)
  }
  if (!searchQuery.value.trim()) {
    resetSearchResults()
    return
  }
  searchTimer = window.setTimeout(() => {
    searchTimer = null
    runSearch(searchQuery.value.trim(), 0)
  }, 250)
}

async function runSearch(query: string, functionsOffset = 0) {
  searchLoading.value = true
  if (searchAbort) {
    searchAbort.abort()
  }
  const controller = new AbortController()
  searchAbort = controller
  try {
    const resp = await api.search(
      query,
      'keyword',
      searchLimit,
      undefined,
      undefined,
      controller.signal,
      undefined,
      { functions: functionsOffset }
    )
    if (controller.signal.aborted || searchAbort !== controller) return
    searchResults.value = resp.results.functions.map((fn) => ({
      id: fn.id,
      name: fn.name,
      repo: fn.repo,
      file: fn.file,
    }))
    searchBucket.value = resp.stats.buckets.functions
  } catch (err) {
    if (controller.signal.aborted || searchAbort !== controller) return
    if ((err as Error).name === 'AbortError') {
      return
    }
    searchError.value = 'Search failed.'
    searchBucket.value = null
  } finally {
    if (searchAbort === controller) searchLoading.value = false
  }
}

function changeSearchPage(direction: 1 | -1) {
  const bucket = searchBucket.value
  const query = searchQuery.value.trim()
  if (!bucket || !query) {
    return
  }
  if (direction < 0 && bucket.offset === 0) {
    return
  }
  if (direction > 0 && !bucket.hasMore) {
    return
  }
  const nextOffset = direction < 0 ? Math.max(bucket.offset - bucket.limit, 0) : bucket.offset + bucket.limit
  void runSearch(query, nextOffset)
}

function addExactFunctionTarget(target: ImpactFunctionSearchResult) {
  cancelSearch()
  if (!selectedFunctionTargets.value.some((existing) => existing.id === target.id)) {
    selectedFunctionTargets.value = [...selectedFunctionTargets.value, target]
  }
  searchQuery.value = ''
  resetSearchResults()
}

function removeExactFunctionTarget(id: number) {
  selectedFunctionTargets.value = selectedFunctionTargets.value.filter((target) => target.id !== id)
}

function parseUnifiedDiff(diffText: string, repo: string): ImpactRange[] {
  const ranges: ImpactRange[] = []
  const lines = diffText.split('\n')
  let currentFile = ''

  for (const line of lines) {
    if (line.startsWith('diff --git ')) {
      const parts = line.split(' ')
      if (parts.length >= 4) {
        currentFile = normalizeDiffPath(parts[3])
      }
      continue
    }
    if (line.startsWith('+++ ')) {
      const path = line.replace('+++', '').trim()
      if (path === '/dev/null') {
        currentFile = ''
        continue
      }
      currentFile = normalizeDiffPath(path)
      continue
    }
    if (line.startsWith('@@ ')) {
      if (!currentFile) {
        continue
      }
      const match = /@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@/.exec(line)
      if (!match) {
        continue
      }
      const start = Number(match[1])
      const count = match[2] ? Number(match[2]) : 1
      if (!count) {
        continue
      }
      ranges.push({
        repo: repo || undefined,
        path: currentFile,
        startLine: start,
        endLine: start + count - 1,
      })
    }
  }
  return ranges
}

function normalizeDiffPath(path: string) {
  return path.replace(/^a\//, '').replace(/^b\//, '').replace(/\\/g, '/')
}

function cleanRepoName(name: string) {
  if (!name) return ''
  const atIndex = name.indexOf('@')
  return atIndex > 0 ? name.slice(0, atIndex) : name
}

function splitRoot(root: string) {
  const first = root.indexOf(':')
  const last = root.lastIndexOf(':')
  if (first === -1 || last === -1 || first === last) {
    return { repo: '', file: '', fn: '' }
  }
  return {
    repo: root.slice(0, first),
    file: root.slice(first + 1, last),
    fn: root.slice(last + 1),
  }
}

function sampleList(values: string[], limit: number) {
  if (limit <= 0 || values.length <= limit) {
    return values
  }
  return values.slice(0, limit)
}

function groupRootsByFile(roots: string[], nodes: ImpactResponse['report']['nodes']): RootGroup[] {
  const nodeIndex = new Map(nodes.map((node) => [node.id, node]))
  const grouped = new Map<string, RootGroup>()
  for (const root of roots) {
    const { repo: rawRepo, file, fn } = splitRoot(root)
    const repo = cleanRepoName(rawRepo)
    if (!repo || !file || !fn) {
      continue
    }
    const key = `${repo}|${file}`
    let group = grouped.get(key)
    if (!group) {
      group = { repo, file, count: 0, functions: [] }
      grouped.set(key, group)
    }
    group.count += 1
    const node = nodeIndex.get(`func:${root}`)
    const label = node?.line ? `${fn}:${node.line}` : fn
    group.functions.push(label)
  }
  return Array.from(grouped.values())
    .map((group) => ({
      ...group,
      functions: sampleList([...group.functions].sort(), 4),
    }))
    .sort((a, b) => {
      if (a.count !== b.count) return b.count - a.count
      if (a.repo !== b.repo) return a.repo.localeCompare(b.repo)
      return a.file.localeCompare(b.file)
    })
}

function summarizePathArea(file: string) {
  if (!file) return ''
  const normalized = file.replace(/\\/g, '/')
  const parts = normalized.split('/').filter(Boolean)
  if (parts.length === 0) return ''
  const base = parts[parts.length - 1]
  const parent = parts.length > 1 ? parts[parts.length - 2] : ''
  return parent ? `${parent}/${base}` : base
}

function summarizeChangeAreas(groups: RootGroup[]) {
  const repoSet = new Set(groups.map((group) => group.repo).filter(Boolean))
  const multiRepo = repoSet.size > 1
  const counts = new Map<string, number>()
  for (const group of groups) {
    const area = summarizePathArea(group.file)
    if (!area) continue
    const label = multiRepo ? `${group.repo} · ${area}` : area
    counts.set(label, (counts.get(label) ?? 0) + group.count)
  }
  return Array.from(counts.entries())
    .sort((a, b) => (a[1] === b[1] ? a[0].localeCompare(b[0]) : b[1] - a[1]))
    .slice(0, 3)
    .map(([label]) => label)
}

function httpKey(method?: string, path?: string) {
  return `${method ?? ''} ${path ?? ''}`.trim()
}

function entrypointNodeKey(repo?: string, handler?: string) {
  return repo && handler ? `${repo}|${handler}` : ''
}

function shortFile(node: NonNullable<ImpactResponse['report']>['nodes'][number]) {
  if (!node.repo && !node.file) return ''
  if (!node.repo) return node.file ?? ''
  if (!node.file) return cleanRepoName(node.repo)
  return `${cleanRepoName(node.repo)}:${node.file}`
}

function formatNodeLabel(node: NonNullable<ImpactResponse['report']>['nodes'][number]) {
  switch (node.type) {
    case 'function': {
      const location = shortFile(node)
      if (!location) return node.name
      if (node.line && node.line > 0) {
        return `${node.name} (${location}:${node.line})`
      }
      return `${node.name} (${location})`
    }
    case 'http_call':
      return `HTTP ${node.method ?? ''} ${node.path ?? ''}`.trim()
    case 'sqs':
      return `SQS ${node.queue || node.name}`.trim()
    default:
      return node.name || ''
  }
}

function buildGraph(report: ImpactResponse['report']) {
  const nodes = new Map(report.nodes.map((node) => [node.id, node]))
  const edges = new Map<string, string[]>()
  for (const edge of report.edges) {
    const existing = edges.get(edge.source) ?? []
    existing.push(edge.target)
    edges.set(edge.source, existing)
  }
  return { nodes, edges }
}

function rootNodeIDs(roots: string[], nodes: Map<string, NonNullable<ImpactResponse['report']>['nodes'][number]>) {
  const ids: string[] = []
  for (const root of roots) {
    const id = `func:${root}`
    if (nodes.has(id)) {
      ids.push(id)
    }
  }
  return ids
}

function bfsParentsFromEdges(edges: Map<string, string[]>, roots: string[]) {
  const parent = new Map<string, string>()
  const visited = new Set<string>()
  const queue = [...roots]
  for (const root of roots) {
    visited.add(root)
  }
  while (queue.length > 0) {
    const current = queue.shift()!
    for (const next of edges.get(current) ?? []) {
      if (visited.has(next)) continue
      visited.add(next)
      parent.set(next, current)
      queue.push(next)
    }
  }
  return parent
}

function reverseEdges(edges: Map<string, string[]>) {
  const reversed = new Map<string, string[]>()
  for (const [source, targets] of edges.entries()) {
    for (const target of targets) {
      const existing = reversed.get(target) ?? []
      existing.push(source)
      reversed.set(target, existing)
    }
  }
  return reversed
}

function formatPath(pathIDs: string[], nodes: Map<string, NonNullable<ImpactResponse['report']>['nodes'][number]>) {
  const labels = pathIDs
    .map((id) => nodes.get(id))
    .filter((node): node is NonNullable<typeof node> => Boolean(node))
    .map((node) => formatNodeLabel(node))
    .filter(Boolean)
  if (labels.length <= 4) {
    return labels.join(' -> ')
  }
  return [labels[0], '...', labels[labels.length - 2], labels[labels.length - 1]].join(' -> ')
}

function buildPath(target: string, parent: Map<string, string>, nodes: Map<string, NonNullable<ImpactResponse['report']>['nodes'][number]>) {
  if (!nodes.has(target)) return ''
  const pathIDs = [target]
  let current = target
  while (parent.has(current)) {
    current = parent.get(current)!
    pathIDs.push(current)
  }
  if (pathIDs.length < 2) return ''
  pathIDs.reverse()
  return formatPath(pathIDs, nodes)
}

function indexHttpNodes(nodes: Map<string, NonNullable<ImpactResponse['report']>['nodes'][number]>) {
  const index = new Map<string, string[]>()
  for (const [id, node] of nodes.entries()) {
    if (node.type !== 'http_call') continue
    let key = httpKey(node.method, node.path)
    if (!key) {
      key = node.name.replace(/^\[\s*|\s*\]$/g, '').trim()
    }
    if (!key) continue
    const existing = index.get(key) ?? []
    existing.push(id)
    index.set(key, existing)
  }
  return index
}

function indexQueueNodes(nodes: Map<string, NonNullable<ImpactResponse['report']>['nodes'][number]>) {
  const index = new Map<string, string[]>()
  for (const [id, node] of nodes.entries()) {
    if (node.type !== 'sqs') continue
    const key = node.queue || node.name
    if (!key) continue
    const existing = index.get(key) ?? []
    existing.push(id)
    index.set(key, existing)
  }
  return index
}

function indexEntrypointNodes(nodes: Map<string, NonNullable<ImpactResponse['report']>['nodes'][number]>) {
  const index = new Map<string, string[]>()
  for (const [id, node] of nodes.entries()) {
    if (node.type !== 'function' || !node.name) continue
    const key = entrypointNodeKey(node.repo, node.name)
    if (!key) continue
    const existing = index.get(key) ?? []
    existing.push(id)
    index.set(key, existing)
  }
  return index
}

function buildImpactEvidencePaths(resp: ImpactResponse | null): ImpactEvidencePaths {
  if (!resp || !resp.report?.nodes?.length) {
    return { entrypoints: {}, http: {}, queues: {} }
  }
  const graph = buildGraph(resp.report)
  const roots = rootNodeIDs(resp.roots, graph.nodes)
  const parentDown = bfsParentsFromEdges(graph.edges, roots)
  const parentUp = bfsParentsFromEdges(reverseEdges(graph.edges), roots)

  const httpPaths: Record<string, string> = {}
  const queuePaths: Record<string, string> = {}
  const entrypointPaths: Record<string, string> = {}

  const httpIndex = indexHttpNodes(graph.nodes)
  for (const call of resp.summary.httpCalls) {
    const key = httpKey(call.method, call.path)
    for (const nodeID of httpIndex.get(key) ?? []) {
      const path = buildPath(nodeID, parentDown, graph.nodes)
      if (path) {
        httpPaths[key] = path
        break
      }
    }
  }

  const queueIndex = indexQueueNodes(graph.nodes)
  for (const queue of resp.summary.queues) {
    for (const nodeID of queueIndex.get(queue.name) ?? []) {
      const path = buildPath(nodeID, parentDown, graph.nodes)
      if (path) {
        queuePaths[queue.name] = path
        break
      }
    }
  }

  const entryIndex = indexEntrypointNodes(graph.nodes)
  for (const entry of resp.summary.entrypoints) {
    const key = entrypointNodeKey(entry.repo, entry.handler)
    for (const nodeID of entryIndex.get(key) ?? []) {
      const path = buildPath(nodeID, parentUp, graph.nodes)
      if (path) {
        entrypointPaths[key] = path
        break
      }
    }
  }

  return { entrypoints: entrypointPaths, http: httpPaths, queues: queuePaths }
}

async function runImpact() {
  impactAbort?.abort()
  impactAbort = null
  loading.value = false
  error.value = ''
  result.value = null
  rootIntegrations.value = {}
  const functions = parseFunctions()
  const functionIds = selectedFunctionTargets.value.map((target) => target.id)
  if (functions.length === 0 && functionIds.length === 0 && ranges.value.length === 0) {
    error.value = 'Provide at least one function name, exact function target, or file range.'
    return
  }
  loading.value = true
  const controller = new AbortController()
  impactAbort = controller
  try {
    const response = await api.impact({
      functions,
      functionIds,
      ranges: ranges.value,
      depth: depth.value,
      noTests: noTests.value,
      resolve: resolveCalls.value,
      maxNodes: maxNodes.value,
      includeTrace: false,
    }, controller.signal)
    if (controller.signal.aborted || impactAbort !== controller) return
    result.value = response
    await loadRootIntegrations(response, controller)
  } catch (err) {
    if (controller.signal.aborted || impactAbort !== controller) return
    if (err instanceof ApiError) {
      error.value = err.message
    } else {
      error.value = 'Failed to build impact report.'
    }
  } finally {
    if (impactAbort === controller) loading.value = false
  }
}

async function loadRootIntegrations(response: ImpactResponse, controller: AbortController): Promise<void> {
  const nodes = new Map(response.report.nodes.map((node) => [node.id, node]))
  const callerIds = Array.from(new Set(
    rootNodeIDs(response.roots, nodes)
      .map((id) => nodes.get(id))
      .filter((node): node is NonNullable<typeof node> => Boolean(node && node.type === 'function' && node.repo && node.file && node.name))
      .map((node) => `${node.repo}:${node.file}:${node.name}`)
  ))
  const loaded = await Promise.all(
    callerIds.map(async (callerId) => {
      try {
        const integrationResponse = await api.functionIntegrations(callerId, controller.signal)
        return [callerId, integrationResponse.integrations] as const
      } catch {
        return [callerId, [] as FunctionIntegration[]] as const
      }
    })
  )
  if (!controller.signal.aborted && impactAbort === controller) {
    rootIntegrations.value = Object.fromEntries(loaded)
  }
}

function cancelSearch() {
  if (searchTimer !== null) window.clearTimeout(searchTimer)
  searchTimer = null
  searchAbort?.abort()
  searchAbort = null
}

watch(workspaceId, () => {
  cancelSearch()
  impactAbort?.abort()
  impactAbort = null
  loading.value = false
  result.value = null
  rootIntegrations.value = {}
  error.value = ''
  searchError.value = ''
  selectedFunctionTargets.value = []
  ranges.value = []
  diffMessage.value = ''
  resetSearchResults()
  if (searchQuery.value.trim()) queueSearch()
}, { flush: 'sync' })

onUnmounted(() => {
  cancelSearch()
  impactAbort?.abort()
  impactAbort = null
})

onMounted(() => {
  addFunctionsFromQuery(route.query.fn)
})

watch(
  () => route.query.fn,
  (value) => {
    addFunctionsFromQuery(value)
  }
)
</script>
