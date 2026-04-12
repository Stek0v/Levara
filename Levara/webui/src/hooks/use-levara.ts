'use client'

import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { levara, type Dataset, type CollectionMeta, type Memory, type SearchRequest } from '@/lib/api'

// ── Query Keys (single source of truth) ──

export const queryKeys = {
  health: ['health'] as const,
  info: ['info'] as const,
  datasets: ['datasets'] as const,
  dataset: (id: string) => ['dataset', id] as const,
  collections: ['collections'] as const,
  memories: (type?: string) => ['memories', type] as const,
  feedbackStats: ['feedbackStats'] as const,
  cacheStats: ['cacheStats'] as const,
  errors: ['errors'] as const,
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
