const API_BASE = process.env.NEXT_PUBLIC_API_URL || ''

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public traceId?: string,
    public retryable?: boolean,
  ) {
    super(message)
    this.name = 'ApiError'
  }
}

// Paths that must NEVER trigger the global 401 redirect. /auth/me is the
// probe itself; redirecting on its 401 would loop. /auth/login is obviously
// exempt for the same reason.
const AUTH_EXEMPT_PATHS = ['/api/v1/auth/me', '/api/v1/auth/login', '/api/v1/auth/register']

function shouldRedirectOn401(requestPath: string): boolean {
  if (typeof window === 'undefined') return false
  if (window.location.pathname.startsWith('/login')) return false
  return !AUTH_EXEMPT_PATHS.some((p) => requestPath.startsWith(p))
}

async function handleResponse<T>(res: Response, requestPath: string): Promise<T> {
  if (res.status === 401 && shouldRedirectOn401(requestPath)) {
    const next = encodeURIComponent(window.location.pathname + window.location.search)
    window.location.href = `/login?next=${next}`
    // Still throw so React Query / callers see a terminal failure — the
    // navigation above will unload the page before they can react, but if
    // the browser delays we want the promise chain to short-circuit.
    throw new ApiError(401, 'UNAUTHORIZED', 'Session expired. Redirecting to login.')
  }
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    const err = body.error || body
    throw new ApiError(
      res.status,
      err.code || `HTTP_${res.status}`,
      err.message || err.detail || res.statusText,
      err.traceId || res.headers.get('x-trace-id') || undefined,
      err.retryable ?? res.status >= 500,
    )
  }
  const text = await res.text()
  if (!text || text === 'null') return ([] as unknown) as T
  return JSON.parse(text)
}

// ── Auth token management ──

let authToken: string | null = null

export function getAuthToken(): string | null {
  if (authToken) return authToken
  if (typeof window !== 'undefined') {
    authToken = localStorage.getItem('levara_token')
  }
  return authToken
}

export function setAuthToken(token: string | null) {
  authToken = token
  if (typeof window !== 'undefined') {
    if (token) {
      localStorage.setItem('levara_token', token)
    } else {
      localStorage.removeItem('levara_token')
    }
  }
}

export async function api<T>(path: string, options?: RequestInit): Promise<T> {
  const traceId = crypto.randomUUID()
  const isFormData = options?.body instanceof FormData
  const headers: Record<string, string> = {
    'X-Trace-ID': traceId,
  }
  const token = getAuthToken()
  if (token) {
    headers['Authorization'] = `Bearer ${token}`
  }
  // Don't set Content-Type for FormData — browser sets it with boundary
  if (!isFormData) {
    headers['Content-Type'] = 'application/json'
  }
  const res = await fetch(`${API_BASE}${path}`, {
    credentials: 'include',
    ...options,
    headers: {
      ...headers,
      ...options?.headers,
    },
  })
  return handleResponse<T>(res, path)
}

