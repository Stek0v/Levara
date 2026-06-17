'use client'

import { useMemo, useState } from 'react'
import {
  useCollections,
  useCutoverEmbeddingMigration,
  useDisableEmbeddingDualWrite,
  useEmbeddingDualWriteRules,
  useEmbeddingMigrationStatus,
  useEmbeddingShadowRead,
  useMCPAdminSummary,
  useMCPSessions,
  useMCPTools,
  useRetryEmbeddingMigration,
  useStartEmbeddingMigration,
} from '@/hooks/use-levara'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Brain, GitBranch, ListChecks, Play, RefreshCw, Search, Shield, Shuffle, Users } from 'lucide-react'
import type { EmbeddingMigrationRequest } from '@/lib/api'

function statusVariant(status?: string) {
  if (status === 'canonical') return 'success'
  if (status === 'deprecated') return 'warning'
  if (status === 'legacy') return 'default'
  return 'info'
}

export default function AdminPage() {
  const [toolSearch, setToolSearch] = useState('')
  const [groupFilter, setGroupFilter] = useState('')
  const [migrationRunId, setMigrationRunId] = useState('')
  const [sourceCollection, setSourceCollection] = useState('')
  const [targetCollection, setTargetCollection] = useState('')
  const [targetEndpoint, setTargetEndpoint] = useState('')
  const [targetModel, setTargetModel] = useState('')
  const [targetDim, setTargetDim] = useState('1024')
  const [enableDualWrite, setEnableDualWrite] = useState(true)
  const [dryRun, setDryRun] = useState(false)
  const [shadowQueries, setShadowQueries] = useState('memory search\nproject architecture\nrate limiting')
  const summary = useMCPAdminSummary()
  const tools = useMCPTools()
  const sessions = useMCPSessions(20)
  const collections = useCollections()
  const dualWrite = useEmbeddingDualWriteRules()
  const migrationStatus = useEmbeddingMigrationStatus(migrationRunId.trim())
  const startMigration = useStartEmbeddingMigration()
  const retryMigration = useRetryEmbeddingMigration()
  const cutoverMigration = useCutoverEmbeddingMigration()
  const disableDualWrite = useDisableEmbeddingDualWrite()
  const shadowRead = useEmbeddingShadowRead()

  const groups = useMemo(
    () => Object.keys(summary.data?.tools_by_group ?? {}).sort(),
    [summary.data?.tools_by_group],
  )
  const filteredTools = useMemo(() => {
    const q = toolSearch.trim().toLowerCase()
    return (tools.data?.tools ?? []).filter((tool) => {
      if (groupFilter && tool.group !== groupFilter) return false
      if (!q) return true
      return tool.name.toLowerCase().includes(q) || String(tool.description || '').toLowerCase().includes(q)
    })
  }, [tools.data?.tools, toolSearch, groupFilter])
  const collectionNames = useMemo(() => (collections.data ?? []).map((c) => c.name).sort(), [collections.data])
  const effectiveTarget = targetCollection.trim() || (sourceCollection.trim() ? `${sourceCollection.trim()}__shadow` : '')
  const canStartMigration = Boolean(sourceCollection.trim() && effectiveTarget && targetEndpoint.trim() && targetModel.trim() && Number.parseInt(targetDim, 10) > 0)
  const canRunShadow = Boolean(sourceCollection.trim() && effectiveTarget && shadowQueries.trim())

  const submitMigration = () => {
    const payload: EmbeddingMigrationRequest = {
      source_collection: sourceCollection.trim(),
      target_collection: effectiveTarget,
      target_endpoint: targetEndpoint.trim(),
      target_model: targetModel.trim(),
      target_dim: Number.parseInt(targetDim, 10),
      target_metric: 'cosine',
      batch_size: 64,
      max_attempts: 3,
      dry_run: dryRun,
      enable_dual_write: enableDualWrite,
    }
    startMigration.mutate(payload, {
      onSuccess: (status) => {
        setMigrationRunId(status.run_id)
        if (!targetCollection.trim()) setTargetCollection(effectiveTarget)
      },
    })
  }

  const runShadowRead = () => {
    shadowRead.mutate({
      source_collection: sourceCollection.trim(),
      shadow_collection: effectiveTarget,
      queries: shadowQueries.split('\n').map((q) => q.trim()).filter(Boolean),
      top_k: 10,
      shadow_endpoint: targetEndpoint.trim() || undefined,
      shadow_model: targetModel.trim() || undefined,
      min_mean_jaccard_at_k: 0.3,
      min_top1_stability: 0.3,
      max_shadow_empty_rate: 0.25,
      max_latency_ratio_p95: 3,
      require_cutover_gate_pass: false,
    })
  }
  const status = migrationStatus.data
  const progress = status?.total_records ? Math.round((status.processed / status.total_records) * 100) : 0

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h1 className="text-2xl font-bold">Admin</h1>
        <Badge variant={summary.data?.audit_enabled ? 'success' : 'default'}>{summary.data?.audit_enabled ? 'MCP audit on' : 'MCP audit off'}</Badge>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6">
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4">
          <div className="flex items-center justify-between mb-2"><span className="text-sm text-gray-500">MCP tools</span><ListChecks className="h-4 w-4 text-blue-600" /></div>
          <span className="text-2xl font-bold">{summary.data?.tools_total ?? 0}</span>
        </div>
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4">
          <div className="flex items-center justify-between mb-2"><span className="text-sm text-gray-500">Recent sessions</span><Users className="h-4 w-4 text-green-600" /></div>
          <span className="text-2xl font-bold">{sessions.data?.total ?? 0}</span>
        </div>
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4">
          <div className="flex items-center justify-between mb-2"><span className="text-sm text-gray-500">Pinned memories</span><Brain className="h-4 w-4 text-purple-600" /></div>
          <span className="text-2xl font-bold">{summary.data?.pinned_memories ?? 0}</span>
        </div>
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4">
          <div className="flex items-center justify-between mb-2"><span className="text-sm text-gray-500">Memory warnings</span><Shield className="h-4 w-4 text-amber-600" /></div>
          <span className="text-2xl font-bold">{summary.data?.memory_metadata_warnings ?? 0}</span>
        </div>
      </div>

      <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5 mb-6">
        <div className="flex items-center justify-between gap-3 mb-4">
          <h2 className="text-lg font-semibold flex items-center gap-2"><Shuffle className="h-5 w-5 text-cyan-600" /> Embedding Migration</h2>
          <Badge variant={status?.status === 'COMPLETED' || status?.status === 'CUTOVER_COMPLETED' ? 'success' : status?.status === 'FAILED' ? 'error' : 'default'}>
            {status?.status || 'idle'}
          </Badge>
        </div>
        <div className="grid grid-cols-1 xl:grid-cols-[1fr_0.9fr] gap-6">
          <div className="space-y-4">
            <div className="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-3 gap-3">
              <select value={sourceCollection} onChange={(e) => setSourceCollection(e.target.value)} className="h-9 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 text-sm" aria-label="Source collection">
                <option value="">Source collection</option>
                {collectionNames.map((name) => <option key={name} value={name}>{name}</option>)}
              </select>
              <Input value={targetCollection} onChange={(e) => setTargetCollection(e.target.value)} placeholder={sourceCollection ? `${sourceCollection}__shadow` : 'target shadow collection'} aria-label="Target shadow collection" />
              <Input value={targetDim} onChange={(e) => setTargetDim(e.target.value)} inputMode="numeric" placeholder="target dim" aria-label="Target embedding dimension" />
              <Input value={targetModel} onChange={(e) => setTargetModel(e.target.value)} placeholder="target embedding model" aria-label="Target embedding model" />
              <Input value={targetEndpoint} onChange={(e) => setTargetEndpoint(e.target.value)} placeholder="target embedding endpoint" aria-label="Target embedding endpoint" />
              <Input value={migrationRunId} onChange={(e) => setMigrationRunId(e.target.value)} placeholder="migration run id" aria-label="Migration run id" />
            </div>
            <div className="flex flex-wrap items-center gap-3">
              <label className="flex items-center gap-2 text-sm text-gray-500"><input type="checkbox" checked={enableDualWrite} onChange={(e) => setEnableDualWrite(e.target.checked)} /> dual-write</label>
              <label className="flex items-center gap-2 text-sm text-gray-500"><input type="checkbox" checked={dryRun} onChange={(e) => setDryRun(e.target.checked)} /> dry-run</label>
              <Button size="sm" onClick={submitMigration} disabled={!canStartMigration} loading={startMigration.isPending}>
                <Play className="h-4 w-4" /> Start
              </Button>
              <Button size="sm" variant="secondary" onClick={() => migrationRunId && retryMigration.mutate(migrationRunId)} disabled={!migrationRunId} loading={retryMigration.isPending}>
                <RefreshCw className="h-4 w-4" /> Retry failed
              </Button>
              <Button size="sm" variant="secondary" onClick={() => migrationRunId && cutoverMigration.mutate({ runId: migrationRunId, params: { retention_days: 7 } })} disabled={!migrationRunId || status?.status !== 'COMPLETED'} loading={cutoverMigration.isPending}>
                <GitBranch className="h-4 w-4" /> Cutover
              </Button>
            </div>
            {(startMigration.isError || retryMigration.isError || cutoverMigration.isError) && (
              <p className="text-sm text-red-600">{String((startMigration.error || retryMigration.error || cutoverMigration.error) instanceof Error ? (startMigration.error || retryMigration.error || cutoverMigration.error)?.message : 'embedding migration failed')}</p>
            )}
            {status && (
              <div className="rounded-md border border-gray-100 dark:border-gray-800 p-3">
                <div className="flex items-center justify-between text-sm">
                  <span className="font-mono text-xs truncate">{status.run_id}</span>
                  <span>{status.processed}/{status.total_records} · failed {status.failed}</span>
                </div>
                <div className="mt-2 h-2 rounded-full bg-gray-200 dark:bg-gray-800">
                  <div className="h-2 rounded-full bg-cyan-600" style={{ width: `${Math.min(100, progress)}%` }} />
                </div>
                <p className="mt-2 text-xs text-gray-400 truncate">{status.message || status.target_version || 'waiting for status'}</p>
              </div>
            )}
          </div>

          <div className="space-y-4">
            <div>
              <div className="flex items-center justify-between mb-2">
                <h3 className="text-sm font-medium">Shadow-read gate</h3>
                <Button size="sm" variant="secondary" onClick={runShadowRead} disabled={!canRunShadow} loading={shadowRead.isPending}>
                  <Search className="h-4 w-4" /> Run
                </Button>
              </div>
              <textarea value={shadowQueries} onChange={(e) => setShadowQueries(e.target.value)} className="h-24 w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-2 text-sm" aria-label="Shadow read queries" />
              {shadowRead.isError && <p className="mt-2 text-sm text-red-600">{shadowRead.error instanceof Error ? shadowRead.error.message : 'shadow-read failed'}</p>}
              {shadowRead.data && (
                <div className="mt-3 grid grid-cols-2 gap-2 text-sm">
                  <div className="rounded-md bg-gray-50 dark:bg-gray-800 p-2"><span className="text-gray-500">Jaccard</span><p className="font-medium">{shadowRead.data.mean_jaccard_at_k.toFixed(3)}</p></div>
                  <div className="rounded-md bg-gray-50 dark:bg-gray-800 p-2"><span className="text-gray-500">Top-1</span><p className="font-medium">{shadowRead.data.top1_stability.toFixed(3)}</p></div>
                  <div className="rounded-md bg-gray-50 dark:bg-gray-800 p-2"><span className="text-gray-500">Shadow p95</span><p className="font-medium">{shadowRead.data.shadow_p95_ms} ms</p></div>
                  <div className="rounded-md bg-gray-50 dark:bg-gray-800 p-2"><span className="text-gray-500">Gate</span><p className={shadowRead.data.cutover_ready ? 'font-medium text-green-600' : 'font-medium text-amber-600'}>{shadowRead.data.cutover_ready ? 'ready' : 'blocked'}</p></div>
                  {(shadowRead.data.gate_failures ?? []).length > 0 && <p className="col-span-2 text-xs text-amber-600">{shadowRead.data.gate_failures?.join('; ')}</p>}
                </div>
              )}
            </div>
            <div>
              <h3 className="text-sm font-medium mb-2">Dual-write rules</h3>
              <div className="space-y-2">
                {(dualWrite.data?.rules ?? []).map((rule) => (
                  <div key={rule.source_collection} className="flex items-center justify-between gap-3 rounded-md border border-gray-100 dark:border-gray-800 p-2 text-sm">
                    <div className="min-w-0">
                      <p className="truncate"><span className="font-medium">{rule.source_collection}</span> → {rule.target_collection}</p>
                      <p className="text-xs text-gray-400 truncate">{rule.target_model} · {rule.target_contract?.dim || 'dim?'}</p>
                    </div>
                    <Button size="sm" variant="ghost" onClick={() => disableDualWrite.mutate(rule.source_collection)} loading={disableDualWrite.isPending}>Disable</Button>
                  </div>
                ))}
                {dualWrite.isSuccess && (dualWrite.data?.rules ?? []).length === 0 && <p className="text-sm text-gray-400">No active dual-write rules.</p>}
              </div>
            </div>
          </div>
        </div>
      </section>

      <div className="grid grid-cols-1 xl:grid-cols-[1.35fr_0.65fr] gap-6">
        <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <div className="flex items-center justify-between gap-3 mb-4">
            <h2 className="text-lg font-semibold flex items-center gap-2"><Search className="h-5 w-5 text-blue-600" /> MCP Tools</h2>
            <div className="flex gap-2">
              <Input value={toolSearch} onChange={(e) => setToolSearch(e.target.value)} placeholder="search tools" className="w-52" />
              <select value={groupFilter} onChange={(e) => setGroupFilter(e.target.value)} className="h-9 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 text-sm">
                <option value="">All groups</option>
                {groups.map((group) => <option key={group} value={group}>{group}</option>)}
              </select>
            </div>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="text-left text-gray-500">
                <tr>
                  <th className="py-2 pr-3">Tool</th>
                  <th className="py-2 pr-3">Group</th>
                  <th className="py-2 pr-3">Status</th>
                  <th className="py-2 pr-3">Description</th>
                </tr>
              </thead>
              <tbody>
                {filteredTools.map((tool) => (
                  <tr key={tool.name} className="border-t border-gray-100 dark:border-gray-800">
                    <td className="py-2 pr-3 font-mono text-xs">{tool.name}</td>
                    <td className="py-2 pr-3"><Badge variant="default">{tool.group || 'unknown'}</Badge></td>
                    <td className="py-2 pr-3"><Badge variant={statusVariant(tool.status)}>{tool.status || 'canonical'}</Badge></td>
                    <td className="py-2 pr-3 text-gray-500 max-w-xl"><span className="line-clamp-2">{tool.description || '-'}</span></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>

        <div className="space-y-6">
          <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
            <h2 className="text-lg font-semibold mb-4">Tool Groups</h2>
            <div className="space-y-2">
              {Object.entries(summary.data?.tools_by_group ?? {}).sort(([a], [b]) => a.localeCompare(b)).map(([group, count]) => (
                <div key={group} className="flex justify-between text-sm">
                  <span className="text-gray-500">{group}</span>
                  <span>{count}</span>
                </div>
              ))}
            </div>
          </section>

          <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
            <h2 className="text-lg font-semibold mb-4">Recent Sessions</h2>
            <div className="space-y-2">
              {(sessions.data?.sessions ?? []).map((session) => (
                <div key={session.session_id} className="rounded-md border border-gray-100 dark:border-gray-800 p-3">
                  <code className="block text-xs truncate">{session.session_id}</code>
                  <div className="mt-2 flex items-center justify-between text-xs text-gray-500">
                    <span>{session.count} interactions</span>
                    <span>{session.last_at ? new Date(session.last_at).toLocaleString() : '-'}</span>
                  </div>
                </div>
              ))}
              {sessions.isSuccess && (sessions.data?.sessions ?? []).length === 0 && <p className="text-sm text-gray-400">No recent sessions.</p>}
            </div>
          </section>
        </div>
      </div>
    </div>
  )
}
