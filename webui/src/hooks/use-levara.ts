'use client'

import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  levara,
  type SearchRequest,
  type Settings,
  type DatasetDataRow,
  type DatasetDataResponse,
  type DatasetGraph,
  type EmbeddingMigrationCutoverRequest,
  type EmbeddingMigrationRequest,
  type EmbeddingShadowReadRequest,
  type GraphPathRequest,
  type AgentTrajectoriesRequest,
  type MemoryScaffoldProposalRequest,
  type MemoryBehaviorRequest,
  type VSAQueryRequest,
  type WorkspaceArtifactsRequest,
  type WorkspaceAuditRequest,
  type WorkspaceIndexRequest,
  type WorkspaceReadRequest,
  type WorkspaceReindexRequest,
  type WorkspaceRetryJobRequest,
  type WorkspaceScope,
  type WorkspaceSearchRequest,
  type WorkspaceWriteRequest,
  type SyncRunRequest,
} from '@/lib/api'

// ── Query Keys (single source of truth) ──

export const queryKeys = {
  health: ['health'] as const,
  info: ['info'] as const,
  datasets: ['datasets'] as const,
  dataset: (id: string) => ['dataset', id] as const,
  collections: ['collections'] as const,
  memories: (type?: string) => ['memories', type] as const,
  feedbackStats: ['feedbackStats'] as const,
  memoryBehavior: (params?: MemoryBehaviorRequest) => ['memoryBehavior', params] as const,
  agentTrajectories: (params?: AgentTrajectoriesRequest) => ['agentTrajectories', params] as const,
  memoryScaffoldProposals: (params?: MemoryScaffoldProposalRequest) => ['memoryScaffoldProposals', params] as const,
  memoryScaffoldProposal: (id: string) => ['memoryScaffoldProposal', id] as const,
  cacheStats: ['cacheStats'] as const,
  errors: ['errors'] as const,
  settings: ['settings'] as const,
  datasetData: (id: string, page: number) => ['datasetData', id, page] as const,
  datasetGraph: (id: string) => ['datasetGraph', id] as const,
}

// ── Queries ──

export function useHealth() {
  return useQuery({
    queryKey: queryKeys.health,
    queryFn: () => levara.health(),
    staleTime: 10_000,
    refetchInterval: 30_000,
  })
}

export function useInfo() {
  return useQuery({
    queryKey: queryKeys.info,
    queryFn: () => levara.info(),
    staleTime: 60_000,
  })
}

export function useDatasets() {
  return useQuery({
    queryKey: queryKeys.datasets,
    queryFn: () => levara.datasets(),
    staleTime: 5_000,
  })
}

export function useCollections() {
  return useQuery({
    queryKey: queryKeys.collections,
    queryFn: () => levara.collections(),
    staleTime: 10_000,
  })
}

export function useMemories(type?: string) {
  return useQuery({
    queryKey: queryKeys.memories(type),
    queryFn: () => levara.memories(type === 'all' ? undefined : type),
    staleTime: 5_000,
  })
}

export function useFeedbackStats() {
  return useQuery({
    queryKey: queryKeys.feedbackStats,
    queryFn: () => levara.feedbackStats(),
    staleTime: 30_000,
  })
}

export function useMemoryBehavior(params?: MemoryBehaviorRequest) {
  return useQuery({
    queryKey: queryKeys.memoryBehavior(params),
    queryFn: () => levara.memoryBehavior(params),
    staleTime: 15_000,
    refetchInterval: 30_000,
  })
}

export function useAgentTrajectories(params?: AgentTrajectoriesRequest) {
  return useQuery({
    queryKey: queryKeys.agentTrajectories(params),
    queryFn: () => levara.agentTrajectories(params),
    staleTime: 15_000,
    refetchInterval: 30_000,
  })
}

