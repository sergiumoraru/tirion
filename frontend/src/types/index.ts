// API Response Types - matching server-api.md

export interface ApiError {
  error: {
    code: string
    message: string
    details?: Record<string, unknown>
  }
}

export interface Stats {
  repos: number
  files: number
  functions: number
  classes: number
  endpoints: number
}

export interface ResponseWorkspace {
  id: string
  name: string
}

export interface WorkspaceSnapshotRef {
  workspaceId: number
  workspace: string
  repoId: number
  repoName: string
  snapshotId: number
  branch: string
  sha: string
  indexedAt: string
}

export interface WorkspaceRepo {
  workspaceId: number
  repoName: string
  targetRef: string
  resolvedBranch: string
  resolvedSha: string
  worktreePath: string
  activeSnapshotId?: number
  mainlineSnapshotId?: number
  mainlineResolvedSha?: string
  mainlineLastIndexedAt?: string
  lastCheckoutAt?: string
  lastIndexedAt?: string
  indexStatus: string
  lastError: string
  createdAt: string
  updatedAt: string
}

export interface WorkspaceSummary {
  id: number
  slug: string
  name: string
  description: string
  createdByLabel: string
  isDefault: boolean
  createdAt: string
  updatedAt: string
  activeSnapshots: WorkspaceSnapshotRef[]
  repos: WorkspaceRepo[]
}

export interface WorkspacesResponse {
  workspaces: WorkspaceSummary[]
}

export interface CreateWorkspaceRequest {
  slug: string
  name?: string
  description?: string
  from?: string
  default?: boolean
}

export interface SetWorkspaceRepoRefRequest {
  ref: string
}

export interface CheckoutWorkspaceRepoRequest {
  ref?: string
}

export interface BulkCheckoutWorkspaceRequest {
  ref?: string
  mode?: 'ref' | 'mainline'
}

export interface BulkCheckoutWorkspaceRepoResult {
  repo: string
  status: 'configured' | 'checked_out' | 'already_current' | 'skipped' | 'failed'
  branch?: string
  ref?: string
  sha?: string
  path?: string
  warning?: string
  error?: string
}

export interface BulkCheckoutWorkspaceResponse {
  workspace: WorkspaceSummary
  ref: string
  mode: 'ref' | 'mainline'
  configured: number
  checkedOut: number
  alreadyCurrent: number
  skipped: number
  failed: number
  results: BulkCheckoutWorkspaceRepoResult[]
}

export interface IndexWorkspaceRepoRequest {
  resolve?: boolean
}

export interface BulkIndexWorkspaceRequest {
  repos: string[]
  resolve?: boolean
}

export interface BulkIndexWorkspaceRepoResult {
  repo: string
  status: 'queued' | 'indexing' | 'ok' | 'failed' | 'skipped'
  error?: string
  startedAt?: string
  finishedAt?: string
}

export interface BulkIndexWorkspaceJob {
  id: string
  workspace: string
  resolve: boolean
  status: 'running' | 'ok' | 'failed'
  total: number
  completed: number
  failed: number
  currentRepo?: string
  startedAt: string
  finishedAt?: string
  results: BulkIndexWorkspaceRepoResult[]
}

export interface BulkIndexWorkspaceResponse {
  job?: BulkIndexWorkspaceJob
}

export interface IndexWorkspaceRequest {
  resolve?: boolean
  skipTests?: boolean
  skipUnchanged?: boolean
  exclude?: string
}

export interface WorkspaceHintMatch {
  workspaceId: string
  repo: string
  branch: string
  sha: string
  file?: string
  symbol?: string
  kind?: string
}

export interface WorkspaceHint {
  kind: string
  query: string
  activeWorkspace: string
  foundIn: WorkspaceHintMatch[]
}

export interface Repo {
  id: number
  name: string
  path: string
  file_count: number
}

export interface RepoDetail {
  id: number
  name: string
  path: string
  files: number
  stats: {
    functions: number
    classes: number
    endpoints: number
    files: number
    primaryLanguage: string
  }
}

export interface AdminHealthOverview {
  status: 'ok' | 'warn'
  serverStatus: 'ok' | 'warn'
  repos: number
  files: number
  functions: number
  classes: number
  endpoints: number
  recentRepos: number
  agingRepos: number
  staleRepos: number
  latestIndexedAt?: string
  oldestIndexedAt?: string
}

