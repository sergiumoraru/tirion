<template>
  <div class="mx-auto p-4 w-full space-y-6">
    <div>
      <h1 class="text-lg font-bold uppercase tracking-wide">Admin Health</h1>
      <p class="mt-2 text-sm text-n-500">
        Index freshness, graph quality warnings, and repo-level parse coverage for operators.
      </p>
    </div>

    <div v-if="loading" class="py-12 text-center">
      <div class="animate-spin w-8 h-8 border-2 border-accent border-t-transparent mx-auto"></div>
    </div>

    <div
      v-else-if="error"
      class="border-2 border-red-500 bg-red-50 dark:bg-red-950 rounded-none p-4 text-red-600 dark:text-red-400"
    >
      {{ error }}
    </div>

    <template v-else-if="result">
      <section class="border-2 border-black dark:border-n-600 bg-white dark:bg-n-900 rounded-none">
        <header class="border-b-2 border-black dark:border-n-600 px-4 py-3 bg-gray-50 dark:bg-n-800">
          <div class="flex flex-wrap items-start justify-between gap-4">
            <div>
              <h2 class="text-sm font-bold uppercase tracking-wide">Workspace Operations</h2>
              <p class="mt-1 text-xs text-n-500">
                Create isolated code contexts, set repo refs, checkout worktrees, and index workspace snapshots.
              </p>
            </div>
            <div class="text-xs uppercase tracking-wide text-n-500">
              Active:
              <span class="font-semibold text-n-800 dark:text-n-200">{{ activeWorkspaceId }}</span>
            </div>
          </div>
        </header>
        <div class="p-4 grid gap-4 xl:grid-cols-[0.9fr_1.1fr]">
          <div class="space-y-4">
            <div class="border border-black/10 dark:border-n-700 p-3 space-y-3">
              <h3 class="text-xs font-semibold uppercase tracking-wide text-n-700 dark:text-n-200">Create workspace</h3>
              <div class="grid gap-3 md:grid-cols-[1fr_1fr_auto] md:items-end">
                <div>
                  <label class="block text-[11px] uppercase tracking-wide text-n-500 mb-1">Name</label>
                  <input
                  v-model.trim="workspaceCreateSlug"
                  type="text"
                  placeholder="release-investigation"
                  class="h-10 w-full px-3 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-sm"
                />
                </div>
                <div>
                  <label class="block text-[11px] uppercase tracking-wide text-n-500 mb-1">Clone from</label>
                <select
                  v-model="workspaceCreateFrom"
                  class="h-10 w-full px-3 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-sm"
                >
                    <option value="">none</option>
                    <option v-for="workspace in workspaces" :key="`clone:${workspace.slug}`" :value="workspace.slug">
                      {{ workspace.slug }}
                    </option>
                  </select>
                </div>
                <button
                  class="h-10 px-3 bg-accent hover:bg-accent/80 rounded-none text-xs font-semibold uppercase tracking-wide text-white disabled:opacity-50"
                  :disabled="workspaceBusy || !workspaceCreateSlug"
                  @click="createWorkspace"
                >
                  Create
                </button>
              </div>
            </div>

            <div class="border border-black/10 dark:border-n-700 p-3 space-y-3">
              <div class="flex items-baseline justify-between gap-3 flex-wrap">
                <h3 class="text-xs font-semibold uppercase tracking-wide text-n-700 dark:text-n-200">Single repo</h3>
                <span v-if="isDefaultMainWorkspace" class="text-[11px] text-n-500">
                  Disabled on default-main (shared baseline). Switch to a non-default workspace.
                </span>
              </div>
              <div class="grid gap-3 md:grid-cols-2">
                <div>
                  <label class="block text-[11px] uppercase tracking-wide text-n-500 mb-1">Repo</label>
                  <input
                    v-model.trim="workspaceRepoName"
                    type="text"
                    placeholder="resource-api"
                    class="h-10 w-full px-3 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-sm"
                  />
                </div>
                <div>
                  <label class="block text-[11px] uppercase tracking-wide text-n-500 mb-1">Target ref</label>
                  <input
                    v-model.trim="workspaceRepoRef"
                    type="text"
                    placeholder="Branch, tag, or commit"
                    class="h-10 w-full px-3 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-sm"
                  />
                </div>
              </div>
              <div class="flex flex-wrap gap-2">
                <button
                  class="h-10 px-3 border-2 border-black dark:border-n-600 rounded-none text-xs font-semibold uppercase tracking-wide hover:bg-gray-50 dark:hover:bg-n-800 disabled:opacity-50 disabled:cursor-not-allowed"
                  :disabled="workspaceBusy || isDefaultMainWorkspace || !workspaceRepoName || !workspaceRepoRef"
                  @click="setWorkspaceRef"
                >
                  Set Ref
                </button>
                <button
                  class="h-10 px-3 border-2 border-black dark:border-n-600 rounded-none text-xs font-semibold uppercase tracking-wide hover:bg-gray-50 dark:hover:bg-n-800 disabled:opacity-50 disabled:cursor-not-allowed"
                  :disabled="workspaceBusy || isDefaultMainWorkspace || !workspaceRepoName"
                  @click="fetchWorkspaceRepo"
                >
                  Fetch Refs
                </button>
                <button
                  class="h-10 px-3 border-2 border-black dark:border-n-600 rounded-none text-xs font-semibold uppercase tracking-wide hover:bg-gray-50 dark:hover:bg-n-800 disabled:opacity-50 disabled:cursor-not-allowed"
                  :disabled="workspaceBusy || isDefaultMainWorkspace || !workspaceRepoName"
                  @click="checkoutWorkspaceRepo"
                >
                  Checkout
                </button>
                <button
                  class="h-10 px-3 bg-accent hover:bg-accent/80 rounded-none text-xs font-semibold uppercase tracking-wide text-white disabled:opacity-50 disabled:cursor-not-allowed"
                  :disabled="workspaceBusy || !workspaceRepoName"
                  @click="indexWorkspaceRepo"
                >
                  Index Repo
                </button>
              </div>
            </div>

            <div class="border border-black/10 dark:border-n-700 p-3 space-y-3">
              <div class="flex items-baseline justify-between gap-3 flex-wrap">
                <h3 class="text-xs font-semibold uppercase tracking-wide text-n-700 dark:text-n-200">Bulk branch checkout</h3>
                <span v-if="isDefaultMainWorkspace" class="text-[11px] text-n-500">
                  Disabled on default-main. Select or create a release workspace.
                </span>
              </div>
              <div>
                <label class="block text-[11px] uppercase tracking-wide text-n-500 mb-1">Branch / ref</label>
                <div class="grid gap-3 md:grid-cols-[1fr_auto] md:items-start">
                  <input
                    v-model.trim="workspaceBulkRef"
                    type="text"
                    placeholder="release/next"
                    class="h-10 w-full px-3 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-sm"
                  />
                  <div class="flex flex-wrap gap-2">
                    <button
                      class="h-10 px-3 bg-accent hover:bg-accent/80 rounded-none text-xs font-semibold uppercase tracking-wide text-white disabled:opacity-50 disabled:cursor-not-allowed"
                      :disabled="workspaceBusy || !workspaceBulkRef || isDefaultMainWorkspace"
                      @click="bulkCheckoutWorkspace"
                    >
                      Set Branch Refs
                    </button>
                    <button
                      class="h-10 px-3 border-2 border-black dark:border-n-600 rounded-none text-xs font-semibold uppercase tracking-wide hover:bg-gray-50 dark:hover:bg-n-800 disabled:opacity-50 disabled:cursor-not-allowed"
                      :disabled="workspaceBusy || isDefaultMainWorkspace"
                      @click="bulkCheckoutMainline"
                    >
                      Checkout Mainline
                    </button>
                  </div>
                </div>
                <p class="mt-1 text-[11px] text-n-500">
                  Fetches every repo and records this ref only where it exists. Physical worktrees are created only when selected repos are parsed.
                </p>
              </div>
              <div v-if="workspaceBulkResult" class="text-xs">
                <div class="font-semibold uppercase tracking-wide">
                  {{ workspaceBulkResult.ref }}:
                  {{ workspaceBulkResult.configured }} configured,
                  {{ workspaceBulkResult.checkedOut }} checked out,
                  {{ workspaceBulkAlreadyCurrentCount }} already current,
                  {{ workspaceBulkResult.skipped }} skipped,
                  {{ workspaceBulkResult.failed }} failed
                </div>
                <div v-if="workspaceBulkParseCandidates.length" class="mt-2 flex flex-wrap items-center gap-2">
                  <button
                    class="px-2 py-1 border border-black dark:border-n-600 rounded-none text-[11px] font-semibold uppercase tracking-wide hover:bg-gray-50 dark:hover:bg-n-800 disabled:opacity-50"
                    :disabled="workspaceBusy"
                    @click="selectAllBulkParseCandidates"
                  >
                    Select All
                  </button>
                  <button
                    class="px-2 py-1 border border-black dark:border-n-600 rounded-none text-[11px] font-semibold uppercase tracking-wide hover:bg-gray-50 dark:hover:bg-n-800 disabled:opacity-50"
                    :disabled="workspaceBusy"
                    @click="clearBulkParseSelection"
                  >
                    Clear
                  </button>
                  <button
                    class="px-2 py-1 bg-accent hover:bg-accent/80 rounded-none text-[11px] font-semibold uppercase tracking-wide text-white disabled:opacity-50"
                    :disabled="workspaceBusy || !workspaceBulkSelectedRepos.length"
                    @click="indexSelectedBulkRepos"
                  >
                    Parse Selected
                  </button>
                  <span class="uppercase tracking-wide text-n-500">
                    {{ workspaceBulkSelectedRepos.length }} selected
                  </span>
                  <span v-if="workspaceBulkIndexJob" class="uppercase tracking-wide text-n-500">
                    {{ workspaceBulkIndexJob.completed }}/{{ workspaceBulkIndexJob.total }} parsed
                    <span v-if="workspaceBulkIndexJob.failed"> · {{ workspaceBulkIndexJob.failed }} failed</span>
                  </span>
                </div>
                <details class="mt-2" open>
                  <summary class="cursor-pointer uppercase tracking-wide text-n-500">
                    Branch ref summary
                    <span v-if="workspaceBulkHiddenSkippedCount">({{ workspaceBulkHiddenSkippedCount }} unchanged/not-found repos hidden)</span>
                  </summary>
                  <div class="mt-2 max-h-40 overflow-y-auto space-y-1">
                    <label
                      v-for="item in workspaceBulkVisibleResults"
                      :key="`${item.repo}:${item.status}`"
                      class="flex items-start gap-2 border border-black/10 dark:border-n-700 px-2 py-1 font-mono"
                    >
                      <input
                        v-if="isBulkParseCandidate(item)"
                        v-model="workspaceBulkSelectedRepos"
                        type="checkbox"
                        :value="item.repo"
                        :disabled="workspaceBusy"
                        class="mt-0.5 rounded-none"
                      />
                      <span v-else class="mt-0.5 inline-block h-3 w-3"></span>
                      <span>
                        <span class="uppercase">{{ item.status }}</span>
                        · {{ item.repo }}
                        <span v-if="item.ref"> · {{ item.ref }}</span>
                        <span v-if="item.sha"> @ {{ formatShortSHA(item.sha) }}</span>
                        <span v-if="item.warning" class="text-amber-700 dark:text-amber-300"> · {{ item.warning }}</span>
                        <span v-if="item.error" class="text-n-500"> · {{ item.error }}</span>
                      </span>
                    </label>
                    <div
                      v-if="!workspaceBulkVisibleResults.length"
                      class="border border-black/10 dark:border-n-700 px-2 py-2 text-n-500"
                    >
                      No repos were configured or failed. {{ workspaceBulkHiddenSkippedCount }} repos were unchanged or did not have the requested ref.
                    </div>
                  </div>
                </details>
              </div>
            </div>
            <div v-if="workspaceAction || workspaceBulkIndexJob || workspaceError" class="border border-black/10 dark:border-n-700 p-3 flex flex-wrap gap-3">
              <!-- Full workspace indexing can parse every repo in the workspace and take
                   a long time on VM-sized estates. Keep the normal UI on targeted
                   Index Repo / Parse Selected flows unless an explicit operator-only
                   confirmation flow is added. Changing the server default workspace
                   is also intentionally absent from the regular UI; use the top
                   workspace selector for normal scoped requests. -->
              <span v-if="workspaceAction" class="text-xs uppercase tracking-wide text-amber-700 dark:text-amber-300">
                {{ workspaceAction }} running
              </span>
              <span v-if="workspaceBulkIndexJob" class="text-xs uppercase tracking-wide text-amber-700 dark:text-amber-300">
                parse selected repos {{ workspaceBulkIndexJob.status }}
                <span v-if="workspaceBulkIndexJob.status === 'running' && workspaceBulkIndexJob.currentRepo">
                  · {{ workspaceBulkIndexJob.currentRepo }}
                </span>
                · {{ workspaceBulkIndexJob.completed }}/{{ workspaceBulkIndexJob.total }}
                <span v-if="workspaceBulkIndexJob.failed"> · {{ workspaceBulkIndexJob.failed }} failed</span>
              </span>
              <span v-if="workspaceError" class="text-xs text-red-600 dark:text-red-300">
                {{ workspaceError }}
              </span>
            </div>
          </div>
          <div class="relative min-h-[20rem]">
            <div class="absolute inset-0 border border-black/10 dark:border-n-700 flex flex-col">
            <div class="px-3 py-2 border-b border-black/10 dark:border-n-700 text-xs uppercase tracking-wide text-n-500 flex items-center justify-between gap-3">
              <span>Active workspace repos</span>
              <span v-if="activeWorkspaceRepos.length" class="text-n-400">{{ activeWorkspaceRepos.length }}</span>
            </div>
            <div class="flex-1 min-h-0 overflow-y-auto divide-y divide-gray-200 dark:divide-n-700 [scrollbar-gutter:stable]">
              <button
                v-for="workspaceRepo in activeWorkspaceRepos"
                :key="workspaceRepo.repoName"
                class="w-full pl-3 pr-6 py-2 text-left hover:bg-gray-50 dark:hover:bg-n-800"
                @click="selectWorkspaceRepo(workspaceRepo.repoName, workspaceRepo.targetRef || workspaceRepo.resolvedBranch)"
              >
                <div class="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3">
                  <span class="min-w-0 font-semibold truncate">{{ workspaceRepo.repoName }}</span>
                  <span
                    class="shrink-0 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide border rounded-none"
                    :class="workspaceRepoStatusClass(workspaceRepo.indexStatus)"
                    :title="workspaceRepoStatusTitle(workspaceRepo.indexStatus)"
                  >
                    {{ workspaceRepoStatusLabel(workspaceRepo.indexStatus) }}
                  </span>
                </div>
                <div class="mt-1 text-xs text-n-500 font-mono">
                  {{ workspaceRepo.targetRef || 'no ref' }}
                  <span v-if="workspaceRepo.resolvedSha"> @ {{ formatShortSHA(workspaceRepo.resolvedSha) }}</span>
                </div>
              </button>
              <div v-if="!activeWorkspaceRepos.length" class="px-3 py-6 text-center text-xs uppercase tracking-wide text-n-500">
                No workspace repo refs configured.
              </div>
            </div>
            </div>
          </div>
        </div>
      </section>

      <div
        v-if="result.warnings.length"
        class="bg-amber-50 dark:bg-amber-900/20 border-2 border-amber-500 dark:border-amber-700 rounded-none p-4 text-amber-700 dark:text-amber-200 text-sm"
      >
        <div class="font-semibold uppercase tracking-wide mb-2">Operator Summary</div>
        <div class="text-sm">{{ operatorSummary }}</div>
        <div class="mt-3 flex flex-wrap gap-2">
          <span
            v-if="result.overview.staleRepos > 0"
            class="px-2 py-1 text-xs font-semibold uppercase tracking-wide border border-red-500 bg-red-100 dark:border-red-700 dark:bg-red-900/30"
          >
            {{ result.overview.staleRepos }} stale repos
          </span>
          <span
            v-if="result.audit.warnings.length > 0"
            class="px-2 py-1 text-xs font-semibold uppercase tracking-wide border border-amber-500 bg-amber-100 dark:border-amber-700 dark:bg-amber-900/30"
          >
            {{ result.audit.warnings.length }} graph-quality warning<span v-if="result.audit.warnings.length !== 1">s</span>
          </span>
          <span
            v-if="result.refresh.status === 'failed' && !result.refresh.superseded"
            class="px-2 py-1 text-xs font-semibold uppercase tracking-wide border border-red-500 bg-red-100 dark:border-red-700 dark:bg-red-900/30"
          >
            Refresh failed
          </span>
        </div>
      </div>

      <section class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
        <article class="border-2 border-black dark:border-n-600 bg-white dark:bg-n-900 rounded-none p-4">
          <div class="text-xs uppercase tracking-widest text-n-500">Estate</div>
          <div class="mt-3 flex items-start justify-between gap-4">
            <div>
              <div class="text-xl font-bold uppercase" :class="headlineStatusClass(estateStatus)">
                {{ estateStatus }}
              </div>
              <div class="text-sm text-n-500">
                Generated: {{ formatDateTime(result.generatedAt) }}
              </div>
              <div class="text-sm text-n-500">
                Version: <span class="font-semibold text-n-700 dark:text-n-200">{{ result.version }}</span>
              </div>
            </div>
            <div class="text-right text-xs text-n-500 uppercase tracking-wide">
              <div>Queue match</div>
              <div class="mt-1 font-semibold text-n-700 dark:text-n-200">{{ formatPercent(result.audit.metrics.queueMatchRate) }}</div>
            </div>
          </div>
          <div class="mt-3 grid grid-cols-2 gap-x-4 gap-y-1 text-sm">
            <div>Repos</div>
            <div class="text-right font-semibold">{{ formatNumber(result.overview.repos) }}</div>
            <div>Files</div>
            <div class="text-right font-semibold">{{ formatNumber(result.overview.files) }}</div>
            <div>Functions</div>
            <div class="text-right font-semibold">{{ formatNumber(result.overview.functions) }}</div>
            <div>Endpoints</div>
            <div class="text-right font-semibold">{{ formatNumber(result.overview.endpoints) }}</div>
            <div>Duplicate endpoints</div>
            <div class="text-right font-semibold">{{ result.audit.metrics.duplicateEndpointGroups }}</div>
            <div>Unresolved HTTP</div>
            <div class="text-right font-semibold">{{ result.audit.metrics.unresolvedHTTPCalls }}</div>
            <div>Data coverage</div>
            <div class="text-right font-semibold">{{ formatPercent(result.audit.metrics.dataCoverageRate) }}</div>
          </div>
        </article>

        <article class="border-2 border-black dark:border-n-600 bg-white dark:bg-n-900 rounded-none p-4">
          <div class="text-xs uppercase tracking-widest text-n-500">Indexing</div>
          <div class="mt-3 flex items-start justify-between gap-4">
            <div>
              <div class="text-lg font-bold uppercase">
                {{ formatDateTime(result.overview.latestIndexedAt) }}
              </div>
              <div class="mt-1 text-sm text-n-500">
                Completed {{ formatRelative(result.overview.latestIndexedAt) }}
              </div>
            </div>
            <div class="text-right">
              <div class="text-xs uppercase tracking-wide text-n-500">Refresh</div>
              <div class="mt-1 text-sm font-bold uppercase" :class="refreshStatusClass(result.refresh)">
                {{ refreshStatusLabel(result.refresh) }}
              </div>
              <div v-if="result.refresh.workspace" class="mt-1 text-xs font-mono text-n-500">
                {{ result.refresh.workspace }}
              </div>
            </div>
          </div>
          <div class="mt-3 grid grid-cols-2 gap-x-4 gap-y-1 text-sm">
            <div>Last refresh</div>
            <div class="text-xs text-right text-n-500">{{ formatDateTime(result.refresh.lastSuccessAt || result.refresh.updatedAt) }}</div>
            <div>Latest index</div>
            <div class="text-xs text-right text-n-500">{{ formatDateTime(result.overview.latestIndexedAt) }}</div>
            <div>Oldest index</div>
            <div class="text-xs text-right text-n-500">{{ formatDateTime(result.overview.oldestIndexedAt) }}</div>
            <div>Refresh attempt</div>
            <div class="text-right text-n-500">
              {{ result.refresh.attempt || 0 }}<span v-if="result.refresh.maxAttempts"> / {{ result.refresh.maxAttempts }}</span>
            </div>
            <div>Refresh updated</div>
            <div class="text-right text-n-500">{{ formatDateTime(result.refresh.updatedAt) }}</div>
            <div>Last success</div>
            <div class="text-right text-n-500">{{ formatDateTime(result.refresh.lastSuccessAt) }}</div>
          </div>
          <div v-if="result.refresh.status !== 'unknown'" class="mt-3 text-sm text-n-500">
            Attempt: {{ result.refresh.attempt || 0 }}<span v-if="result.refresh.maxAttempts"> / {{ result.refresh.maxAttempts }}</span>
          </div>
          <div
            v-if="result.refresh.status === 'failed' && result.refresh.errorSummary"
            class="mt-3 text-sm break-words"
            :class="result.refresh.superseded ? 'text-amber-700 dark:text-amber-300' : 'text-red-600 dark:text-red-300'"
          >
            {{ result.refresh.errorSummary }}
          </div>
          <details
            v-if="result.refresh.status === 'failed' && result.refresh.error && result.refresh.error !== result.refresh.errorSummary"
            class="mt-3 border border-black dark:border-n-600 p-2 text-xs text-n-500"
          >
            <summary class="cursor-pointer uppercase tracking-wide">Raw details</summary>
            <pre class="mt-2 whitespace-pre-wrap break-words max-h-40 overflow-y-auto">{{ result.refresh.error }}</pre>
          </details>
          <div
            v-if="result.refresh.status === 'failed' && result.refresh.superseded"
            class="mt-3 text-xs text-n-500"
          >
            A newer successful index superseded this failure.
          </div>
        </article>

        <article class="border-2 border-black dark:border-n-600 bg-white dark:bg-n-900 rounded-none p-4">
          <div class="text-xs uppercase tracking-widest text-n-500">Freshness</div>
          <div class="mt-3 flex items-start justify-between gap-4">
            <div>
              <div class="text-xl font-bold uppercase" :class="headlineStatusClass(freshnessHealthStatus)">
                {{ freshnessHealthLabel }}
              </div>
              <div class="text-sm text-n-500">
                Recent share: <span class="font-semibold text-n-700 dark:text-n-200">{{ freshnessRecentShareLabel }}</span>
              </div>
            </div>
            <div class="text-right text-xs text-n-500 uppercase tracking-wide">
              <div>Oldest repo</div>
              <div class="mt-1 font-semibold text-n-700 dark:text-n-200">{{ oldestRepoAgeLabel }}</div>
            </div>
          </div>
          <div class="mt-3 grid grid-cols-3 gap-2 text-xs uppercase tracking-wide">
            <div class="border px-2 py-2" :class="freshnessClass('recent')">
              <div>Recent</div>
              <div class="mt-1 text-base font-bold normal-case tracking-normal">{{ result.overview.recentRepos }}</div>
            </div>
            <div class="border px-2 py-2" :class="freshnessClass('aging')">
              <div>Aging</div>
              <div class="mt-1 text-base font-bold normal-case tracking-normal">{{ result.overview.agingRepos }}</div>
            </div>
            <div class="border px-2 py-2" :class="freshnessClass('stale')">
              <div>Stale</div>
              <div class="mt-1 text-base font-bold normal-case tracking-normal">{{ result.overview.staleRepos }}</div>
            </div>
          </div>
          <div class="mt-3 grid grid-cols-2 gap-y-2 text-sm">
            <div>Recent</div>
            <div class="text-right font-semibold text-accent">{{ result.overview.recentRepos }}</div>
            <div>Aging</div>
            <div class="text-right font-semibold text-amber-600 dark:text-amber-300">{{ result.overview.agingRepos }}</div>
            <div>Stale</div>
            <div class="text-right font-semibold text-red-600 dark:text-red-300">{{ result.overview.staleRepos }}</div>
            <div>Attention queue</div>
            <div class="text-right font-semibold">{{ attentionRepos.length }}</div>
          </div>
        </article>

      </section>

      <section v-if="result.audit.warnings.length" class="grid gap-4">
        <article class="border-2 border-black dark:border-n-600 bg-white dark:bg-n-900 rounded-none">
          <header class="border-b-2 border-black dark:border-n-600 px-4 py-3 bg-gray-50 dark:bg-n-800">
            <h2 class="text-sm font-bold uppercase tracking-wide">Audit Warnings</h2>
          </header>
          <div class="p-4">
            <ul v-if="result.audit.warnings.length" class="space-y-2 text-sm">
              <li
                v-for="warning in result.audit.warnings"
                :key="warning"
                class="border border-amber-300 dark:border-amber-700 bg-amber-50 dark:bg-amber-900/20 px-3 py-2"
              >
                {{ warning }}
              </li>
            </ul>
          </div>
        </article>
      </section>

      <section
        v-if="showUnresolvedHTTPSection"
        class="grid gap-4 xl:grid-cols-[0.9fr_1.1fr]"
      >
        <article class="border-2 border-black dark:border-n-600 bg-white dark:bg-n-900 rounded-none overflow-hidden">
          <header class="border-b-2 border-black dark:border-n-600 px-4 py-3 bg-gray-50 dark:bg-n-800">
            <h2 class="text-sm font-bold uppercase tracking-wide">Unresolved HTTP By Repo</h2>
            <p class="mt-1 text-xs text-n-500">Top caller repos contributing unresolved internal HTTP routes.</p>
          </header>
          <div
            v-if="result.audit.unresolvedHTTP.topRepos.length"
            class="divide-y divide-gray-200 dark:divide-n-700"
          >
            <div
              v-for="repo in result.audit.unresolvedHTTP.topRepos"
              :key="repo.repo"
              class="px-4 py-3 grid grid-cols-[minmax(0,1fr)_auto_auto] gap-4 items-start"
            >
              <div class="min-w-0">
                <div class="font-semibold truncate">{{ repo.repo }}</div>
                <div class="mt-1 text-xs text-n-500">
                  {{ repo.pathUnmatched }} path mismatches · {{ repo.methodMismatched }} method mismatches
                </div>
              </div>
              <div class="text-xs text-n-500 uppercase tracking-wide">Total</div>
              <div class="font-semibold">{{ repo.count }}</div>
            </div>
          </div>
          <div v-else class="p-4 text-sm text-n-500">
            {{ unresolvedHTTPFallback }}
          </div>
        </article>

        <article class="border-2 border-black dark:border-n-600 bg-white dark:bg-n-900 rounded-none overflow-hidden">
          <header class="border-b-2 border-black dark:border-n-600 px-4 py-3 bg-gray-50 dark:bg-n-800">
            <h2 class="text-sm font-bold uppercase tracking-wide">Unresolved HTTP Samples</h2>
            <p class="mt-1 text-xs text-n-500">Representative unresolved routes with the exact mismatch reason.</p>
          </header>
          <div
            v-if="result.audit.unresolvedHTTP.samples.length"
            class="divide-y divide-gray-200 dark:divide-n-700 max-h-[32rem] overflow-y-auto"
          >
            <div
              v-for="sample in result.audit.unresolvedHTTP.samples"
              :key="`${sample.repo}:${sample.callerId}:${sample.method}:${sample.path}:${sample.lineNumber}`"
              class="px-4 py-3"
            >
              <div class="flex flex-wrap items-center gap-2">
                <span class="font-semibold">{{ sample.repo }}</span>
                <span class="px-2 py-0.5 text-xs font-semibold uppercase tracking-wide border" :class="reasonClass(sample.reason)">
                  {{ formatReason(sample.reason) }}
                </span>
                <span v-if="sample.clientType" class="text-xs text-n-500 uppercase tracking-wide">{{ sample.clientType }}</span>
              </div>
              <div class="mt-2 font-mono text-sm break-all">{{ sample.method }} {{ sample.path }}</div>
              <div class="mt-1 text-xs text-n-500 break-all">{{ sample.callerId }}<span v-if="sample.lineNumber > 0"> :{{ sample.lineNumber }}</span></div>
              <div v-if="sample.availableMethods.length" class="mt-2 text-xs text-amber-700 dark:text-amber-300">
                Indexed methods for this path: {{ sample.availableMethods.join(', ') }}
              </div>
            </div>
          </div>
          <div v-else class="p-4 text-sm text-n-500">
            {{ unresolvedHTTPFallback }}
          </div>
        </article>
      </section>

      <section class="border-2 border-black dark:border-n-600 bg-white dark:bg-n-900 rounded-none overflow-visible">
        <header class="border-b-2 border-black dark:border-n-600 px-4 py-3 bg-gray-50 dark:bg-n-800 space-y-4">
          <div>
            <h2 class="text-sm font-bold uppercase tracking-wide">Repo Workspace</h2>
            <p class="mt-1 text-xs text-n-500">Shared estate branch state, indexed revision drift, and per-repo parse controls.</p>
            <p class="mt-2 text-xs text-amber-700 dark:text-amber-300">
              These actions mutate the shared indexed estate. Use them deliberately.
            </p>
          </div>
          <div
            v-if="displayOperation"
            class="border border-amber-400 dark:border-amber-700 bg-amber-50 dark:bg-amber-900/20 px-3 py-2 text-xs uppercase tracking-wide"
          >
            <span class="inline-flex items-center gap-2">
              <span class="inline-block h-3 w-3 animate-spin rounded-full border-2 border-amber-600 border-t-transparent dark:border-amber-300 dark:border-t-transparent"></span>
              <span>Active operation: {{ displayOperation.action }} on {{ displayOperation.repoName }}</span>
            </span>
            <span class="text-n-500 normal-case tracking-normal">started {{ formatRelative(displayOperation.startedAt) }}</span>
          </div>
          <div
            v-if="repoActionError"
            class="border border-red-500 dark:border-red-700 bg-red-50 dark:bg-red-900/20 px-3 py-2 text-sm text-red-700 dark:text-red-300"
          >
            {{ repoActionError }}
          </div>
          <div class="flex flex-wrap items-end gap-3">
            <div class="min-w-[220px]">
              <label class="block text-xs uppercase tracking-wide text-n-500 mb-1">Search repos</label>
              <input
                v-model.trim="search"
                type="text"
                placeholder="Repo name, path, language..."
                class="w-full px-3 py-2 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-sm"
              />
            </div>
            <div>
              <label class="block text-xs uppercase tracking-wide text-n-500 mb-1">Freshness</label>
              <select
                v-model="freshnessFilter"
                class="px-3 py-2 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-sm"
              >
                <option value="attention">Needs attention</option>
                <option value="all">All repos</option>
                <option value="stale">Stale only</option>
                <option value="aging">Aging only</option>
                <option value="recent">Recent only</option>
              </select>
            </div>
            <div class="text-xs text-n-500 uppercase tracking-wide">
              Showing {{ visibleRepos.length }} of {{ filteredRepos.length }} repos
            </div>
          </div>
        </header>

        <div class="overflow-x-auto overflow-y-visible">
          <table class="w-full min-w-[1500px]">
            <thead class="bg-gray-50 dark:bg-n-800 text-xs uppercase tracking-widest text-n-500">
              <tr>
                <th class="px-4 py-3 text-left">Repo</th>
                <th class="px-4 py-3 text-left">Freshness</th>
                <th class="px-4 py-3 text-left">Workspace</th>
                <th class="px-4 py-3 text-left">Indexed</th>
                <th class="px-4 py-3 text-left">Drift</th>
                <th class="px-4 py-3 text-right">Age</th>
                <th class="px-4 py-3 text-right">
                  <span class="inline-flex items-center gap-2">
                    <span>Intel</span>
                    <span
                      class="inline-flex h-4 w-4 items-center justify-center rounded-full border border-current text-[10px] font-bold"
                      title="Indexed counts in this repo: files / functions / endpoints"
                    >
                      i
                    </span>
                  </span>
                </th>
                <th class="px-4 py-3 text-left">Actions</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-200 dark:divide-n-700 text-sm">
              <tr
                v-for="repo in visibleRepos"
                :key="repo.id"
                class="hover:bg-gray-50 dark:hover:bg-n-800/40"
                :class="rowClass(repo.freshnessStatus)"
              >
                <td class="px-4 py-3 align-top">
                  <div class="font-semibold">{{ repo.name }}</div>
                  <div class="mt-1 text-xs text-n-500 truncate" :title="repo.path">{{ compactPath(repo.path) }}</div>
                </td>
                <td class="px-4 py-3 align-top">
                  <span class="px-2 py-1 text-xs font-semibold uppercase tracking-wide border" :class="freshnessClass(repo.freshnessStatus)">
                    {{ repo.freshnessStatus }}
                  </span>
                </td>
                <td class="px-4 py-3 align-top text-n-500 min-w-[150px]">
                  <div class="font-semibold text-n-700 dark:text-n-200">{{ repo.currentBranch || 'unknown' }}</div>
                  <div class="mt-1 text-xs">HEAD {{ formatShortSHA(repo.headSha) }}</div>
                  <div class="mt-1 text-xs">
                    <span :class="repo.dirty ? 'text-red-600 dark:text-red-300' : 'text-emerald-600 dark:text-emerald-300'">
                      {{ repo.dirty ? 'dirty' : 'clean' }}
                    </span>
                    <span class="mx-1">·</span>
                    +{{ repo.aheadCount }}/-{{ repo.behindCount }}
                  </div>
                </td>
                <td class="px-4 py-3 align-top text-n-500 min-w-[150px]">
                  <div class="font-semibold text-n-700 dark:text-n-200">{{ repo.indexedSha ? repo.indexedBranch : 'not indexed' }}</div>
                  <div class="mt-1 text-xs">SHA {{ formatShortSHA(repo.indexedSha) }}</div>
                  <div class="mt-1 text-xs">{{ repo.indexedSha ? formatDateTime(repo.indexedAt || repo.updatedAt) : 'not indexed in this workspace' }}</div>
                </td>
                <td class="px-4 py-3 align-top min-w-[150px]">
                  <span class="inline-block whitespace-nowrap px-2 py-1 text-xs font-semibold uppercase tracking-wide border" :class="driftClass(repo.driftStatus)">
                    {{ driftLabel(repo) }}
                  </span>
                  <div v-if="repo.lastOperation" class="mt-2 text-xs text-n-500">
                    {{ formatOperationLabel(repo.lastOperation, repo.lastOperationStatus) }}
                  </div>
                  <div v-if="repo.lastError" class="mt-1 text-xs text-red-600 dark:text-red-300 break-words">
                    {{ repo.lastError }}
                  </div>
                </td>
                <td class="px-4 py-3 text-right align-top text-n-500">{{ formatAgeHours(repo.ageHours) }}</td>
                <td class="px-4 py-3 text-right align-top whitespace-nowrap">
                  {{ formatNumber(repo.fileCount) }}/{{ formatNumber(repo.functionCount) }}/{{ formatNumber(repo.endpointCount) }}
                </td>
                <td class="px-4 py-3 align-top">
                  <div class="flex flex-col gap-2 min-w-[320px]">
                    <div class="flex items-stretch gap-2">
                      <input
                        v-model.trim="branchInputs[repo.id]"
                        type="text"
                        placeholder="Branch name"
                        class="flex-1 px-3 py-2 bg-white dark:bg-n-900 border-2 border-black dark:border-n-600 rounded-none text-sm"
                        :disabled="isRepoActionBlocked(repo)"
                      />
                      <button
                        class="w-10 flex items-center justify-center border-2 border-black dark:border-n-600 rounded-none text-sm font-bold hover:bg-gray-50 dark:hover:bg-n-800 disabled:opacity-50"
                        :disabled="isRepoActionBlocked(repo)"
                        @click="toggleRepoMenu(repo.id, $event)"
                        :aria-expanded="openRepoMenuId === repo.id ? 'true' : 'false'"
                      >
                        ⋮
                      </button>
                    </div>
                    <div class="grid grid-cols-2 gap-2">
                      <button
                        class="flex-1 px-3 py-2 border-2 border-black dark:border-n-600 rounded-none text-xs font-semibold uppercase tracking-wide hover:bg-gray-50 dark:hover:bg-n-800 disabled:opacity-50"
                        :disabled="isRepoActionBlocked(repo) || !branchInputs[repo.id]"
                        @click="runCheckout(repo.id)"
                      >
                        <span v-if="isRepoActionRunning(repo.id, 'checkout')" class="inline-flex items-center gap-2">
                          <span class="inline-block h-3 w-3 animate-spin rounded-full border-2 border-current border-t-transparent"></span>
                          Running
                        </span>
                        <span v-else>Checkout</span>
                      </button>
                      <button
                        class="flex-1 px-3 py-2 bg-accent hover:bg-accent/80 rounded-none text-xs font-semibold uppercase tracking-wide text-white disabled:opacity-50"
                        :disabled="isRepoActionBlocked(repo)"
                        @click="runParse(repo.id, true)"
                      >
                        <span v-if="isRepoActionRunning(repo.id, 'parse_resolve')" class="inline-flex items-center gap-2">
                          <span class="inline-block h-3 w-3 animate-spin rounded-full border-2 border-current border-t-transparent"></span>
                          Running
                        </span>
                        <span v-else>Parse + Resolve</span>
                      </button>
                    </div>
                  </div>
                </td>
              </tr>
              <tr v-if="!visibleRepos.length">
                <td colspan="8" class="px-4 py-8 text-center text-xs uppercase tracking-widest text-n-500">
                  No repos match the current filters.
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <div
          v-if="visibleRepos.length < filteredRepos.length"
          class="border-t-2 border-black dark:border-n-600 px-4 py-3 bg-gray-50 dark:bg-n-800 flex items-center justify-between gap-4"
        >
          <div class="text-sm text-n-500">
            {{ filteredRepos.length - visibleRepos.length }} more repos hidden to keep the page readable.
          </div>
          <button
            class="px-3 py-2 bg-accent hover:bg-accent/80 rounded-none text-sm font-medium uppercase tracking-wide text-white"
            @click="showMoreRepos"
          >
            Show More
          </button>
        </div>
      </section>
    </template>

    <Teleport to="body">
      <div
        v-if="openRepoMenuId !== null"
        class="fixed inset-0 z-40"
        @click="closeRepoMenu"
      >
        <div
          class="fixed z-50 w-52 border-2 border-black dark:border-n-600 bg-white dark:bg-n-900 shadow-lg"
          :style="repoMenuStyle"
          @click.stop
        >
          <button
            class="w-full px-3 py-2 text-left text-xs font-semibold uppercase tracking-wide hover:bg-gray-50 dark:hover:bg-n-800 disabled:opacity-50"
            :disabled="menuRepo ? isRepoActionBlocked(menuRepo) : true"
            @click="menuRepo && runMenuFetch(menuRepo.id)"
          >
            <span v-if="menuRepo && isRepoActionRunning(menuRepo.id, 'fetch')" class="inline-flex items-center gap-2">
              <span class="inline-block h-3 w-3 animate-spin rounded-full border-2 border-current border-t-transparent"></span>
              Running
            </span>
            <span v-else>Fetch</span>
          </button>
          <button
            class="w-full border-t border-black dark:border-n-600 px-3 py-2 text-left text-xs font-semibold uppercase tracking-wide hover:bg-gray-50 dark:hover:bg-n-800 disabled:opacity-50"
            :disabled="menuRepo ? isRepoActionBlocked(menuRepo) : true"
            @click="menuRepo && runMenuParse(menuRepo.id)"
          >
            <span v-if="menuRepo && isRepoActionRunning(menuRepo.id, 'parse')" class="inline-flex items-center gap-2">
              <span class="inline-block h-3 w-3 animate-spin rounded-full border-2 border-current border-t-transparent"></span>
              Running
            </span>
            <span v-else>Parse Only</span>
          </button>
        </div>
      </div>
    </Teleport>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { api } from '@/api/client'