export function useMemoryScaffoldProposals(params?: MemoryScaffoldProposalRequest) {
  return useQuery({
    queryKey: queryKeys.memoryScaffoldProposals(params),
    queryFn: () => levara.memoryScaffoldProposals(params),
    staleTime: 15_000,
  })
}

export function useMemoryScaffoldProposal(id?: string) {
  return useQuery({
    queryKey: queryKeys.memoryScaffoldProposal(id || ''),
    queryFn: () => levara.memoryScaffoldProposal(id || ''),
    enabled: Boolean(id),
    staleTime: 15_000,
  })
}

export function useDecideMemoryScaffoldProposal() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ id, status, note }: { id: string; status: 'approved' | 'rejected'; note?: string }) =>
      levara.decideMemoryScaffoldProposal(id, status, note),
    onSuccess: (proposal) => {
      qc.invalidateQueries({ queryKey: ['memoryScaffoldProposals'] })
      qc.invalidateQueries({ queryKey: queryKeys.memoryScaffoldProposal(proposal.id) })
    },
  })
}

export function useCacheStats() {
  return useQuery({
    queryKey: queryKeys.cacheStats,
    queryFn: async () => {
      const res = await fetch('/api/v1/cache/stats', { credentials: 'include' })
      const c = await res.json() as Record<string, unknown>
      return {
        size: (c.size ?? c.Size ?? 0) as number,
        max_size: (c.max_size ?? c.MaxSize ?? 0) as number,
        hits: (c.hits ?? c.Hits ?? 0) as number,
        misses: (c.misses ?? c.Misses ?? 0) as number,
        hit_rate: (c.hit_rate ?? c.HitRate ?? 0) as number,
      }
    },
    staleTime: 10_000,
    refetchInterval: 30_000,
  })
}

export function useErrors() {
  return useQuery({
    queryKey: queryKeys.errors,
    queryFn: async () => {
      const res = await fetch('/api/v1/errors?limit=10', { credentials: 'include' })
      const data = await res.json()
      return Array.isArray(data) ? data as { message: string; timestamp: string }[] : []
    },
    staleTime: 10_000,
    refetchInterval: 30_000,
  })
}

// ── Mutations ──

export function useCreateDataset() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (name: string) => levara.createDataset(name),
    onSuccess: () => { qc.invalidateQueries({ queryKey: queryKeys.datasets }) },
  })
}

export function useDeleteDataset() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (id: string) => levara.deleteDataset(id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: queryKeys.datasets }) },
  })
}

export function useUpload() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ files, datasetName }: { files: File[]; datasetName?: string }) =>
      levara.upload(files, datasetName),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: queryKeys.datasets })
      qc.invalidateQueries({ queryKey: queryKeys.collections })
    },
  })
}

export function useCognify() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (params: Parameters<typeof levara.cognify>[0]) => levara.cognify(params),
    onSuccess: () => {
      // Cognify is async — collections update later
      setTimeout(() => {
        qc.invalidateQueries({ queryKey: queryKeys.collections })
        qc.invalidateQueries({ queryKey: queryKeys.datasets })
      }, 5000)
    },
  })
}

export function useSaveMemory() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (params: { key: string; value: string; type?: string }) =>
      levara.saveMemory(params.key, params.value, params.type),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['memories'] }) },
  })
}

export function useSubmitFeedback() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (params: Parameters<typeof levara.submitFeedback>[0]) => levara.submitFeedback(params),
    onSuccess: () => { qc.invalidateQueries({ queryKey: queryKeys.feedbackStats }) },
  })
}

export function useSearch() {
  return useMutation({
    mutationFn: (params: SearchRequest) => levara.search(params),
  })
}

// ── Settings (T9) ──
//
// useSettings hydrates from the backend once per session (staleTime=Infinity
// — settings rarely change out-of-band, and we refresh on mutation via
// invalidateQueries below). useUpdateSettings applies an optimistic cache
// patch so toggles feel instant; the onError rollback restores the prior
// value if the backend rejects the PUT. onSettled forces a refetch so the
// cache matches the server's view of merged settings.
export function useSettings() {
  return useQuery({
    queryKey: queryKeys.settings,
    queryFn: () => levara.getSettings(),
    staleTime: Infinity,
  })
}