export interface AdminHealthAuditMetrics {
  duplicateEndpointGroups: number
  unresolvedHTTPCalls: number
  httpPathUnmatched: number
  httpMethodMismatched: number
  httpNonRouteLike: number
  httpInvalidMethodToken: number
  queueProducerTotal: number
  queueProducerCount: number
  queueMatchedCount: number
  queueMatchRate: number
  queueExactMatched: number
  queueNormalizedMatched: number
  queueUnmatched: number
  queueExternalNoConsumer: number
  dataWriteEntityCount: number
  dataSharedEntityCount: number
  dataCrossRepoCount: number
  dataCoverageRate: number
  dataNoReaderCount: number
  dataSameRepoOnlyCount: number
  dataSingleRepoCount: number
}

export interface AdminHealthUnresolvedHTTPRepoSummary {
  repo: string
  count: number
  pathUnmatched: number
  methodMismatched: number
}

export interface AdminHealthUnresolvedHTTPSample {
  repo: string
  callerId: string
  method: string
  path: string
  lineNumber: number
  clientType: string
  reason: 'path_unmatched' | 'method_mismatched'
  availableMethods: string[]
}

export interface AdminHealthUnresolvedHTTPDrilldown {
  topRepos: AdminHealthUnresolvedHTTPRepoSummary[]
  samples: AdminHealthUnresolvedHTTPSample[]
}

export interface AdminHealthAudit {
  status: 'ok' | 'warn'
  warnings: string[]
  metrics: AdminHealthAuditMetrics
  unresolvedHTTP: AdminHealthUnresolvedHTTPDrilldown
}

export interface AdminHealthRepo {
  id: number
  name: string
  path: string
  updatedAt: string
  ageHours: number
  freshnessStatus: 'recent' | 'aging' | 'stale'
  fileCount: number
  functionCount: number
  classCount: number
  endpointCount: number
  primaryLanguage: string
}

export interface AdminHealthRefresh {
  status: 'ok' | 'running' | 'failed' | 'unknown'
  workspace?: string
  attempt: number
  maxAttempts: number
  startedAt?: string
  updatedAt?: string
  lastSuccessAt?: string
  error?: string
  errorSummary?: string
  superseded?: boolean
}

export interface AdminHealthResponse {
  generatedAt: string
  version: string
  overview: AdminHealthOverview
  audit: AdminHealthAudit
  refresh: AdminHealthRefresh
  warnings: string[]
  repos: AdminHealthRepo[]
}

export interface AdminRepoOperationState {
  repoId: number
  repoName: string
  action: string
  startedAt: string
}

export interface AdminRepoRow {
  id: number
  name: string
  path: string
  updatedAt: string
  ageHours: number
  freshnessStatus: 'recent' | 'aging' | 'stale'
  fileCount: number
  functionCount: number
  classCount: number
  endpointCount: number
  primaryLanguage: string
  selectedBranch: string
  currentBranch: string
  headSha: string
  workspaceBranch: string
  workspaceCommit: string
  workspaceAvailable: boolean
  workspaceError?: string
  indexedBranch: string
  indexedSha: string
  indexedCommit: string
  indexedAt?: string
  dirty: boolean
  aheadCount: number
  behindCount: number
  branchDrift: boolean
  reparseNeeded: boolean
  driftStatus: 'unknown' | 'drift' | 'in_sync'
  indexAlignmentStatus: 'fresh' | 'stale' | 'unknown'
  indexAlignmentReasons?: string[]
  lastOperation?: string
  lastOperationStatus?: 'running' | 'ok' | 'failed' | ''
  lastOperationAt?: string
  lastError?: string
  busy: boolean
}

export interface AdminReposResponse {
  generatedAt: string
  activeOperation?: AdminRepoOperationState
  repos: AdminRepoRow[]
}

export interface AdminRepoCheckoutRequest {
  branch: string
}

export interface AdminRepoParseRequest {
  resolve?: boolean
}

// Search types
export interface FunctionResult {
  id: number
  name: string
  file: string
  repo: string
  snapshotId?: number
  startLine: number
  endLine: number
  source?: string
  score?: number
  richness?: number
  callerId?: string
  tags?: string[]
  integrations?: FunctionIntegration[]
}