import type {
  AdminHealthResponse,
  AdminRepoOperationState,
  AdminRepoRow,
  BulkCheckoutWorkspaceRepoResult,
  BulkCheckoutWorkspaceResponse,
  BulkIndexWorkspaceJob,
  WorkspaceSummary,
} from '@/types'
import { clearCachedAdminHealth, getCachedAdminHealth, setCachedAdminHealth } from '@/lib/adminHealthCache'
import { useWorkspace } from '@/composables/useWorkspace'

const { workspaceId: activeWorkspaceId, setWorkspaceId } = useWorkspace()

const loading = ref(false)
const error = ref<string | null>(null)
const repoActionError = ref<string | null>(null)
const workspaceError = ref<string | null>(null)
const result = ref<AdminHealthResponse | null>(null)
const repoRows = ref<AdminRepoRow[]>([])
const workspaces = ref<WorkspaceSummary[]>([])
const activeOperation = ref<AdminRepoOperationState | null>(null)
const branchInputs = ref<Record<number, string>>({})
const pendingRepoId = ref<number | null>(null)
const pendingAction = ref<'fetch' | 'checkout' | 'parse' | 'parse_resolve' | null>(null)
const pendingStartedAt = ref<string | null>(null)
const workspaceAction = ref<string | null>(null)
const workspaceCreateSlug = ref('')
const workspaceCreateFrom = ref('default-main')
const workspaceRepoName = ref('')
const workspaceRepoRef = ref('')
const workspaceBulkRef = ref('')
const workspaceBulkResult = ref<BulkCheckoutWorkspaceResponse | null>(null)
const workspaceBulkIndexJob = ref<BulkIndexWorkspaceJob | null>(null)
const workspaceBulkSelectedRepos = ref<string[]>([])
const openRepoMenuId = ref<number | null>(null)
const repoMenuPosition = ref<{ top: number; left: number }>({ top: 0, left: 0 })
const search = ref('')
const freshnessFilter = ref<'attention' | 'all' | 'stale' | 'aging' | 'recent'>('attention')
const visibleCount = ref(25)
let workspaceBulkIndexPoll: ReturnType<typeof setTimeout> | null = null
let workspaceBulkIndexRequest: AbortController | null = null
let disposed = false