export function useUpdateSettings() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (patch: Partial<Settings>) => levara.updateSettings(patch),
    onMutate: async (patch) => {
      await qc.cancelQueries({ queryKey: queryKeys.settings })
      const prev = qc.getQueryData<Settings>(queryKeys.settings) ?? {}
      qc.setQueryData<Settings>(queryKeys.settings, { ...prev, ...patch })
      return { prev }
    },
    onError: (_err, _patch, ctx) => {
      if (ctx?.prev) qc.setQueryData(queryKeys.settings, ctx.prev)
    },
    onSettled: () => {
      qc.invalidateQueries({ queryKey: queryKeys.settings })
    },
  })
}

// ── Dataset data + graph (T7) ──
//
// useDatasetData normalises the two response shapes (plain array vs
// {data, pagination}) into a single {rows, total} view so the page
// component doesn't need to branch on shape. useDatasetGraph returns the
// raw {nodes, edges} payload. useDeleteDatasetRecord invalidates ALL
// datasetData pages for this dataset via a predicate — otherwise a
// delete from page 2 leaves the now-stale page 1 in cache.
export function useDatasetData(datasetId: string, page = 1, limit = 20) {
  return useQuery({
    queryKey: queryKeys.datasetData(datasetId, page),
    queryFn: async () => {
      const res = await levara.getDatasetData(datasetId, page, limit)
      if (Array.isArray(res)) {
        return { rows: res as DatasetDataRow[], total: res.length }
      }
      const env = res as DatasetDataResponse
      return {
        rows: env.data ?? [],
        total: env.pagination?.total ?? env.data?.length ?? 0,
      }
    },
    staleTime: 5_000,
    enabled: !!datasetId,
  })
}

export function useDatasetGraph(datasetId: string) {
  return useQuery({
    queryKey: queryKeys.datasetGraph(datasetId),
    queryFn: () => levara.getDatasetGraph(datasetId),
    staleTime: 10_000,
    enabled: !!datasetId,
  })
}

export function useDeleteDatasetRecord() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ datasetId, recordId }: { datasetId: string; recordId: string }) =>
      levara.deleteDatasetRecord(datasetId, recordId),
    onSuccess: (_res, { datasetId }) => {
      // Invalidate every page of this dataset's data + its graph view —
      // a deleted record could change pagination and graph structure.
      qc.invalidateQueries({
        predicate: (q) =>
          q.queryKey[0] === 'datasetData' && q.queryKey[1] === datasetId,
      })
      qc.invalidateQueries({ queryKey: queryKeys.datasetGraph(datasetId) })
    },
  })
}

// Re-export the DatasetGraph type so consumers can annotate props without
// pulling it directly from @/lib/api.
export type { DatasetGraph }

// ── VSA & Health (analytics) ──

export function useHealthDetails() {
  return useQuery({
    queryKey: ['healthDetails'],
    queryFn: () => levara.healthDetails(),
    staleTime: 10_000,
    refetchInterval: 30_000,
  })
}

export function useVSAStatus() {
  return useQuery({
    queryKey: ['vsaStatus'],
    queryFn: () => levara.vsaStatus(),
    staleTime: 10_000,
    refetchInterval: 30_000,
  })
}

export function useVSARebuild() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (params?: { dataset_id?: string; dim?: number; shard_size?: number }) => levara.rebuildVSA(params ?? {}),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['vsaStatus'] })
    },
  })
}

export function useVSAQuery() {
  return useMutation({
    mutationFn: (params: VSAQueryRequest) => levara.queryVSA(params),
  })
}

// ── Embedding migrations ──

export function useEmbeddingDualWriteRules() {
  return useQuery({
    queryKey: ['embeddingDualWriteRules'],
    queryFn: () => levara.embeddingDualWriteRules(),
    staleTime: 10_000,
    refetchInterval: 30_000,
  })
}

