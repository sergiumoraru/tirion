import type {
  Stats,
  Repo,
  RepoDetail,
  SearchResponse,
  SearchIntegrationsRequest,
  SearchIntegrationsResponse,
  SearchBucketOffsets,
  TraceRequest,
  TraceResponse,
  TraceExpandRequest,
  TraceExpandResponse,
  GraphResponse,
  PathResponse,
  ImpactRequest,
  ImpactResponse,
  FlowRequest,
  FlowResponse,
  FunctionDataAccessResponse,
  FunctionIntegrationsResponse,
  ClassIntegrationsResponse,
  ContractsResponse,
  AdminHealthResponse,
  AdminReposResponse,
  AdminRepoCheckoutRequest,
  AdminRepoParseRequest,
  WorkspacesResponse,
  WorkspaceSummary,
  WorkspaceRepo,
  CreateWorkspaceRequest,
  SetWorkspaceRepoRefRequest,
  CheckoutWorkspaceRepoRequest,
  BulkCheckoutWorkspaceRequest,
  BulkCheckoutWorkspaceResponse,
  BulkIndexWorkspaceRequest,
  BulkIndexWorkspaceResponse,
  IndexWorkspaceRepoRequest,
  IndexWorkspaceRequest,
} from '@/types'
import { getActiveWorkspaceId } from '@/composables/useWorkspace'

import { apiToken, clearApiAuth } from '@/composables/useApiAuth'

const API_BASE = import.meta.env.VITE_API_URL || '/api'

class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

function normalizeAdminHealthResponse(response: AdminHealthResponse): AdminHealthResponse {
  return {
    ...response,
    warnings: Array.isArray(response.warnings) ? response.warnings : [],
    repos: Array.isArray(response.repos) ? response.repos : [],
    audit: {
      ...response.audit,
      warnings: Array.isArray(response.audit?.warnings) ? response.audit.warnings : [],
      unresolvedHTTP: {
        topRepos: Array.isArray(response.audit?.unresolvedHTTP?.topRepos)
          ? response.audit.unresolvedHTTP.topRepos
          : [],
        samples: Array.isArray(response.audit?.unresolvedHTTP?.samples)
          ? response.audit.unresolvedHTTP.samples.map((sample) => ({
              ...sample,
              availableMethods: Array.isArray(sample.availableMethods) ? sample.availableMethods : [],
            }))
          : [],
      },
    },
  }
}

function workspaceHeader(): Record<string, string> {
  const workspaceId = getActiveWorkspaceId()
  return workspaceId ? { 'X-Tirion-Workspace': workspaceId } : {}
}

function mergeHeaders(headers?: HeadersInit): Headers {
  const merged = new Headers({
    'Content-Type': 'application/json',
    ...workspaceHeader(),
    ...(apiToken.value ? { 'X-Tirion-Token': apiToken.value } : {}),
  })
  if (headers) {
    new Headers(headers).forEach((value, key) => merged.set(key, value))
  }
  return merged
}

async function request<T>(endpoint: string, options?: RequestInit): Promise<T> {
  const response = await fetch(`${API_BASE}${endpoint}`, {
    ...options,
    headers: mergeHeaders(options?.headers),
  })

  if (!response.ok) {
    if (response.status === 401) clearApiAuth()
    let errorMessage = `API error: ${response.status}`
    let errorCode = 'UNKNOWN_ERROR'
    try {
      const errorBody = await response.json()
      if (errorBody.error) {
        errorMessage = errorBody.error.message
        errorCode = errorBody.error.code
      }
    } catch {
      // ignore parse error
    }
    throw new ApiError(response.status, errorCode, errorMessage)
  }

  return response.json()
}

