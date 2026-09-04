'use client'

import { useState, useEffect, startTransition } from 'react'
import { useParams, useRouter } from 'next/navigation'
import { levara } from '@/lib/api'
import { useCognifyProgress } from '@/hooks/use-sse'
import {
  useDatasets,
  useDatasetData,
  useDeleteDatasetRecord,
} from '@/hooks/use-levara'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { EmptyState } from '@/components/ui/empty-state'
import { ArrowLeft, Play, Trash2, FileText, ChevronLeft, ChevronRight } from 'lucide-react'
import { useT, formatBytes, formatDate, formatCount } from '@/lib/i18n'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { ProjectContextItem, ProjectActivityItem, GitCommit } from '@/lib/api'
import { useSettings } from '@/hooks/use-levara'

function formatSize(bytes?: number): string {
  if (!bytes) return ''
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

interface DataRecord {
  id: string
  name?: string
  extension?: string
  mime_type?: string
  data_size?: number
  pipeline_status?: string
  created_at?: string
  [key: string]: unknown
}

export default function DatasetDetailPage() {
  const t = useT()
  const { data: settings } = useSettings()
  const locale = (settings?.locale ?? 'en') as 'ru' | 'en'
  const params = useParams()
  const router = useRouter()
  const datasetId = params.id as string

  const [page, setPage] = useState(1)
  const [search, setSearch] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [cognifyRunning, setCognifyRunning] = useState(false)
  const [activeCognifyRunId, setActiveCognifyRunId] = useState<string | null>(null)
  const cognifyProgress = useCognifyProgress(activeCognifyRunId)
  const limit = 20

  // Block ③: tabs. Context/history fetch lazily — only when the tab is
  // opened (enabled flag), so the files view stays the default payload.
  const [tab, setTab] = useState<'files' | 'context' | 'history'>('files')
  const { data: ctxItems } = useQuery({
    queryKey: ['dataset-context', datasetId],
    queryFn: () => levara.getDatasetContext(datasetId),
    enabled: tab === 'context',
  })
  const { data: activity } = useQuery({
    queryKey: ['dataset-activity', datasetId],
    queryFn: () => levara.getDatasetActivity(datasetId),
    enabled: tab === 'history',
  })

  // Block ④: repo binding + commit feed (fetched on demand).
  const [repoPath, setRepoPath] = useState('')
  const [repoSaved, setRepoSaved] = useState(false)
  const [repoError, setRepoError] = useState('')
  const [repoSyncedFrom, setRepoSyncedFrom] = useState<string | undefined>(undefined)
  const [commits, setCommits] = useState<GitCommit[] | null>(null)
    const saveRepo = async () => {
    setRepoSaved(false); setRepoError('')
    try {
      await levara.setDatasetRepo(datasetId, repoPath)
      setRepoSaved(true)
      if (repoPath) {
        setCommits(await levara.getDatasetCommits(datasetId))
      }
    } catch (e) {
      setRepoError(e instanceof Error ? e.message : String(e))
    }
  }

  // Block ⑤: share management (react-query cache, invalidated on
  // grant/revoke instead of manual state sync).
  const queryClient = useQueryClient()
  const { data: shares } = useQuery({
    queryKey: ['dataset-shares', datasetId],
    queryFn: () => levara.getDatasetShares(datasetId),
  })
  const [shareEmail, setShareEmail] = useState('')
  const [shareRole, setShareRole] = useState('viewer')
  const [shareError, setShareError] = useState('')
  const grantShare = async () => {
    setShareError('')
    try {
      await levara.createDatasetShare(datasetId, shareEmail, shareRole)
      setShareEmail('')
      queryClient.invalidateQueries({ queryKey: ['dataset-shares', datasetId] })
    } catch (e) {
      setShareError(e instanceof Error ? e.message : String(e))
    }
  }
  const revokeShare = async (shareId: string) => {
    setShareError('')
    try {
      await levara.deleteDatasetShare(datasetId, shareId)
      queryClient.invalidateQueries({ queryKey: ['dataset-shares', datasetId] })
    } catch (e) {
      setShareError(e instanceof Error ? e.message : String(e))
    }
  }

  // Data access moved to React Query (T7). Datasets list feeds the name
  // lookup; useDatasetData paginates rows and handles the two response
  // shapes backend still returns (plain array vs {data, pagination}).
  // useDeleteDatasetRecord invalidates every page of this dataset on
  // success so deletions reflect in the table without manual refetch.
  const { data: datasetsRes } = useDatasets()
  const dsMeta = datasetsRes?.data?.find((d) => d.id === datasetId)
  const dsName = dsMeta?.name ?? ''
  const dsSize = dsMeta?.total_size ?? 0
  // Keep the repo input in sync once the dataset list resolves —
  // state-adjust-during-render pattern (react-hooks/set-state-in-effect
  // forbids the effect form).
  const repoBinding = dsMeta?.github_repo ?? ''
  if (repoSyncedFrom !== repoBinding) {
    setRepoSyncedFrom(repoBinding)
    setRepoPath(repoBinding)
  }
  const { data: dataPage, isLoading: loading } = useDatasetData(datasetId, page, limit)
  const records = (dataPage?.rows ?? []) as DataRecord[]
  const total = dataPage?.total ?? 0
  const deleteRecordMutation = useDeleteDatasetRecord()

  // SSE-driven completion: when the stream emits a terminal status,
  // stop showing the "running" spinner (T8). Replaces the previous
  // 3s cognifyStatus polling loop which raced with SSE updates.
  useEffect(() => {
    const d = cognifyProgress.data
    if (!d) return
    const terminal = d._complete || d.status === 'COMPLETED' || d.status === 'FAILED'
    if (!terminal) return
    startTransition(() => {
      setCognifyRunning(false)
      setActiveCognifyRunId(null)
    })
  }, [cognifyProgress.data])

  // datasets list + page data come from React Query now (see above). No
  // standalone useEffect — queries refetch automatically on key change.

  const handleCognify = async () => {
    setCognifyRunning(true)
    try {
      const res = await levara.cognify({ dataset_id: datasetId, collection: dsName })
      const runId = res?.pipeline_run_id
      if (!runId) {
        setCognifyRunning(false)
        return
      }
      // Hand off to SSE — the useEffect above flips cognifyRunning off
      // when the stream reports a terminal state.
      setActiveCognifyRunId(runId)
    } catch {
      setCognifyRunning(false)
    }
  }

  const handleDelete = async (recordId: string) => {
    try {
      await deleteRecordMutation.mutateAsync({ datasetId, recordId })
      // Cache invalidation in useDeleteDatasetRecord refreshes records + total.
    } catch (err) {
      alert(`Failed: ${err instanceof Error ? err.message : 'Error'}`)
    }
  }

  const handleBulkDelete = async () => {
    if (!selected.size || !confirm(`Delete ${selected.size} record(s)?`)) return
    for (const id of selected) await handleDelete(id)
    setSelected(new Set())
  }

  const toggleSelect = (id: string) => { const n = new Set(selected); if (n.has(id)) { n.delete(id) } else { n.add(id) }; setSelected(n) }
  const toggleAll = () => { if (selected.size === records.length) { setSelected(new Set()) } else { setSelected(new Set(records.map((r) => r.id))) } }

  const totalPages = Math.ceil(total / limit)
  const filtered = search ? records.filter((r) => (r.name || r.id).toLowerCase().includes(search.toLowerCase())) : records

  if (loading) {
    return (
      <div>
        <h1 className="text-2xl font-bold mb-6">{t('projects.title')}</h1>
        <div className="space-y-2">{[...Array(5)].map((_, i) => <Skeleton key={i} className="h-16 rounded-lg" />)}</div>
      </div>
    )
  }

  return (
    <div>
      <div className="flex items-center gap-3 mb-6">
        <Button variant="ghost" size="sm" onClick={() => router.push('/datasets')}><ArrowLeft className="h-4 w-4" /></Button>
        <div className="flex-1">
          <h1 className="text-2xl font-bold">{dsName || 'Dataset'}</h1>
          <p className="text-sm text-gray-500">{formatCount(total, locale)} {t('projects.files').toLowerCase()}{dsSize ? ` · ${formatBytes(dsSize, locale)}` : ''}</p>
        </div>
        <Button variant="secondary" size="sm" onClick={handleCognify} loading={cognifyRunning} disabled={cognifyRunning}>
          <Play className="h-4 w-4" /> Cognify
        </Button>
      </div>

      {/* Repo binding (block ④) */}
      <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4 mb-4">
        <div className="flex items-center justify-between mb-2">
          <h2 className="text-sm font-semibold">{t('project.repo.title')}</h2>
          {repoSaved && <span className="text-xs text-green-600">{t('project.repo.saved')}</span>}
          {repoError && <span className="text-xs text-red-600">{repoError}</span>}
        </div>
        <div className="flex gap-2">
          <Input placeholder={t('project.repo.placeholder')} value={repoPath} onChange={(e) => setRepoPath(e.target.value)} className="flex-1" />
          <Button size="sm" onClick={saveRepo}>{t('project.repo.save')}</Button>
        </div>
        {commits && commits.length > 0 && (
          <div className="mt-3 space-y-1.5">
            <p className="text-xs font-medium text-gray-500">{t('project.repo.commits')} ({commits.length})</p>
            {commits.map((cm) => (
              <div key={cm.hash} className="flex items-baseline gap-2 text-sm">
                <code className="text-xs text-gray-400">{cm.hash.slice(0, 7)}</code>
                <span className="flex-1 truncate">{cm.message}</span>
                <span className="text-xs text-gray-400">{cm.author}</span>
                <span className="text-xs text-gray-400">{cm.date}</span>
              </div>
            ))}
          </div>
        )}
        {commits && commits.length === 0 && (
          <p className="mt-2 text-xs text-gray-400">{t('project.repo.empty')}</p>
        )}
      </div>

      {/* Access / shares (block ⑤) */}
      <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4 mb-4">
        <div className="flex items-center justify-between mb-2">
          <h2 className="text-sm font-semibold">{t('project.shares.title')}</h2>
          {shareError && <span className="text-xs text-red-600">{t('project.shares.error')}: {shareError}</span>}
        </div>
        {(shares ?? []).length === 0 ? (
          <p className="text-xs text-gray-400 mb-3">{t('project.shares.empty')}</p>
        ) : (
          <div className="space-y-1.5 mb-3">
            {(shares ?? []).map((s) => (
              <div key={s.id} className="flex items-center gap-2 text-sm">
                <span className="flex-1 truncate">{s.user_email || s.user_id}</span>
                <span className="text-xs px-2 py-0.5 rounded bg-gray-100 dark:bg-gray-800 text-gray-600 dark:text-gray-300">
                  {t(`project.shares.role.${s.role}`)}
                </span>
                <button onClick={() => revokeShare(s.id)} title={t('project.shares.revoke')}
                  className="text-gray-400 hover:text-red-600 text-xs">✕</button>
              </div>
            ))}
          </div>
        )}
        <div className="flex gap-2">
          <Input placeholder={t('project.shares.email')} value={shareEmail} onChange={(e) => setShareEmail(e.target.value)} className="flex-1" />
          <select value={shareRole} onChange={(e) => setShareRole(e.target.value)}
            className="rounded-md border border-gray-300 dark:border-gray-700 bg-white dark:bg-gray-900 px-2 text-sm">
            <option value="viewer">{t('project.shares.role.viewer')}</option>
            <option value="editor">{t('project.shares.role.editor')}</option>
            <option value="admin">{t('project.shares.role.admin')}</option>
          </select>
          <Button size="sm" onClick={grantShare}>{t('project.shares.grant')}</Button>
        </div>
      </div>

      {/* Tabs */}
      <div className="flex gap-1 mb-4 border-b border-gray-200 dark:border-gray-800">
        {([['files', t('project.tab.files')], ['context', t('project.tab.context')], ['history', t('project.tab.history')]] as const).map(([key, label]) => (
          <button key={key} onClick={() => setTab(key)}
            className={`px-4 py-2 text-sm font-medium -mb-px border-b-2 transition-colors ${tab === key
              ? 'border-blue-600 text-blue-600 dark:border-blue-400 dark:text-blue-400'
              : 'border-transparent text-gray-500 hover:text-gray-700 dark:hover:text-gray-300'}`}>
            {label}
          </button>
        ))}
      </div>

      {tab === 'context' && (
        <div className="space-y-2">
          {(ctxItems ?? []).length === 0 ? (
            <EmptyState icon={FileText} title={t('project.tab.context')} description={t('project.context.empty')} />
          ) : (
            (ctxItems ?? []).map((m: ProjectContextItem) => (
              <div key={m.id} className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-3">
                <div className="flex items-center justify-between">
                  <span className="font-medium text-sm">{m.key}</span>
                  <span className="text-xs text-gray-400">{formatDate(m.created_at, locale)}</span>
                </div>
                <p className="text-sm text-gray-600 dark:text-gray-400 mt-1">{m.value}</p>
              </div>
            ))
          )}
        </div>
      )}

      {tab === 'history' && (
        <div className="space-y-2">
          {(activity ?? []).length === 0 ? (
            <EmptyState icon={FileText} title={t('project.tab.history')} description={t('project.history.empty')} />
          ) : (
            (activity ?? []).map((e: ProjectActivityItem, i: number) => (
              <div key={i} className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-3 flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <Badge variant={e.type === 'upload' ? 'success' : e.type === 'share_granted' ? 'default' : 'warning'}>
                    {e.type === 'upload' ? t('project.history.upload') : e.type === 'share_granted' ? t('project.history.share') : t('project.history.context')}
                  </Badge>
                  <span className="font-medium text-sm">{e.title}</span>
                  {e.detail && <span className="text-xs text-gray-400">{e.detail}</span>}
                </div>
                <span className="text-xs text-gray-400">{formatDate(e.created_at, locale)}</span>
              </div>
            ))
          )}
        </div>
      )}

      {tab === 'files' && (
      <div className="flex items-center gap-3 mb-4">
        <Input placeholder={t('memories.searchPlaceholder')} value={search} onChange={(e) => setSearch(e.target.value)} className="w-64" />
        {selected.size > 0 && (
          <div className="flex items-center gap-2 ml-auto">
            <Badge>{formatCount(selected.size, locale)}</Badge>
            <Button variant="danger" size="sm" onClick={handleBulkDelete}><Trash2 className="h-4 w-4" /> {t('common.delete')}</Button>
            <Button variant="ghost" size="sm" onClick={() => setSelected(new Set())}>{t('common.clear')}</Button>
          </div>
        )}
      </div>
      )}

      {tab === 'files' && (filtered.length === 0 ? (
        <EmptyState icon={FileText} title={t('common.empty')} description={search ? '' : t('project.dropzone')} />
      ) : (
        <>
          <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 overflow-hidden">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800">
                  <th className="w-10 px-3 py-2">
                    <input type="checkbox" checked={selected.size === records.length && records.length > 0} onChange={toggleAll} className="rounded" aria-label="Select all" />
                  </th>
                  <th className="text-left px-3 py-2 font-medium text-gray-500">{t('project.file')}</th>
                  <th className="text-left px-3 py-2 font-medium text-gray-500">{t('project.type')}</th>
                  <th className="text-left px-3 py-2 font-medium text-gray-500">{t('projects.size')}</th>
                  <th className="text-left px-3 py-2 font-medium text-gray-500">{t('common.status')}</th>
                  <th className="w-20 px-3 py-2"></th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((r) => (
                  <tr key={r.id} className="border-b border-gray-100 dark:border-gray-800 hover:bg-gray-50 dark:hover:bg-gray-800/50">
                    <td className="px-3 py-2">
                      <input type="checkbox" checked={selected.has(r.id)} onChange={() => toggleSelect(r.id)} className="rounded" aria-label={`Select ${r.name || r.id}`} />
                    </td>
                    <td className="px-3 py-2">
                      <p className="font-medium text-gray-900 dark:text-gray-100">{r.name || r.id}</p>
                      <code className="text-[10px] text-gray-400">{r.id}</code>
                    </td>
                    <td className="px-3 py-2 text-gray-500 text-xs">{r.extension || r.mime_type || '—'}</td>
                    <td className="px-3 py-2 text-gray-500 text-xs">{formatSize(r.data_size)}</td>
                    <td className="px-3 py-2">
                      <Badge variant={r.pipeline_status === 'completed' ? 'success' : r.pipeline_status === 'processing' ? 'warning' : 'default'}>
                        {r.pipeline_status === 'completed' ? t('project.status.ready')
                          : r.pipeline_status === 'processing' ? t('project.status.processing')
                          : r.pipeline_status === 'error' ? t('project.status.error')
                          : t('project.status.unknown')}
                      </Badge>
                    </td>
                    <td className="px-3 py-2">
                      <Button variant="ghost" size="sm" onClick={() => handleDelete(r.id)}>
                        <Trash2 className="h-3.5 w-3.5 text-red-400" />
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          {totalPages > 1 && (
            <div className="flex items-center justify-between mt-4">
              <p className="text-sm text-gray-500">Page {page} of {totalPages} ({total} records)</p>
              <div className="flex gap-1">
                <Button variant="ghost" size="sm" disabled={page <= 1} onClick={() => setPage(page - 1)}><ChevronLeft className="h-4 w-4" /> Prev</Button>
                <Button variant="ghost" size="sm" disabled={page >= totalPages} onClick={() => setPage(page + 1)}>Next <ChevronRight className="h-4 w-4" /></Button>
              </div>
            </div>
          )}
        </>
      ))}
    </div>
  )
}