export function useEmbeddingMigrationStatus(runId: string) {
  return useQuery({
    queryKey: ['embeddingMigrationStatus', runId],
    queryFn: () => levara.embeddingMigrationStatus(runId),
    enabled: Boolean(runId),
    staleTime: 5_000,
    refetchInterval: (query) => {
      const status = query.state.data?.status
      return status === 'RUNNING' || status === 'PENDING' ? 3_000 : false
    },
  })
}

export function useStartEmbeddingMigration() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (params: EmbeddingMigrationRequest) => levara.startEmbeddingMigration(params),
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ['embeddingDualWriteRules'] })
      qc.invalidateQueries({ queryKey: ['collections'] })
      if (data.run_id) qc.invalidateQueries({ queryKey: ['embeddingMigrationStatus', data.run_id] })
    },
  })
}

export function useRetryEmbeddingMigration() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (runId: string) => levara.retryEmbeddingMigration(runId),
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ['embeddingDualWriteRules'] })
      if (data.run_id) qc.invalidateQueries({ queryKey: ['embeddingMigrationStatus', data.run_id] })
    },
  })
}

export function useCutoverEmbeddingMigration() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: ({ runId, params }: { runId: string; params: EmbeddingMigrationCutoverRequest }) =>
      levara.cutoverEmbeddingMigration(runId, params),
    onSuccess: (data) => {
      qc.invalidateQueries({ queryKey: ['collections'] })
      qc.invalidateQueries({ queryKey: ['embeddingDualWriteRules'] })
      qc.invalidateQueries({ queryKey: ['embeddingMigrationStatus', data.run_id] })
    },
  })
}

export function useDisableEmbeddingDualWrite() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (sourceCollection: string) => levara.disableEmbeddingDualWrite(sourceCollection),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['embeddingDualWriteRules'] })
    },
  })
}

export function useEmbeddingShadowRead() {
  return useMutation({
    mutationFn: (params: EmbeddingShadowReadRequest) => levara.embeddingShadowRead(params),
  })
}

// ── Graph path ──

export function useGraphPath() {
  return useMutation({
    mutationFn: (params: GraphPathRequest) => levara.graphPath(params),
  })
}

// ── Workspace ──

export function useWorkspaceOps(params: WorkspaceScope = {}) {
  return useQuery({
    queryKey: ['workspaceOps', params],
    queryFn: () => levara.workspaceOpsStatus(params),
    staleTime: 10_000,
    refetchInterval: 30_000,
  })
}

export function useWorkspaceManifest(params: WorkspaceScope = {}) {
  return useQuery({
    queryKey: ['workspaceManifest', params],
    queryFn: () => levara.workspaceManifest(params),
    enabled: Boolean(params.project_id),
    staleTime: 10_000,
  })
}

export function useWorkspaceJobs(params: WorkspaceScope & { status?: string } = {}) {
  return useQuery({
    queryKey: ['workspaceJobs', params],
    queryFn: () => levara.workspaceJobs(params),
    enabled: Boolean(params.project_id),
    staleTime: 10_000,
  })
}

export function useWorkspaceArtifacts(params: WorkspaceArtifactsRequest = {}) {
  return useQuery({
    queryKey: ['workspaceArtifacts', params],
    queryFn: () => levara.workspaceArtifacts(params),
    enabled: Boolean(params.project_id),
    staleTime: 10_000,
  })
}

export function useWorkspaceConflicts(params: WorkspaceScope = {}) {
  return useQuery({
    queryKey: ['workspaceConflicts', params],
    queryFn: () => levara.workspaceConflicts(params),
    enabled: Boolean(params.project_id),
    staleTime: 10_000,
  })
}