export const api = {
  // Health & Stats
  health: () => request<{ status: string }>('/health'),
  stats: () => request<Stats>('/stats'),
  adminHealth: async () => normalizeAdminHealthResponse(await request<AdminHealthResponse>('/admin/health')),
  adminRepos: () => request<AdminReposResponse>('/admin/repos'),
  adminRepoFetch: (id: number) =>
    request<AdminReposResponse>(`/admin/repos/${id}/fetch`, {
      method: 'POST',
      body: JSON.stringify({}),
    }),
  adminRepoCheckout: (id: number, body: AdminRepoCheckoutRequest) =>
    request<AdminReposResponse>(`/admin/repos/${id}/checkout`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  adminRepoParse: (id: number, body: AdminRepoParseRequest) =>
    request<AdminReposResponse>(`/admin/repos/${id}/parse`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  listWorkspaces: () => request<WorkspacesResponse>('/workspaces'),
  createWorkspace: (body: CreateWorkspaceRequest) =>
    request<WorkspaceSummary>('/workspaces', {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  setDefaultWorkspace: (workspaceId: string) =>
    request<WorkspaceSummary>(`/workspaces/${encodeURIComponent(workspaceId)}/default`, {
      method: 'POST',
      body: JSON.stringify({}),
    }),
  setWorkspaceRepoRef: (workspaceId: string, repo: string, body: SetWorkspaceRepoRefRequest) =>
    request<WorkspaceRepo>(`/workspaces/${encodeURIComponent(workspaceId)}/repos/${encodeURIComponent(repo)}/ref`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  fetchWorkspaceRepo: (workspaceId: string, repo: string) =>
    request<WorkspaceRepo>(`/workspaces/${encodeURIComponent(workspaceId)}/repos/${encodeURIComponent(repo)}/fetch`, {
      method: 'POST',
      body: JSON.stringify({}),
    }),
  checkoutWorkspaceRepo: (workspaceId: string, repo: string, body: CheckoutWorkspaceRepoRequest) =>
    request<WorkspaceRepo>(`/workspaces/${encodeURIComponent(workspaceId)}/repos/${encodeURIComponent(repo)}/checkout`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  bulkCheckoutWorkspace: (workspaceId: string, body: BulkCheckoutWorkspaceRequest) =>
    request<BulkCheckoutWorkspaceResponse>(`/workspaces/${encodeURIComponent(workspaceId)}/bulk-checkout`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  bulkIndexWorkspace: (workspaceId: string, body: BulkIndexWorkspaceRequest) =>
    request<BulkIndexWorkspaceResponse>(`/workspaces/${encodeURIComponent(workspaceId)}/bulk-index`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  activeWorkspaceBulkIndex: (workspaceId: string, signal?: AbortSignal) =>
    request<BulkIndexWorkspaceResponse>(`/workspaces/${encodeURIComponent(workspaceId)}/bulk-index/active`, { signal }),
  indexWorkspaceRepo: (workspaceId: string, repo: string, body: IndexWorkspaceRepoRequest) =>
    request<WorkspaceSummary>(`/workspaces/${encodeURIComponent(workspaceId)}/repos/${encodeURIComponent(repo)}/index`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),
  indexWorkspace: (workspaceId: string, body: IndexWorkspaceRequest) =>
    request<WorkspaceSummary>(`/workspaces/${encodeURIComponent(workspaceId)}/index`, {
      method: 'POST',
      body: JSON.stringify(body),
    }),

  // Repos
  listRepos: () => request<{ repos: Repo[] }>('/repos'),
  getRepo: (id: number) => request<RepoDetail>(`/repos/${id}`),

  // Search
  search: (
    q: string,
    mode: 'keyword' | 'trigram' = 'keyword',
    limit = 10,
    repo?: string,
    offset?: number,
    signal?: AbortSignal,
    sort?: string,
    bucketOffsets?: SearchBucketOffsets
  ) => {
    const params = new URLSearchParams({ q, mode, limit: String(limit) })
    if (repo) params.set('repo', repo)
    if (offset !== undefined) params.set('offset', String(offset))
    if (sort) params.set('sort', sort)
    if (bucketOffsets?.functions !== undefined) params.set('functionsOffset', String(bucketOffsets.functions))
    if (bucketOffsets?.classes !== undefined) params.set('classesOffset', String(bucketOffsets.classes))
    if (bucketOffsets?.endpoints !== undefined) params.set('endpointsOffset', String(bucketOffsets.endpoints))
    if (bucketOffsets?.dataEntities !== undefined) params.set('dataEntitiesOffset', String(bucketOffsets.dataEntities))
    if (bucketOffsets?.externalSymbols !== undefined) params.set('externalSymbolsOffset', String(bucketOffsets.externalSymbols))
    if (bucketOffsets?.schedules !== undefined) params.set('schedulesOffset', String(bucketOffsets.schedules))
    if (bucketOffsets?.graphqlOperations !== undefined) params.set('graphqlOperationsOffset', String(bucketOffsets.graphqlOperations))
    if (bucketOffsets?.azureTriggers !== undefined) params.set('azureTriggersOffset', String(bucketOffsets.azureTriggers))
    return request<SearchResponse>(`/search?${params}`, { signal })
  },
  searchIntegrations: (body: SearchIntegrationsRequest, signal?: AbortSignal) =>
    request<SearchIntegrationsResponse>('/search/integrations', {
      method: 'POST',
      body: JSON.stringify(body),
      signal,
    }),

  // Trace
  trace: (options: TraceRequest, signal?: AbortSignal) =>
    request<TraceResponse>('/trace', {
      method: 'POST',
      body: JSON.stringify(options),
      signal,
    }),
  traceExpand: (options: TraceExpandRequest, signal?: AbortSignal) =>
    request<TraceExpandResponse>('/trace/expand', {
      method: 'POST',
      body: JSON.stringify(options),
      signal,
    }),

  // Impact
  impact: (options: ImpactRequest, signal?: AbortSignal) =>
    request<ImpactResponse>('/impact', {
      method: 'POST',
      body: JSON.stringify(options),
      signal,
    }),
  // Flow
  flow: (options: FlowRequest, signal?: AbortSignal) =>
    request<FlowResponse>('/flow', {
      method: 'POST',
      body: JSON.stringify(options),
      signal,
    }),
  functionDataAccess: (callerId: string, signal?: AbortSignal) => {
    const params = new URLSearchParams({ callerId })
    return request<FunctionDataAccessResponse>(`/functions/data-access?${params}`, { signal })
  },
  functionIntegrations: (callerId: string, signal?: AbortSignal) => {
    const params = new URLSearchParams({ callerId })
    return request<FunctionIntegrationsResponse>(`/functions/integrations?${params}`, { signal })
  },
  classIntegrations: (id: number, signal?: AbortSignal) => {
    const params = new URLSearchParams({ id: String(id) })
    return request<ClassIntegrationsResponse>(`/classes/integrations?${params}`, { signal })
  },

  // Service contracts
  listContracts: (query?: { q?: string; limit?: number }) => {
    const params = new URLSearchParams()
    if (query?.q) params.set('q', query.q)
    if (query?.limit) params.set('limit', String(query.limit))
    const suffix = params.toString() ? `?${params}` : ''
    return request<ContractsResponse>(`/contracts${suffix}`)
  },
  getContract: (repo: string, limit?: number) => {
    const params = new URLSearchParams()
    if (limit) params.set('limit', String(limit))
    const suffix = params.toString() ? `?${params}` : ''
    return request<ContractsResponse>(`/contracts/${encodeURIComponent(repo)}${suffix}`)
  },

  // Graph
  findPath: (
    from: string,
    to: string,
    fromType = 'function',
    toType = 'function',
    maxDepth = 5
  ) => {
    const params = new URLSearchParams({
      from,
      to,
      fromType,
      toType,
      maxDepth: String(maxDepth),
    })
    return request<PathResponse>(`/graph/path?${params}`)
  },

  getRepoDependencies: () => request<GraphResponse>('/graph/repo-dependencies'),
}

export { ApiError }