export const levara = {
  // Health
  health: () => api<{ status: string }>('/health'),
  healthDetails: () => api<HealthDetails>('/health/details'),
  info: () => api<{ dimension: number; shards: number; status: string }>('/api/v1/info'),

  // Auth
  login: (email: string, password: string) =>
    api<{ access_token: string; token_type: string }>('/api/v1/auth/login', {
      method: 'POST',
      body: JSON.stringify({ email, password }),
    }).then((res) => {
      setAuthToken(res.access_token)
      return res
    }),
  register: (email: string, password: string, username?: string) =>
    api<{ access_token: string; token_type: string }>('/api/v1/auth/register', {
      method: 'POST',
      body: JSON.stringify({ email, password, username }),
    }).then((res) => {
      setAuthToken(res.access_token)
      return res
    }),
  me: () => api<{ id: string; email: string; username: string }>('/api/v1/auth/me'),

  // Datasets
  datasets: async (page = 1, limit = 20) => {
    const res = await api<Dataset[] | { data: Dataset[]; pagination: Pagination }>(`/api/v1/datasets?page=${page}&limit=${limit}`)
    // Backend returns plain array or {data, pagination} depending on version
    if (Array.isArray(res)) return { data: res, pagination: { page, limit, total: res.length, total_pages: 1 } }
    return res
  },
  createDataset: (name: string) =>
    api<Dataset>('/api/v1/datasets', { method: 'POST', body: JSON.stringify({ name }) }),
  deleteDataset: (id: string) =>
    api<void>(`/api/v1/datasets/${id}`, { method: 'DELETE' }),

  // Upload
  upload: (files: File[], datasetName?: string) => {
    const form = new FormData()
    files.forEach((f) => form.append('data', f))
    if (datasetName) form.append('datasetName', datasetName)
    return api<{ status: string; items: unknown[]; dataset_id: string }>('/api/v1/add', {
      method: 'POST',
      body: form,
      headers: {}, // let browser set Content-Type for FormData
    })
  },

  // Search
  search: async (params: SearchRequest) => {
    const res = await api<SearchResult[] | Record<string, unknown> | null>('/api/v1/search/text', {
      method: 'POST',
      body: JSON.stringify(params),
    })
    if (res === null || res === undefined) return []
    if (Array.isArray(res)) return res
    return res // RAG/Graph returns {answer, chunks, ...}
  },

  // Collections
  collections: () => api<CollectionMeta[]>('/api/v1/collections'),

  // Memories
  memories: (type?: string) =>
    api<Memory[]>(`/api/v1/memories${type ? `?type=${type}` : ''}`),
  saveMemory: (key: string, value: string, type?: string) =>
    api<Memory>('/api/v1/memories', {
      method: 'POST',
      body: JSON.stringify({ key, value, type }),
    }),

  // Cognify
  cognify: (params: { texts?: string[]; dataset_id?: string; datasets?: string[]; collection?: string; mode?: string }) => {
    // Backend expects datasets[] array, not dataset_id string
    const body: Record<string, unknown> = { ...params }
    if (params.dataset_id && !params.datasets) {
      body.datasets = [params.dataset_id]
      delete body.dataset_id
    }
    return api<{ status: string; pipeline_run_id: string }>('/api/v1/cognify', {
      method: 'POST',
      body: JSON.stringify(body),
    })
  },
  cognifyStatus: (runId: string) =>
    api<CognifyStatus>(`/api/v1/cognify/${runId}/status`),

  // Feedback
  submitFeedback: (params: { query: string; result_id?: string; rating: number; comment?: string; search_type?: string }) =>
    api<{ id: string }>('/api/v1/feedback', {
      method: 'POST',
      body: JSON.stringify(params),
    }),
  feedbackStats: () => api<{ total: number; avg_rating: number; worst_query?: string }>('/api/v1/feedback/stats'),

  // Settings
  getSettings: () => api<Settings>('/api/v1/settings'),
  updateSettings: (data: Partial<Settings>) =>
    api<void>('/api/v1/settings', { method: 'PUT', body: JSON.stringify(data) }),

  // Dataset data rows (paginated) + record delete + graph (T7)
  getDatasetData: (id: string, page = 1, limit = 20) =>
    api<DatasetDataResponse | DatasetDataRow[]>(
      `/api/v1/datasets/${id}/data?page=${page}&limit=${limit}`,
    ),
  deleteDatasetRecord: (datasetId: string, recordId: string) =>
    api<void>(`/api/v1/datasets/${datasetId}/data/${recordId}`, { method: 'DELETE' }),
  getDatasetGraph: (id: string) =>
    api<DatasetGraph>(`/api/v1/datasets/${id}/graph`),
  graphPath: (params: GraphPathRequest) => {
    const q = new URLSearchParams({
      from: params.from,
      to: params.to,
      max_hops: String(params.max_hops ?? 4),
      limit: String(params.limit ?? 100),
    })
    if (params.as_of) q.set('as_of', String(params.as_of))
    if (params.cursor) q.set('cursor', params.cursor)
    return api<GraphPathResult>(`/api/v1/graph/path?${q.toString()}`)
  },

  // VSA graph-memory index
  vsaStatus: () => api<VSAStatus>('/api/v1/vsa/status'),
  rebuildVSA: (params: VSARebuildRequest) =>
    api<VSARebuildResponse>('/api/v1/vsa/rebuild', {
      method: 'POST',
      body: JSON.stringify(params),
    }),
  queryVSA: (params: VSAQueryRequest) => {
    const q = new URLSearchParams({
      source_id: params.source_id,
      predicate: params.predicate,
      top_k: String(params.top_k ?? 5),
    })
    if (params.dataset_id) q.set('dataset_id', params.dataset_id)
    return api<VSAQueryResponse>(`/api/v1/vsa/query?${q.toString()}`)
  },

  // Workspace indexing and operations
  workspaceOpsStatus: (params: WorkspaceScope) => {
    const q = workspaceScopeParams(params)
    return api<WorkspaceOpsStatus>(`/api/v1/workspace/ops/status?${q.toString()}`)
  },
  workspaceManifest: (params: WorkspaceScope) => {
    const q = workspaceScopeParams(params)
    return api<WorkspaceManifestResponse>(`/api/v1/workspace/manifest?${q.toString()}`)
  },
  workspaceJobs: (params: WorkspaceScope & { status?: string }) => {
    const q = workspaceScopeParams(params)
    if (params.status) q.set('status', params.status)
    return api<WorkspaceJobsResponse>(`/api/v1/workspace/jobs?${q.toString()}`)
  },
  workspaceArtifacts: (params: WorkspaceArtifactsRequest) => {
    const q = workspaceScopeParams(params)
    if (params.kind) q.set('kind', params.kind)
    if (params.index_only) q.set('index_only', 'true')
    return api<WorkspaceArtifactsResponse>(`/api/v1/workspace/context/artifacts?${q.toString()}`)
  },
  workspaceConflicts: (params: WorkspaceScope) => {
    const q = workspaceScopeParams(params)
    return api<WorkspaceConflictsResponse>(`/api/v1/workspace/conflicts?${q.toString()}`)
  },
  workspaceAudit: (params: WorkspaceAuditRequest) => {
    const q = workspaceScopeParams(params)
    if (params.operation) q.set('operation', params.operation)
    if (params.result) q.set('result', params.result)
    if (params.limit) q.set('limit', String(params.limit))
    return api<WorkspaceAuditResponse>(`/api/v1/workspace/audit?${q.toString()}`)
  },
  workspaceRead: (params: WorkspaceReadRequest) => {
    const q = workspaceScopeParams(params)
    q.set('path', params.path)
    return api<WorkspaceReadResponse>(`/api/v1/workspace/read?${q.toString()}`)
  },
  workspaceSearch: (params: WorkspaceSearchRequest) =>
    api<WorkspaceSearchResponse>('/api/v1/workspace/search', {
      method: 'POST',
      body: JSON.stringify(params),
    }),
  workspaceIndex: (params: WorkspaceIndexRequest) =>
    api<WorkspaceIndexResponse>('/api/v1/workspace/index', {
      method: 'POST',
      body: JSON.stringify(params),
    }),
  workspaceWrite: (params: WorkspaceWriteRequest) =>
    api<WorkspaceWriteResponse>('/api/v1/workspace/write', {
      method: 'POST',
      body: JSON.stringify(params),
    }),
  workspaceReindex: (params: WorkspaceReindexRequest) =>
    api<WorkspaceReindexResponse>('/api/v1/workspace/reindex', {
      method: 'POST',
      body: JSON.stringify(params),
    }),
  workspaceRetryJob: (params: WorkspaceRetryJobRequest) =>
    api<WorkspaceRetryJobResponse>('/api/v1/workspace/jobs/retry', {
      method: 'POST',
      body: JSON.stringify(params),
    }),

  // Cross-instance sync
  syncManifest: () => api<SyncManifest>('/api/v1/sync/manifest'),
  syncStatus: (limit = 10) => api<SyncStatus>(`/api/v1/sync/status?limit=${limit}`),
  runSync: (params: SyncRunRequest) =>
    api<SyncRunResponse>('/api/v1/sync/run', {
      method: 'POST',
      body: JSON.stringify(params),
    }),

  // MCP/Admin observability
  mcpTools: () => api<MCPToolsResponse>('/api/v1/admin/mcp/tools'),
  mcpSummary: () => api<MCPAdminSummary>('/api/v1/admin/mcp/summary'),
  mcpSessions: (limit = 20) => api<MCPSessionsResponse>(`/api/v1/admin/mcp/sessions?limit=${limit}`),
}