const relativeFormatter = new Intl.RelativeTimeFormat(undefined, { numeric: 'auto' })
const workspaceBulkIndexRunning = computed(() => workspaceBulkIndexJob.value?.status === 'running')
const workspaceBusy = computed(() => workspaceAction.value !== null || workspaceBulkIndexRunning.value)
const activeWorkspace = computed(() => workspaces.value.find((workspace) => workspace.slug === activeWorkspaceId.value) ?? null)
const activeWorkspaceRepos = computed(() => activeWorkspace.value?.repos ?? [])
const activeWorkspaceRepoByName = computed(() => new Map(activeWorkspaceRepos.value.map((repo) => [repo.repoName, repo])))
const isDefaultMainWorkspace = computed(() => activeWorkspaceId.value === 'default-main')
const workspaceBulkParseCandidates = computed(() =>
  (workspaceBulkResult.value?.results ?? []).filter(isBulkParseCandidate)
)
const workspaceBulkVisibleResults = computed(() =>
  (workspaceBulkResult.value?.results ?? []).filter((item) => {
    if (item.status === 'skipped' && item.error === `ref "${item.ref}" not found`) return false
    if (workspaceBulkResult.value?.mode === 'mainline' && item.status === 'already_current') return false
    return true
  })
)
const workspaceBulkHiddenSkippedCount = computed(() =>
  (workspaceBulkResult.value?.results ?? []).length - workspaceBulkVisibleResults.value.length
)
const workspaceBulkAlreadyCurrentCount = computed(() =>
  (workspaceBulkResult.value?.results ?? []).filter((item) => item.status === 'already_current').length
)

