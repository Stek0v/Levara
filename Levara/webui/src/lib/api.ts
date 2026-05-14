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

export async function api<T>(path: string, options?: RequestInit): Promise<T> {
  const traceId = crypto.randomUUID()
  const isFormData = options?.body instanceof FormData
  const headers: Record<string, string> = {
    'X-Trace-ID': traceId,
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
  healthDetails: () => api<Record<string, unknown>>('/health/details'),
  info: () => api<{ dimension: number; shards: number; status: string }>('/api/v1/info'),

  // Auth
  login: (email: string, password: string) =>
    api<{ token: string }>('/api/v1/auth/login', {
      method: 'POST',
      body: JSON.stringify({ email, password }),
    }),
  register: (email: string, password: string, username?: string) =>
    api<{ token: string }>('/api/v1/auth/register', {
      method: 'POST',
      body: JSON.stringify({ email, password, username }),
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
  source: string
  target: string
  label: string
}

export interface DatasetGraph {
  nodes: GraphNode[]
  edges: GraphEdge[]
}