function workspaceScopeParams(params: WorkspaceScope) {
  const q = new URLSearchParams()
  if (params.project_id) q.set('project_id', params.project_id)
  if (params.branch) q.set('branch', params.branch)
  return q
}

// Types
export interface Dataset {
  id: string
  name: string
  record_count: number
  created_at: string
  updated_at: string
}

export interface Pagination {
  page: number
  limit: number
  total: number
  total_pages: number
}

export interface SearchRequest {
  query_text: string
  query_type?: string
  top_k?: number
  collection?: string
  domain?: string
  tags?: string[]
  /**
   * Cross-encoder rerank, tri-state. Phase 2 (2026-05-14):
   *   omit       — server default (on iff RERANK_ENDPOINT is configured)
   *   true       — force on (still requires endpoint)
   *   false      — explicit opt-out
   * Don't expose this as a default UI toggle — rerank should be on by
   * default whenever the server has a reranker; surface it only in an
   * Advanced section for debugging.
   */
  rerank?: boolean
  session_id?: string
}

export interface SearchResult {
  id: string
  score: number
  fused_score?: number
  vector_score?: number
  bm25_score?: number
  collection: string
  metadata: Record<string, unknown>
  reranked?: boolean
}

export interface CollectionMeta {
  name: string
  embedding_model: string
  embedding_dim: number
  distance_metric: string
  domain?: string
  record_count: number
  created_at: string
}

