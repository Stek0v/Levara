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

async function handleResponse<T>(res: Response): Promise<T> {
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
  return text ? JSON.parse(text) : ({} as T)
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
  return handleResponse<T>(res)
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
  datasets: (page = 1, limit = 20) =>
    api<{ data: Dataset[]; pagination: Pagination }>(`/api/v1/datasets?page=${page}&limit=${limit}`),
  createDataset: (name: string) =>
    api<Dataset>('/api/v1/datasets', { method: 'POST', body: JSON.stringify({ name }) }),
  deleteDataset: (id: string) =>
    api<void>(`/api/v1/datasets/${id}`, { method: 'DELETE' }),

  // Upload
  upload: (files: File[], datasetName?: string) => {
    const form = new FormData()
    files.forEach((f) => form.append('data', f))
    if (datasetName) form.append('dataset_name', datasetName)
    return api<{ status: string; items: unknown[]; dataset_id: string }>('/api/v1/add', {
      method: 'POST',
      body: form,
      headers: {}, // let browser set Content-Type for FormData
    })
  },

  // Search
  search: (params: SearchRequest) =>
    api<SearchResult[]>('/api/v1/search/text', {
      method: 'POST',
      body: JSON.stringify(params),
    }),

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
  cognify: (params: { texts?: string[]; dataset_id?: string; collection?: string; mode?: string }) =>
    api<{ status: string; pipeline_run_id: string }>('/api/v1/cognify', {
      method: 'POST',
      body: JSON.stringify(params),
    }),
  cognifyStatus: (runId: string) =>
    api<CognifyStatus>(`/api/v1/cognify/${runId}/status`),

  // Feedback
  submitFeedback: (params: { query: string; result_id?: string; rating: number; comment?: string; search_type?: string }) =>
    api<{ id: string }>('/api/v1/feedback', {
      method: 'POST',
      body: JSON.stringify(params),
    }),
  feedbackStats: () => api<{ total: number; avg_rating: number }>('/api/v1/feedback/stats'),

  // Settings
  settings: () => api<Record<string, unknown>>('/api/v1/settings'),
  updateSettings: (data: Record<string, unknown>) =>
    api<void>('/api/v1/settings', { method: 'PUT', body: JSON.stringify(data) }),
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