const freshnessRank: Record<AdminRepoRow['freshnessStatus'], number> = {
  stale: 3,
  aging: 2,
  recent: 1,
}

const sortedRepos = computed(() => {
  const repos = [...repoRows.value]
  repos.sort((a, b) => {
    const freshnessDelta = freshnessRank[b.freshnessStatus] - freshnessRank[a.freshnessStatus]
    if (freshnessDelta !== 0) return freshnessDelta
    if (a.driftStatus !== b.driftStatus) {
      const rank = { drift: 2, unknown: 1, in_sync: 0 } as const
      return rank[b.driftStatus] - rank[a.driftStatus]
    }
    if (b.ageHours !== a.ageHours) return b.ageHours - a.ageHours
    if (b.endpointCount !== a.endpointCount) return b.endpointCount - a.endpointCount
    return a.name.localeCompare(b.name)
  })
  return repos
})

const filteredRepos = computed(() => {
  const term = search.value.toLowerCase()
  return sortedRepos.value.filter((repo) => {
    if (freshnessFilter.value === 'attention' && repo.freshnessStatus === 'recent') {
      return false
    }
    if (freshnessFilter.value !== 'all' && freshnessFilter.value !== 'attention' && repo.freshnessStatus !== freshnessFilter.value) {
      return false
    }
    if (!term) return true
    return [repo.name, repo.path, repo.primaryLanguage, repo.currentBranch, repo.indexedBranch].some((value) =>
      value.toLowerCase().includes(term)
    )
  })
})