export interface Memory {
  id?: string
  key: string
  value: string
  type?: string
  created_at?: string
}

export interface CognifyStatus {
  status: string
  stage?: string
  message?: string
  chunks?: number
  entities?: number
  edges?: number
  elapsed_ms?: number
}

export type Theme = 'light' | 'dark' | 'system'
export type Locale = 'ru' | 'en'

export interface Settings {
  theme?: Theme
  locale?: Locale
  // Backend may return additional user-specific settings; we keep the
  // shape open so new keys don't require a client upgrade in lockstep.
  [key: string]: unknown
}

// Data records inside a dataset — returned by GET /datasets/:id/data.
// Backend currently returns either a plain array (no pagination) or a
// {data, pagination} envelope, so the client normalises both shapes.
export interface DatasetDataRow {
  id: string
  name?: string
  extension?: string
  mime_type?: string
  raw_data_location?: string
  data_size?: number
  pipeline_status?: string
  tags?: string
  created_at?: string
  [key: string]: unknown
}

export interface DatasetDataResponse {
  data: DatasetDataRow[]
  pagination?: Pagination
}

// Knowledge-graph view for a dataset — returned by GET /datasets/:id/graph.
export interface GraphNode {
  id: string
  name: string
  type: string
  properties?: Record<string, unknown>
}

export interface GraphEdge {
  id?: string
  source: string
  target: string
  label: string
  valid_from?: number
  valid_until?: number | null
  properties?: Record<string, unknown>
}

export interface DatasetGraph {
  nodes: GraphNode[]
  edges: GraphEdge[]
}

export interface GraphPathRequest {
  from: string
  to: string
  max_hops?: number
  as_of?: number
  limit?: number
  cursor?: string
}

export interface GraphPathEdge {
  source_id: string
  target_id: string
  type: string
  valid_from: number
  valid_until?: number | null
  properties?: Record<string, unknown>
}

export interface GraphPathResult {
  edges: GraphPathEdge[]
  next_cursor?: string
  as_of: number
}

export interface VSAStatus {
  available: boolean
  reason?: string
  datasets?: string[]
  predicates?: string[]
  shard_count?: number
  member_count?: number
  fact_count?: number
  max_dim?: number
  last_updated_at?: string
}

export interface VSARebuildRequest {
  dataset_id?: string
  dim?: number
  shard_size?: number
}