export function useWorkspaceAudit(params: WorkspaceAuditRequest = {}) {
  return useQuery({
    queryKey: ['workspaceAudit', params],
    queryFn: () => levara.workspaceAudit(params),
    enabled: Boolean(params.project_id),
    staleTime: 10_000,
  })
}

export function useWorkspaceIndex() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (params: WorkspaceIndexRequest) => levara.workspaceIndex(params),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['workspaceManifest'] })
      qc.invalidateQueries({ queryKey: ['workspaceOps'] })
      qc.invalidateQueries({ queryKey: ['workspaceConflicts'] })
    },
  })
}

export function useWorkspaceRead() {
  return useMutation({
    mutationFn: (params: WorkspaceReadRequest) => levara.workspaceRead(params),
  })
}

export function useWorkspaceSearch() {
  return useMutation({
    mutationFn: (params: WorkspaceSearchRequest) => levara.workspaceSearch(params),
  })
}

export function useWorkspaceWrite() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (params: WorkspaceWriteRequest) => levara.workspaceWrite(params),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['workspaceManifest'] })
      qc.invalidateQueries({ queryKey: ['workspaceOps'] })
      qc.invalidateQueries({ queryKey: ['workspaceConflicts'] })
      qc.invalidateQueries({ queryKey: ['workspaceAudit'] })
    },
  })
}

export function useWorkspaceReindex() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (params: WorkspaceReindexRequest) => levara.workspaceReindex(params),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['workspaceJobs'] })
      qc.invalidateQueries({ queryKey: ['workspaceManifest'] })
      qc.invalidateQueries({ queryKey: ['workspaceConflicts'] })
    },
  })
}

export function useWorkspaceRetryJob() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (params: WorkspaceRetryJobRequest) => levara.workspaceRetryJob(params),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['workspaceJobs'] })
      qc.invalidateQueries({ queryKey: ['workspaceOps'] })
      qc.invalidateQueries({ queryKey: ['workspaceAudit'] })
    },
  })
}

// ── Sync ──

export function useSyncManifest() {
  return useQuery({
    queryKey: ['syncManifest'],
    queryFn: () => levara.syncManifest(),
    staleTime: 30_000,
    refetchInterval: 60_000,
  })
}

export function useSyncStatus(limit = 10) {
  return useQuery({
    queryKey: ['syncStatus', limit],
    queryFn: () => levara.syncStatus(limit),
    staleTime: 10_000,
    refetchInterval: 30_000,
  })
}

export function useRunSync() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (params: SyncRunRequest) => levara.runSync(params),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['syncStatus'] })
      qc.invalidateQueries({ queryKey: ['syncManifest'] })
    },
  })
}

// ── MCP/Admin ──

export function useMCPTools() {
  return useQuery({
    queryKey: ['mcpTools'],
    queryFn: () => levara.mcpTools(),
    staleTime: 60_000,
  })
}

export function useMCPAdminSummary() {
  return useQuery({
    queryKey: ['mcpAdminSummary'],
    queryFn: () => levara.mcpSummary(),
    staleTime: 10_000,
    refetchInterval: 30_000,
  })
}

export function useMCPSessions(limit = 20) {
  return useQuery({
    queryKey: ['mcpSessions', limit],
    queryFn: () => levara.mcpSessions(limit),
    staleTime: 10_000,
    refetchInterval: 30_000,
  })
}

export function useMCPAnalytics(hours = 24) {
  return useQuery({
    queryKey: ['mcpAnalytics', hours],
    queryFn: () => levara.mcpAnalytics(hours),
    staleTime: 10_000,
    refetchInterval: 30_000,
  })
}

export function useImplicitFeedback() {
  return useQuery({ queryKey: ['implicitFeedback'], queryFn: () => levara.implicitFeedback(), staleTime: 10_000, refetchInterval: 30_000 })
}

export function useMemoryIndexStatus() {
  return useQuery({ queryKey: ['memoryIndexStatus'], queryFn: () => levara.memoryIndexStatus(), staleTime: 5_000, refetchInterval: 15_000 })
}