const visibleRepos = computed(() => filteredRepos.value.slice(0, visibleCount.value))
const attentionRepos = computed(() => sortedRepos.value.filter((repo) => repo.freshnessStatus !== 'recent'))
const oldestRepoAgeLabel = computed(() => {
  if (!attentionRepos.value.length) return '—'
  return formatAgeHours(attentionRepos.value[0].ageHours)
})
const freshnessHealthStatus = computed<'ok' | 'warn'>(() => {
  if (!result.value) return 'ok'
  return result.value.overview.agingRepos > 0 || result.value.overview.staleRepos > 0 ? 'warn' : 'ok'
})
const freshnessHealthLabel = computed(() => {
  if (!result.value) return 'steady'
  if (result.value.overview.staleRepos > 0) return 'attention'
  if (result.value.overview.agingRepos > 0) return 'watch'
  return 'steady'
})
const freshnessRecentShareLabel = computed(() => {
  if (!result.value || result.value.overview.repos === 0) return '0%'
  return formatPercent(100 * result.value.overview.recentRepos / result.value.overview.repos)
})
const estateStatus = computed<'ok' | 'warn'>(() => {
  if (!result.value) return 'ok'
  return result.value.overview.status === 'warn' || result.value.audit.status === 'warn' ? 'warn' : 'ok'
})
const operatorSummary = computed(() => {
  if (!result.value) return ''
  const parts: string[] = []
  if (result.value.overview.staleRepos > 0) {
    parts.push(`${result.value.overview.staleRepos} repos are stale and should be reparsed.`)
  } else if (result.value.overview.agingRepos > 0) {
    parts.push(`${result.value.overview.agingRepos} repos are outside the recent window.`)
  }
  if (result.value.refresh.status === 'running') {
    parts.push(`Refresh attempt ${result.value.refresh.attempt || 1}${result.value.refresh.maxAttempts ? `/${result.value.refresh.maxAttempts}` : ''} is running.`)
  } else if (result.value.refresh.status === 'failed' && !result.value.refresh.superseded) {
    parts.push(`Last refresh failed${result.value.refresh.maxAttempts ? ` after ${result.value.refresh.attempt}/${result.value.refresh.maxAttempts} attempts` : ''}.`)
  } else if (result.value.refresh.status === 'failed' && result.value.refresh.superseded) {
    parts.push('A failed refresh was superseded by a newer successful index.')
  }
  if (result.value.audit.warnings.length > 0) {
    parts.push(`${result.value.audit.warnings.length} graph-quality warning requires follow-up.`)
  }
  return parts.join(' ')
})
const unresolvedHTTPCount = computed(() => result.value?.audit.metrics.unresolvedHTTPCalls ?? 0)
const showUnresolvedHTTPSection = computed(() => {
  if (!result.value) return false
  return unresolvedHTTPCount.value > 0
    || result.value.audit.unresolvedHTTP.topRepos.length > 0
    || result.value.audit.unresolvedHTTP.samples.length > 0
})
const unresolvedHTTPFallback = computed(() => {
  const count = unresolvedHTTPCount.value
  if (count <= 0) {
    return 'No unresolved internal HTTP call samples are available in this snapshot.'
  }
  return `${formatNumber(count)} unresolved internal HTTP call${count === 1 ? '' : 's'} were detected in the audit metrics, but detailed samples are not available in this snapshot.`
})
const displayOperation = computed<AdminRepoOperationState | null>(() => {
  if (pendingRepoId.value !== null && pendingAction.value && pendingStartedAt.value) {
    const repo = repoRows.value.find((item) => item.id === pendingRepoId.value)
    if (repo) {
      return {
        repoId: repo.id,
        repoName: repo.name,
        action: pendingAction.value,
        startedAt: pendingStartedAt.value,
      }
    }
  }
  return activeOperation.value
})
const menuRepo = computed(() => repoRows.value.find((repo) => repo.id === openRepoMenuId.value) ?? null)
const repoMenuStyle = computed(() => ({
  top: `${repoMenuPosition.value.top}px`,
  left: `${repoMenuPosition.value.left}px`,
}))