export interface VSARebuildResponse {
  status: string
  dataset_id?: string
}

export interface VSAQueryRequest {
  dataset_id?: string
  source_id: string
  predicate: string
  top_k?: number
}

export interface VSACandidate {
  target_id: string
  target_name?: string
  edge_id: string
  predicate: string
  dataset_id: string
  shard_id: string
  similarity: number
}

export interface VSAQueryResponse {
  candidates: VSACandidate[]
}

export interface HealthDetails {
  services?: Record<string, DependencyHealth>
}

export interface WorkspaceScope {
  project_id?: string
  branch?: string
}

export interface WorkspaceOpsStatus {
  generated_at: string
  project_id?: string
  branch?: string
  watcher?: Record<string, unknown>
  jobs?: {
    total?: number
    by_status?: Record<string, number>
    dead_letter_count?: number
    max_lag_seconds?: number
    oldest_pending_at?: string
    newest_updated_at?: string
  }
  audit?: {
    total_events?: number
    files?: number
    by_source?: Record<string, number>
    by_result?: Record<string, number>
    last_event_at?: string
  }
}

export interface WorkspaceManifestResponse {
  project_id: string
  branch: string
  manifest_path: string
  active_generation?: string
  chunks_count?: number
  generations?: unknown[]
  chunks?: unknown[]
}

export interface WorkspaceJob {
  id: string
  status: string
  attempts?: number
  created_at?: string
  updated_at?: string
  last_error?: string
  request?: Record<string, unknown>
}

export interface WorkspaceJobsResponse {
  project_id: string
  branch: string
  total: number
  by_status?: Record<string, number>
  jobs: WorkspaceJob[]
}

export interface WorkspaceIndexRequest {
  project_id: string
  branch?: string
  generation: string
  collection?: string
  path: string
  text: string
  title?: string
  room?: string
  tags?: string[]
  activate_generation?: boolean
}

export interface WorkspaceWriteRequest extends WorkspaceIndexRequest {
  index?: boolean
  expected_file_digest?: string
}

export interface WorkspaceWriteResponse {
  project_id: string
  branch: string
  path: string
  bytes: number
  indexed?: WorkspaceIndexResponse
}

export interface WorkspaceReadRequest extends WorkspaceScope {
  project_id: string
  path: string
}

export interface WorkspaceSourceCitation {
  project_id?: string
  branch?: string
  path?: string
  generation?: string
  collection?: string
  chunk_id?: string
  source_uri?: string
  read_tool?: string
  read_args?: Record<string, unknown>
  [key: string]: unknown
}

export interface WorkspaceReadResponse {
  project_id: string
  branch: string
  path: string
  text: string
  citation?: WorkspaceSourceCitation
  citations?: WorkspaceSourceCitation[]
  chunks?: unknown[]
}

export interface WorkspaceSearchRequest extends WorkspaceScope {
  project_id: string
  generation?: string
  collection?: string
  search_query: string
  search_type?: string
  top_k?: number
  mode?: string
  room?: string
  tags?: string[]
  rerank?: boolean
  parent_child?: boolean
  multi_query?: boolean
  dedup?: boolean
  graph_rerank?: boolean
}

export interface WorkspaceSearchFreshness {
  stale?: boolean
  potentially_stale?: boolean
  reason?: string
  active_generation?: string
  resolved_generation?: string
  active_chunk_count?: number
  active_path_count?: number
  watcher_enabled?: boolean
  watcher_branch_pending?: boolean
}

export interface WorkspaceSearchResponse {
  project_id: string
  branch: string
  manifest_path: string
  active_generation?: string
  generation?: string
  collection?: string
  freshness?: WorkspaceSearchFreshness
  exact_read_required?: boolean
  exact_read_tool?: string
  results?: Array<Record<string, unknown>>
  generic_search_status?: string
  search_message?: string
}

export interface WorkspaceArtifactsRequest extends WorkspaceScope {
  kind?: string
  index_only?: boolean
}

export interface WorkspaceArtifact {
  id: string
  kind?: string
  title?: string
  path?: string
  project_id?: string
  branch?: string
  index?: Record<string, unknown>
  metadata?: Record<string, unknown>
  [key: string]: unknown
}