export interface FunctionIntegration {
  type?: 'http' | 'sqs'
  callerId: string
  method: string
  path: string
  clientType?: string
  targetRepo: string
  targetFile: string
  targetHandler?: string
  lineNumber: number
  resolution: 'matched' | 'inferred' | 'unresolved'
}

export interface FunctionIntegrationsResponse {
  callerId: string
  integrations: FunctionIntegration[]
}

export interface ClassIntegration {
  sourceFunction: string
  callerId: string
  method: string
  path: string
  clientType?: string
  targetRepo: string
  targetFile: string
  targetHandler?: string
  lineNumber: number
  resolution: 'matched' | 'inferred' | 'unresolved'
}

export interface ClassHandledEndpoint {
  method: string
  path: string
  handler?: string
}

export interface ClassIntegrationsResponse {
  classId: number
  className: string
  integrations: ClassIntegration[]
  handledEndpoints: ClassHandledEndpoint[]
  methodCount: number
}

export interface ClassResult {
  id: number
  name: string
  file: string
  repo: string
  snapshotId?: number
  startLine: number
  endLine: number
  score?: number
  integrations?: ClassIntegration[]
  handledEndpoints?: ClassHandledEndpoint[]
}

export interface EndpointResult {
  id: number
  method: string
  path: string
  handler: string
  file: string
  repo: string
  snapshotId?: number
  line: number
  richness?: number
}

export interface DataEntityResult {
  id: number
  name: string
  file: string
  repo: string
  snapshotId?: number
  line: number
  access: string
  caller?: string
  callerId?: string
  source?: string
  score?: number
}

export interface ExternalSymbolResult {
  id: number
  name: string
  file: string
  repo: string
  snapshotId?: number
  line: number
  caller?: string
  callerId?: string
  source?: string
  score?: number
}

export interface ScheduleResult {
  id: number
  ruleName: string
  scheduleExpression: string
  targetType: string
  targetName: string
  state: string
  source: string
}

export interface GraphQLOperationResult {
  id: number
  name: string
  operationType: string
  file: string
  repo: string
  snapshotId?: number
  line: number
  usageCount: number
  score?: number
}

export interface AzureTriggerResult {
  id: number
  functionName: string
  triggerType: string
  route?: string
  resourceName?: string
  file: string
  repo: string
  snapshotId?: number
  line: number
  score?: number
}

export interface SearchBucketStats {
  returned: number
  total: number
  offset: number
  limit: number
  hasMore: boolean
  truncated: boolean
  exactTotal: boolean
  /** The source query stopped at its fetch cap; `total` is a lower bound. */
  capped?: boolean
  /** Matches hidden by the noNoise filter in this bucket. */
  filteredCount?: number
}

export interface SearchBucketOffsets {
  functions?: number
  classes?: number
  endpoints?: number
  dataEntities?: number
  externalSymbols?: number
  schedules?: number
  graphqlOperations?: number
  azureTriggers?: number
}

export interface SearchResponse {
  query: string
  mode: string
  workspace?: ResponseWorkspace
  repoContext?: WorkspaceSnapshotRef[]
  results: {
    functions: FunctionResult[]
    classes: ClassResult[]
    endpoints: EndpointResult[]
    dataEntities?: DataEntityResult[]
    externalSymbols?: ExternalSymbolResult[]
    schedules?: ScheduleResult[]
    graphqlOperations?: GraphQLOperationResult[]
    azureTriggers?: AzureTriggerResult[]
  }
  stats: {
    totalResults: number
    returnedResults: number
    searchTime: string
    limit: number
    hasMore: boolean
    truncated: boolean
    exactTotal: boolean
    buckets: {
      functions: SearchBucketStats
      classes: SearchBucketStats
      endpoints: SearchBucketStats
      dataEntities: SearchBucketStats
      externalSymbols: SearchBucketStats
      schedules: SearchBucketStats
      graphqlOperations: SearchBucketStats
      azureTriggers: SearchBucketStats
    }
  }
  workspaceHints?: WorkspaceHint[]
  warnings?: string[]
}

export interface SearchIntegrationsRequest {
  functionCallerIds?: string[]
  classIds?: number[]
}

export interface SearchIntegrationsClassPayload {
  integrations: ClassIntegration[]
  handledEndpoints: ClassHandledEndpoint[]
}