async function loadAdminHealth() {
  const cached = getCachedAdminHealth()
  if (cached) {
    result.value = cached
    error.value = null
    return
  }

  const response = await api.adminHealth()
  result.value = response
  setCachedAdminHealth(response)
}

async function loadAdminRepos() {
  const workspace = activeWorkspaceId.value
  const response = await api.adminRepos()
  if (disposed || workspace !== activeWorkspaceId.value) return
  repoRows.value = response.repos
  activeOperation.value = response.activeOperation ?? null
  const nextInputs: Record<number, string> = {}
  for (const repo of response.repos) {
    nextInputs[repo.id] = repo.selectedBranch || repo.currentBranch || repo.indexedBranch || ''
  }
  branchInputs.value = nextInputs
}

async function loadWorkspaces() {
  const response = await api.listWorkspaces()
  workspaces.value = response.workspaces
  if (!workspaces.value.some((workspace) => workspace.slug === activeWorkspaceId.value)) {
    const fallback = workspaces.value.find((workspace) => workspace.isDefault) ?? workspaces.value[0]
    if (fallback) {
      setWorkspaceId(fallback.slug)
    }
  }
}

async function loadAdminPage() {
  loading.value = true
  error.value = null
  repoActionError.value = null
  try {
    await Promise.all([loadAdminHealth(), loadAdminRepos(), loadWorkspaces()])
    await loadWorkspaceBulkIndexJob()
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to load admin state'
  } finally {
    loading.value = false
  }
}

async function loadWorkspaceBulkIndexJob() {
  await refreshWorkspaceBulkIndexJob(false)
}

function startWorkspaceBulkIndexPolling() {
  if (disposed || workspaceBulkIndexPoll || workspaceBulkIndexRequest) return
  workspaceBulkIndexPoll = setTimeout(() => {
    workspaceBulkIndexPoll = null
    void refreshWorkspaceBulkIndexJob()
  }, 2500)
}