export interface WorkspaceArtifactsResponse {
  version: number
  path: string
  artifacts: WorkspaceArtifact[]
  total: number
}

export interface WorkspaceConflictPath {
  path: string
  state: string
  file_digest?: string
  indexed_digest?: string
  indexed_at?: string
  detail?: string
}

export interface WorkspaceConflictsResponse {
  project_id: string
  branch: string
  active_generation?: string
  manifest_path?: string
  has_conflicts: boolean
  policy: string
  dirty_paths?: WorkspaceConflictPath[]
  unindexed_paths?: WorkspaceConflictPath[]
  missing_indexed_paths?: WorkspaceConflictPath[]
  jobs_by_status?: Record<string, number>
  recommended_actions?: string[]
}

export interface WorkspaceAuditRequest extends WorkspaceScope {
  operation?: string
  result?: string
  limit?: number
}

export interface WorkspaceAuditEvent {
  id: string
  at: string
  source: string
  operation: string
  project_id: string
  branch?: string
  result: string
  status?: number
  error?: string
  metadata?: Record<string, unknown>
}

export interface WorkspaceAuditResponse {
  project_id: string
  branch?: string
  events: WorkspaceAuditEvent[]
  total: number
  limit: number
}

export interface WorkspaceRetryJobRequest extends WorkspaceScope {
  project_id: string
  job_id: string
}

export interface WorkspaceRetryJobResponse {
  job: WorkspaceJob
  result?: unknown
}

export interface WorkspaceIndexResponse {
  project_id: string
  branch: string
  manifest_path: string
  active_generation?: string
  result?: Record<string, unknown>
}

export interface WorkspaceReindexRequest {
  project_id: string
  branch?: string
  generation: string
  collection?: string
  paths: string[]
  room?: string
  tags?: string[]
  activate_generation?: boolean
}

export interface WorkspaceReindexResponse {
  project_id: string
  branch: string
  manifest_path: string
  active_generation?: string
  results?: unknown[]
}

export interface DependencyHealth {
  status?: string
  error?: string
  endpoint?: string
  url?: string
  model?: string
  port?: number
  count?: number
  dimension?: number
  [key: string]: unknown
}

export interface SyncCount {
  count: number
  latest_updated?: string
}

export interface SyncCollectionInfo {
  name: string
  records: number
  dim: number
  model: string
}

export interface SyncManifest {
  version?: string
  embed_model?: string
  embed_dim?: number
  memories?: SyncCount
  interactions?: SyncCount
  graph_nodes?: SyncCount
  graph_edges?: SyncCount
  collections?: SyncCollectionInfo[]
}

export interface SyncDirectionStatus {
  count: number
  last_at?: string
  last_remote?: string
}

export interface SyncEvent {
  id: string
  direction: string
  remote: string
  types?: string[]
  at: string
}

export interface SyncStatus {
  by_direction?: Record<string, SyncDirectionStatus>
  events?: SyncEvent[]
  error?: string
}

export interface SyncRunRequest {
  remote_url: string
  direction: 'pull' | 'push'
  types?: string[]
  since?: string
  collections?: string[]
}

export interface SyncRunResponse {
  remote_manifest?: SyncManifest | Record<string, unknown>
  version_warning?: string
  collections_sync?: unknown
  [key: string]: unknown
}

export interface MCPToolInfo {
  name: string
  description?: string
  group?: string
  status?: string
  input_schema?: Record<string, unknown>
}

export interface MCPToolsResponse {
  tools: MCPToolInfo[]
  total: number
}

export interface MCPSessionSummary {
  session_id: string
  count: number
  last_at?: string
  search_type?: string
}

export interface MCPAdminSummary {
  tools_total: number
  tools_by_group?: Record<string, number>
  tools_by_status?: Record<string, number>
  recent_sessions?: MCPSessionSummary[]
  pinned_memories?: number
  memory_metadata_warnings?: number
  audit_enabled?: boolean
}

export interface MCPSessionsResponse {
  sessions: MCPSessionSummary[]
  total: number
}