export interface SearchIntegrationsResponse {
  functions: Record<string, FunctionIntegration[]>
  classes: Record<string, SearchIntegrationsClassPayload>
  errors?: { kind: 'function' | 'class'; id: string; code: string }[]
}

// Trace types
export interface TraceMatch {
  repo: string
  file: string
  name: string
  qualified_id: string
}

export interface TraceNode {
  name: string
  file: string
  repo: string
  line?: number
  depth?: number
  source?: string
  children?: TraceNode[]
  low_signal?: boolean
  edge_type?: 'call' | 'http' | 'sqs' | 'eventbridge' | 'resolve'
  confidence?: 'high' | 'medium' | 'low'
  caller_id?: string
  is_cross_service?: boolean
  http_method?: string
  http_target?: string
  is_sqs?: boolean
  queue_target?: string
  injected?: boolean
  evidence?: {
    source?: string
    detail?: string
    repo?: string
    file?: string
    line?: number
  }
}

export interface TraceQualityStats {
  totalNodes: number
  maxDepth: number
  lowSignalCount: number
  edgeCounts: Record<string, number>
  confidenceCounts: Record<string, number>
  crossServiceCount: number
  directRatio: number
  resolvedRatio: number
  nameBasedRatio: number
}

export interface TraceDirectionCompleteness {
  returnedNodes: number
  availableNodes: number
  exactAvailableNodes: boolean
  truncated: boolean
}

export interface TraceCompleteness {
  appliedMaxNodes: number
  truncated: boolean
  truncationReasons?: string[]
  downstream: TraceDirectionCompleteness
  upstream: TraceDirectionCompleteness
}

export interface TraceResponse {
  function: string
  workspace?: ResponseWorkspace
  repoContext?: WorkspaceSnapshotRef[]
  matches: TraceMatch[]
  downstream: TraceNode[]
  upstream: TraceNode[]
  selectedMatch?: string
  upstreamSource?: string
  stats: {
    downstreamNodes: number
    upstreamNodes: number
    traceTime: string
  }
  completeness: TraceCompleteness
  quality?: {
    downstream: TraceQualityStats
    upstream: TraceQualityStats
  }
  workspaceHints?: WorkspaceHint[]
  warnings?: string[]
}

export interface TraceRequest {
  function: string
  workspaceId?: string
  match?: string
  profile?: string
  depth?: number
  noTests?: boolean
  resolve?: boolean
  exclude?: string[]
  includeRepos?: string[]
  excludeRepos?: string[]
  maxNodes?: number
}

export interface TraceExpandRequest {
  callerId: string
  direction: 'downstream' | 'upstream'
  workspaceId?: string
  profile?: string
  depth: number
  maxDepth: number
  noTests?: boolean
  resolve?: boolean
  exclude?: string[]
  includeRepos?: string[]
  excludeRepos?: string[]
  maxNodes?: number
  allowedRepo?: string
}

export interface TraceExpandResponse {
  workspace?: ResponseWorkspace
  children: TraceNode[]
  appliedMaxNodes: number
  completeness: TraceDirectionCompleteness
  warnings?: string[]
}

// Impact types
export interface ImpactFile {
  repo?: string
  path: string
}

export interface ImpactRange {
  repo?: string
  path: string
  startLine: number
  endLine: number
}

export interface ImpactRequest {
  functions?: string[]
  callerIds?: string[]
  functionIds?: number[]
  files?: ImpactFile[]
  ranges?: ImpactRange[]
  diff?: string
  repo?: string
  workspaceId?: string
  profile?: string
  depth?: number
  noTests?: boolean
  resolve?: boolean
  exclude?: string[]
  includeRepos?: string[]
  excludeRepos?: string[]
  maxNodes?: number
  includeTrace?: boolean
}

export interface ImpactEndpoint {
  method: string
  path: string
  handler: string
  repo: string
  file: string
  line: number
  minDepth: number
  fanout: number
  score: number
  owners?: string[]
  ownerSource?: string
}

export interface ImpactHttpCall {
  method: string
  path: string
  minDepth: number
  fanout: number
  score: number
  matches?: ImpactHttpMatch[]
}

export interface ImpactHttpMatch {
  repo: string
  handler: string
  file?: string
  line?: number
}