function stopWorkspaceBulkIndexPolling() {
  if (workspaceBulkIndexPoll) clearTimeout(workspaceBulkIndexPoll)
  workspaceBulkIndexPoll = null
  workspaceBulkIndexRequest?.abort()
  workspaceBulkIndexRequest = null
}

async function refreshWorkspaceBulkIndexJob(refreshOnCompletion = true) {
  if (disposed || workspaceBulkIndexRequest) return
  const workspace = activeWorkspaceId.value
  const controller = new AbortController()
  workspaceBulkIndexRequest = controller
  const wasRunning = workspaceBulkIndexJob.value?.status === 'running'
  try {
    const response = await api.activeWorkspaceBulkIndex(workspace, controller.signal)
    if (controller.signal.aborted || disposed || workspace !== activeWorkspaceId.value) return
    workspaceBulkIndexJob.value = response.job ?? null
    if (wasRunning && response.job?.status !== 'running' && refreshOnCompletion) {
      await Promise.all([loadWorkspaces(), loadAdminRepos()])
      if (controller.signal.aborted || disposed || workspace !== activeWorkspaceId.value) return
      pruneBulkParseSelection()
      clearCachedAdminHealth()
      await loadAdminHealth()
    }
  } catch (err) {
    if (!controller.signal.aborted && !disposed && workspace === activeWorkspaceId.value) {
      workspaceError.value = err instanceof Error ? err.message : 'Failed to refresh index job'
    }
  } finally {
    if (workspaceBulkIndexRequest === controller) {
      workspaceBulkIndexRequest = null
      if (workspace === activeWorkspaceId.value && workspaceBulkIndexJob.value?.status === 'running') {
        startWorkspaceBulkIndexPolling()
      }
    }
  }
}

async function runWorkspaceAction(label: string, action: () => Promise<unknown>) {
  workspaceAction.value = label
  workspaceError.value = null
  try {
    await action()
    await loadWorkspaces()
    await loadAdminRepos()
    clearCachedAdminHealth()
    await loadAdminHealth()
  } catch (err) {
    workspaceError.value = err instanceof Error ? err.message : 'Workspace action failed'
  } finally {
    workspaceAction.value = null
  }
}

async function createWorkspace() {
  const slug = workspaceCreateSlug.value.trim()
  if (!slug) return
  await runWorkspaceAction('create workspace', async () => {
    await api.createWorkspace({
      slug,
      from: workspaceCreateFrom.value || undefined,
    })
    setWorkspaceId(slug)
    workspaceCreateSlug.value = ''
  })
}

async function setWorkspaceRef() {
  const repo = workspaceRepoName.value.trim()
  const ref = workspaceRepoRef.value.trim()
  if (!repo || !ref) return
  await runWorkspaceAction('set ref', () => api.setWorkspaceRepoRef(activeWorkspaceId.value, repo, { ref }))
}

async function checkoutWorkspaceRepo() {
  const repo = workspaceRepoName.value.trim()
  if (!repo) return
  await runWorkspaceAction('checkout repo', () =>
    api.checkoutWorkspaceRepo(activeWorkspaceId.value, repo, {
      ref: workspaceRepoRef.value.trim() || undefined,
    })
  )
}

async function bulkCheckoutWorkspace() {
  const ref = workspaceBulkRef.value.trim()
  if (!ref || isDefaultMainWorkspace.value) return
  await runWorkspaceAction('set branch refs', async () => {
    workspaceBulkResult.value = await api.bulkCheckoutWorkspace(activeWorkspaceId.value, { mode: 'ref', ref })
    workspaceBulkIndexJob.value = null
    workspaceBulkSelectedRepos.value = []
  })
}

async function bulkCheckoutMainline() {
  if (isDefaultMainWorkspace.value) return
  await runWorkspaceAction('checkout mainline', async () => {
    workspaceBulkResult.value = await api.bulkCheckoutWorkspace(activeWorkspaceId.value, { mode: 'mainline' })
    workspaceBulkIndexJob.value = null
    workspaceBulkSelectedRepos.value = []
  })
}

function isBulkParseCandidate(item: BulkCheckoutWorkspaceRepoResult): boolean {
  if (workspaceRepoHasParsedSnapshot(item.repo)) return false
  return item.status === 'configured' || item.status === 'checked_out'
}

function workspaceRepoHasParsedSnapshot(repoName: string): boolean {
  const repo = activeWorkspaceRepoByName.value.get(repoName)
  return Boolean(repo?.activeSnapshotId)
}

function pruneBulkParseSelection() {
  const candidates = new Set(workspaceBulkParseCandidates.value.map((item) => item.repo))
  workspaceBulkSelectedRepos.value = workspaceBulkSelectedRepos.value.filter((repo) => candidates.has(repo))
}

function selectAllBulkParseCandidates() {
  workspaceBulkSelectedRepos.value = workspaceBulkParseCandidates.value.map((item) => item.repo)
}

function clearBulkParseSelection() {
  workspaceBulkSelectedRepos.value = []
}

async function indexSelectedBulkRepos() {
  const repos = [...workspaceBulkSelectedRepos.value]
  if (!repos.length) return
  workspaceAction.value = 'starting parse selected repos'
  workspaceError.value = null
  try {
    const response = await api.bulkIndexWorkspace(activeWorkspaceId.value, { repos, resolve: true })
    workspaceBulkIndexJob.value = response.job ?? null
    if (workspaceBulkIndexJob.value?.status === 'running') {
      startWorkspaceBulkIndexPolling()
    }
  } catch (err) {
    workspaceError.value = err instanceof Error ? err.message : 'Workspace bulk index failed to start'
  } finally {
    workspaceAction.value = null
  }
}

async function fetchWorkspaceRepo() {
  const repo = workspaceRepoName.value.trim()
  if (!repo) return
  await runWorkspaceAction('fetch refs', () => api.fetchWorkspaceRepo(activeWorkspaceId.value, repo))
}

async function indexWorkspaceRepo() {
  const repo = workspaceRepoName.value.trim()
  if (!repo) return
  await runWorkspaceAction('index repo', () =>
    api.indexWorkspaceRepo(activeWorkspaceId.value, repo, {
      resolve: true,
    })
  )
}

function selectWorkspaceRepo(repo: string, ref?: string) {
  workspaceRepoName.value = repo
  workspaceRepoRef.value = ref ?? ''
}

function formatNumber(value: number): string {
  return new Intl.NumberFormat().format(value)
}

function formatPercent(value: number): string {
  return `${value.toFixed(1)}%`
}

function formatDateTime(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  return new Intl.DateTimeFormat(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
  }).format(date)
}

function formatRelative(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  const diffMs = date.getTime() - Date.now()
  const diffHours = diffMs / (1000 * 60 * 60)
  if (Math.abs(diffHours) < 24) {
    return relativeFormatter.format(Math.round(diffHours), 'hour')
  }
  return relativeFormatter.format(Math.round(diffHours / 24), 'day')
}

function formatAgeHours(ageHours: number): string {
  if (ageHours < 24) {
    return `${ageHours.toFixed(1)}h`
  }
  return `${(ageHours / 24).toFixed(1)}d`
}



function headlineStatusClass(status: 'ok' | 'warn'): string {
  return status === 'ok'
    ? 'text-emerald-800 dark:text-emerald-200'
    : 'text-amber-800 dark:text-amber-200'
}

function workspaceRepoStatusClass(status?: string): string {
  const value = (status || 'unknown').toLowerCase()
  if (value === 'ok' || value === 'fresh' || value === 'indexed' || value === 'inherited') {
    return 'border-emerald-600/50 bg-emerald-50 text-emerald-700 dark:border-emerald-500/40 dark:bg-emerald-900/30 dark:text-emerald-200'
  }
  if (value === 'stale' || value === 'pending' || value === 'running' || value === 'configured' || value === 'unindexed' || value === 'checking_out' || value === 'checked_out' || value === 'indexing') {
    return 'border-amber-500/50 bg-amber-50 text-amber-700 dark:border-amber-500/40 dark:bg-amber-900/30 dark:text-amber-200'
  }
  if (value === 'failed' || value === 'error') {
    return 'border-red-500/50 bg-red-50 text-red-700 dark:border-red-500/40 dark:bg-red-900/30 dark:text-red-200'
  }
  return 'border-black/20 bg-gray-50 text-n-600 dark:border-n-600 dark:bg-n-800 dark:text-n-300'
}