export interface ImpactQueue {
  name: string
  minDepth: number
  fanout: number
  score: number
}

export interface ImpactRepo {
  name: string
  minDepth: number
  fanout: number
  score: number
}

export interface ImpactSchedule {
  name: string
  schedule?: string
  state?: string
  minDepth: number
  fanout: number
  score: number
}

export interface ImpactSummary {
  entrypoints: ImpactEndpoint[]
  httpCalls: ImpactHttpCall[]
  queues: ImpactQueue[]
  eventBridgeSchedules?: ImpactSchedule[]
  repos: ImpactRepo[]
}

export interface ImpactNode {
  id: string
  type: string
  name: string
  repo?: string
  file?: string
  line?: number
  method?: string
  path?: string
  queue?: string
}

export interface ImpactEdge {
  source: string
  target: string
  type: string
  confidence?: string
  evidence?: {
    source?: string
    detail?: string
  }
}

export interface ImpactReport {
  nodes: ImpactNode[]
  edges: ImpactEdge[]
}

export interface ImpactStats {
  roots: number
  downstreamNodes: number
  upstreamNodes: number
  totalNodes: number
  totalEdges: number
  impactTime: string
}

export interface ImpactDirectionCompleteness {
  returnedNodes: number
  availableNodes: number
  exactAvailableNodes: boolean
  truncated: boolean
}

export interface ImpactCompleteness {
  appliedMaxNodes: number
  truncated: boolean
  truncationReasons?: string[]
  downstream: ImpactDirectionCompleteness
  upstream: ImpactDirectionCompleteness
}

export interface ImpactResponse {
  workspace?: ResponseWorkspace
  repoContext?: WorkspaceSnapshotRef[]
  roots: string[]
  summary: ImpactSummary
  report: ImpactReport
  stats: ImpactStats
  completeness: ImpactCompleteness
  downstream?: TraceNode[]
  upstream?: TraceNode[]
  workspaceHints?: WorkspaceHint[]
  warnings?: string[]
}

// Flow
export interface FlowRequest {
  start: string
  workspaceId?: string
  depth?: number
  maxHops?: number
  includeRelatedEntities?: boolean
  strictMode?: boolean
}

export interface FlowEndpoint {
  repo: string
  handler: string
  file: string
  line?: number
  method?: string
  path?: string
}

export interface FlowVia {
  type: 'http' | 'sqs' | 'data' | 'call'
  method?: string
  path?: string
  queue?: string
  clientType?: string
  external?: boolean
  entity?: string
  table?: string
  access?: string
}

export interface FlowHop {
  depth: number
  from: FlowEndpoint
  via: FlowVia
  to: FlowEndpoint
}

export interface FlowNarrativeStep {
  kind: 'internal' | 'hop' | 'caller' | 'data-store' | 'branch'
  from: FlowEndpoint
  via?: FlowVia
  to: FlowEndpoint
  condition?: string
  evidence?: string[]
  confidence?: 'exact' | 'unknown'
}

export interface FlowNarrative {
  root: FlowEndpoint
  steps: FlowNarrativeStep[]
}

export interface FlowStats {
  hops: number
  roots: number
  duration: string
}

export interface FlowCompleteness {
  appliedMaxHops: number
  returnedHops: number
  availableHops: number
  exactAvailableHops: boolean
  truncated: boolean
  truncationReasons?: string[]
}

export interface FlowAssumptions {
  strictMode: boolean
  includeRelatedEntities: boolean
  includeInternalCalls: boolean
  internalCallHopSoftLimit?: number
  strictModeReducedHops?: number
  internalHopsPruned?: number
  internalCallsSoftLimited?: boolean
}

export interface FlowResponse {
  workspace?: ResponseWorkspace
  repoContext?: WorkspaceSnapshotRef[]
  roots: FlowEndpoint[]
  hops: FlowHop[]
  narratives?: FlowNarrative[]
  callers?: FlowEndpoint[]
  candidates?: FlowDiscoveryCandidate[]
  stats: FlowStats
  completeness: FlowCompleteness
  assumptions: FlowAssumptions
  workspaceHints?: WorkspaceHint[]
  warnings?: string[]
}

export interface FlowDiscoveryCandidate {
  entity: string
  from: FlowEndpoint
  endpoint: FlowEndpoint
  reason: string
}