function workspaceRepoStatusLabel(status?: string): string {
  const value = (status || 'unknown').toLowerCase()
  switch (value) {
    case 'ok':
    case 'fresh':
    case 'indexed':
      return 'parsed'
    case 'inherited':
      return 'baseline parsed'
    case 'configured':
    case 'unindexed':
      return 'ref set'
    case 'checked_out':
      return 'worktree ready'
    case 'checking_out':
      return 'checking out'
    case 'indexing':
    case 'running':
      return 'parsing'
    case 'failed':
    case 'error':
      return 'failed'
    default:
      return value
  }
}

function workspaceRepoStatusTitle(status?: string): string {
  const value = (status || 'unknown').toLowerCase()
  switch (value) {
    case 'ok':
    case 'fresh':
    case 'indexed':
      return 'This workspace has an active parsed snapshot for this repo.'
    case 'inherited':
      return 'This workspace is using a parsed baseline snapshot inherited from the source workspace.'
    case 'configured':
    case 'unindexed':
      return 'The branch ref is recorded, but this repo has not been parsed for this workspace yet.'
    case 'checked_out':
      return 'A workspace worktree exists at the ref, but parsing has not completed yet.'
    case 'checking_out':
      return 'Tirion is preparing the workspace worktree.'
    case 'indexing':
    case 'running':
      return 'Tirion is parsing this repo.'
    case 'failed':
    case 'error':
      return 'The last workspace operation failed.'
    default:
      return 'Workspace repo state.'
  }
}

function refreshStatusClass(refresh: AdminHealthResponse['refresh']): string {
  if (refresh.status === 'failed' && refresh.superseded) {
    return 'text-amber-600 dark:text-amber-300'
  }
  switch (refresh.status) {
    case 'ok':
      return 'text-emerald-600 dark:text-emerald-300'
    case 'running':
      return 'text-blue-600 dark:text-blue-300'
    case 'failed':
      return 'text-red-600 dark:text-red-300'
    default:
      return 'text-n-500'
  }
}

function refreshStatusLabel(refresh: AdminHealthResponse['refresh']): string {
  if (refresh.status === 'failed' && refresh.superseded) {
    return 'historical failure'
  }
  switch (refresh.status) {
    case 'ok':
      return 'ok'
    case 'running':
      return 'running'
    case 'failed':
      return 'failed'
    default:
      return 'unknown'
  }
}

function freshnessClass(status: 'recent' | 'aging' | 'stale'): string {
  if (status === 'recent') {
    return 'bg-accent text-white dark:border-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300'
  }
  if (status === 'aging') {
    return 'border-amber-500 bg-amber-50 text-amber-700 dark:border-amber-700 dark:bg-amber-900/20 dark:text-amber-300'
  }
  return 'border-red-500 bg-red-50 text-red-700 dark:border-red-700 dark:bg-red-900/20 dark:text-red-300'
}

function rowClass(status: AdminRepoRow['freshnessStatus']): string {
  if (status === 'stale') {
    return 'bg-red-50/40 dark:bg-red-950/10'
  }
  if (status === 'aging') {
    return 'bg-amber-50/30 dark:bg-amber-950/10'
  }
  return ''
}

function driftClass(status: AdminRepoRow['driftStatus']): string {
  if (status === 'in_sync') {
    return 'bg-accent text-white dark:border-emerald-700 dark:bg-emerald-900/20 dark:text-emerald-300'
  }
  if (status === 'drift') {
    return 'border-amber-500 bg-amber-50 text-amber-700 dark:border-amber-700 dark:bg-amber-900/20 dark:text-amber-300'
  }
  return 'border-n-400 bg-gray-50 text-n-700 dark:border-n-600 dark:bg-n-800 dark:text-n-300'
}

function driftLabel(repo: AdminRepoRow): string {
  if (repo.driftStatus === 'in_sync') return 'in sync'
  if (repo.driftStatus === 'drift') {
    if (repo.branchDrift && repo.reparseNeeded) return 'branch + commit drift'
    if (repo.branchDrift) return 'branch drift'
    if (repo.reparseNeeded) return 'parse needed'
  }
  return 'unknown'
}

function formatOperationLabel(operation?: string, status?: string): string {
  if (!operation) return ''
  const label = operation.replace(/_/g, ' ')
  if (!status) return label
  return `${label} · ${status}`
}

function formatShortSHA(value?: string): string {
  if (!value) return '—'
  return value.slice(0, 8)
}

function isRepoActionBlocked(repo: AdminRepoRow): boolean {
  return workspaceBusy.value || pendingRepoId.value !== null || repo.busy || activeOperation.value !== null
}

function isRepoActionRunning(repoId: number, action: 'fetch' | 'checkout' | 'parse' | 'parse_resolve'): boolean {
  return pendingRepoId.value === repoId && pendingAction.value === action
}

async function runFetch(repoId: number) {
	pendingAction.value = 'fetch'
	await runRepoAction(repoId, async () => {
		const repo = repoRows.value.find((item) => item.id === repoId)
		if (repo && activeWorkspaceId.value !== 'default-main') {
			await api.fetchWorkspaceRepo(activeWorkspaceId.value, repo.name)
			return api.adminRepos()
		}
		return api.adminRepoFetch(repoId)
	})
}

async function runMenuFetch(repoId: number) {
  openRepoMenuId.value = null
  await runFetch(repoId)
}

async function runCheckout(repoId: number) {
  pendingAction.value = 'checkout'
  await runRepoAction(repoId, () => api.adminRepoCheckout(repoId, { branch: branchInputs.value[repoId] || '' }))
}

async function runParse(repoId: number, resolve: boolean) {
  pendingAction.value = resolve ? 'parse_resolve' : 'parse'
  await runRepoAction(repoId, async () => {
    const response = await api.adminRepoParse(repoId, { resolve })
    clearCachedAdminHealth()
    await loadAdminHealth()
    return response
  })
}

async function runMenuParse(repoId: number) {
  openRepoMenuId.value = null
  await runParse(repoId, false)
}

async function runRepoAction(repoId: number, action: () => Promise<{ repos: AdminRepoRow[]; activeOperation?: AdminRepoOperationState }>) {
  pendingRepoId.value = repoId
  pendingStartedAt.value = new Date().toISOString()
  openRepoMenuId.value = null
  repoActionError.value = null
  try {
    const response = await action()
    repoRows.value = response.repos
    activeOperation.value = response.activeOperation ?? null
  } catch (err) {
    repoActionError.value = err instanceof Error ? err.message : 'Repo action failed'
    await loadAdminRepos()
  } finally {
    pendingRepoId.value = null
    pendingAction.value = null
    pendingStartedAt.value = null
  }
}

function toggleRepoMenu(repoId: number, event: MouseEvent) {
  if (openRepoMenuId.value === repoId) {
    closeRepoMenu()
    return
  }
  const target = event.currentTarget as HTMLElement | null
  if (target) {
    const rect = target.getBoundingClientRect()
    repoMenuPosition.value = {
      top: rect.bottom + 6,
      left: Math.max(12, rect.right - 208),
    }
  }
  openRepoMenuId.value = repoId
}

function closeRepoMenu() {
  openRepoMenuId.value = null
}

function formatReason(reason: 'path_unmatched' | 'method_mismatched'): string {
  return reason === 'method_mismatched' ? 'Method mismatch' : 'Path unmatched'
}

function reasonClass(reason: 'path_unmatched' | 'method_mismatched'): string {
  if (reason === 'method_mismatched') {
    return 'border-amber-400 bg-white text-amber-800 dark:border-amber-700 dark:bg-n-900 dark:text-amber-200'
  }
  return 'border-red-400 bg-white text-red-800 dark:border-red-700 dark:bg-n-900 dark:text-red-200'
}

function compactPath(path: string): string {
  const parts = path.split('/').filter(Boolean)
  if (parts.length <= 4) return path
  return `.../${parts.slice(-4).join('/')}`
}

function showMoreRepos() {
  visibleCount.value += 25
}

watch([search, freshnessFilter], () => {
  visibleCount.value = 25
})

watch(activeWorkspaceId, async () => {
  repoActionError.value = null
  workspaceError.value = null
  workspaceBulkResult.value = null
  workspaceBulkIndexJob.value = null
  workspaceBulkSelectedRepos.value = []
  stopWorkspaceBulkIndexPolling()
  branchInputs.value = {}
  visibleCount.value = 25
  try {
    await Promise.all([loadAdminRepos(), loadWorkspaces()])
    await loadWorkspaceBulkIndexJob()
  } catch (err) {
    repoActionError.value = err instanceof Error ? err.message : 'Failed to load workspace repo state'
  }
})

onMounted(() => {
  loadAdminPage()
})

onUnmounted(() => {
  disposed = true
  stopWorkspaceBulkIndexPolling()
})
</script>