export interface FunctionDataAccess {
  callerId: string
  entityName: string
  access: string
  lineNumber: number
}

export interface FunctionDataAccessResponse {
  callerId: string
  accesses: FunctionDataAccess[]
}

// Service contracts
export interface ContractSummary {
  repo: string
  endpointCount: number
  httpCallCount: number
  graphqlOperations?: number
  graphqlUsages?: number
  graphqlResolvers?: number
  graphqlPermissions?: number
  graphqlEntrypoints?: number
  dataAccessCount?: number
  azureTimerTriggers?: number
  queuesProduced: number
  queuesConsumed: number
}

export interface ContractEndpoint {
  method: string
  path: string
  handler: string
  file: string
  line: number
  owners?: string[]
}

export interface ContractHttpMatch {
  repo: string
  handler: string
  path: string
  file: string
  line: number
}

export interface ContractHttpCall {
  method: string
  path: string
  count: number
  clientType?: string
  external?: boolean
  matches?: ContractHttpMatch[]
}

export interface ContractQueue {
  name: string
  count: number
  counterparties?: string[]
}

export interface ContractRepoCount {
  repo: string
  count: number
}

export interface ContractGraphQLOperation {
  name: string
  type: string
  file: string
  line: number
}

export interface ContractGraphQLUsage {
  importedAs: string
  importPath: string
  caller: string
  file: string
  line: number
}

export interface ContractGraphQLResolver {
  operationName: string
  operationType: string
  resolver: string
  file: string
  line: number
}

export interface ContractGraphQLPermission {
  operationName: string
  operationType: string
  ruleExpression: string
  file: string
  line: number
}

export interface ContractGraphQLEntrypoint {
  handlerName?: string
  registrationKind: string
  controllersPath?: string
  file: string
  line: number
}

export interface ContractDataAccess {
  entity: string
  access: string
  caller: string
  file: string
  line: number
}

export interface ContractSchedule {
  ruleName: string
  scheduleExpression: string
  targetQueue: string
  state: string
  source?: string
  functionName?: string
  file?: string
  line?: number
}

export interface ContractDetail {
  repo: string
  endpoints: ContractEndpoint[]
  httpCalls: ContractHttpCall[]
  httpTargets?: ContractRepoCount[]
  httpCallers?: ContractRepoCount[]
  graphqlOperations?: ContractGraphQLOperation[]
  graphqlUsages?: ContractGraphQLUsage[]
  graphqlTargets?: ContractRepoCount[]
  graphqlCallers?: ContractRepoCount[]
  graphqlResolvers?: ContractGraphQLResolver[]
  graphqlPermissions?: ContractGraphQLPermission[]
  graphqlEntrypoints?: ContractGraphQLEntrypoint[]
  dataAccesses?: ContractDataAccess[]
  queuesProduced: ContractQueue[]
  queuesConsumed: ContractQueue[]
  eventBridgeTriggers?: ContractSchedule[]
  azureTimerTriggers?: ContractSchedule[]
}

export interface ContractsResponse {
  workspace?: ResponseWorkspace
  repoContext?: WorkspaceSnapshotRef[]
  services?: ContractSummary[]
  service?: ContractDetail
}

// Graph types
export interface GraphNode {
  id: string
  type: 'function' | 'class' | 'endpoint' | 'queue' | 'file' | 'repo'
  name: string
  properties: Record<string, unknown>
}

export interface GraphEdge {
  source: string
  target: string
  type: 'CALLS' | 'HTTP_CALLS' | 'SQS_SENDS' | 'SQS_CONSUMED_BY' | 'SQS' | 'CONTAINS'
  properties?: {
    line?: number
    count?: number
    queue?: string
  }
}

export interface GraphResponse {
  workspace?: ResponseWorkspace
  repoContext?: WorkspaceSnapshotRef[]
  center?: GraphNode
  nodes: GraphNode[]
  edges: GraphEdge[]
  warnings?: string[]
}

export interface PathResponse {
  workspace?: ResponseWorkspace
  repoContext?: WorkspaceSnapshotRef[]
  paths: Array<{
    workspace?: ResponseWorkspace
    repoContext?: WorkspaceSnapshotRef[]
    nodes: GraphNode[]
    edges: GraphEdge[]
    warnings?: string[]
  }>
  warnings?: string[]
}
