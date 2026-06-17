'use client'

import { useState } from 'react'
import { useInfo, useCollections, useFeedbackStats, useCacheStats, useErrors, useHealthDetails, useVSARebuild, useVSAQuery, useVSAStatus } from '@/hooks/use-levara'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { Activity, MessageCircle, Star, AlertCircle, Zap, Database, Network, RefreshCw, Search } from 'lucide-react'
import type { VSACandidate } from '@/lib/api'

export default function AnalyticsPage() {
  const { data: info } = useInfo()
  const { data: collections } = useCollections()
  const { data: feedback } = useFeedbackStats()
  const { data: cache } = useCacheStats()
  const { data: errors } = useErrors()
  const { data: healthDetails } = useHealthDetails()
  const { data: vsa } = useVSAStatus()
  const vsaRebuild = useVSARebuild()
  const vsaQuery = useVSAQuery()
  const [vsaDataset, setVSADataset] = useState('')
  const [vsaSourceId, setVSASourceId] = useState('')
  const [vsaPredicate, setVSAPredicate] = useState('')
  const [vsaTopK, setVSATopK] = useState('5')
  const isLoading = !info

  const handleVSARebuild = () => {
    vsaRebuild.mutate({ dataset_id: vsaDataset || undefined })
  }

  const handleVSAQuery = () => {
    const sourceId = vsaSourceId.trim()
    const predicate = vsaPredicate.trim()
    if (!sourceId || !predicate) return
    vsaQuery.mutate({
      dataset_id: vsaDataset || undefined,
      source_id: sourceId,
      predicate,
      top_k: Math.max(1, Number.parseInt(vsaTopK || '5', 10) || 5),
    })
  }

  if (isLoading) {
    return (
      <div>
        <h1 className="text-2xl font-bold mb-6">Analytics</h1>
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
          {[...Array(4)].map((_, i) => <Skeleton key={i} className="h-28 rounded-lg" />)}
        </div>
      </div>
    )
  }

  const widgets = [
    { title: 'System Status', value: info?.status || 'unknown', icon: Activity, color: 'text-green-600', sub: `dim=${info?.dimension}, shards=${info?.shards}` },
    { title: 'Collections', value: collections?.length ?? 0, icon: Database, color: 'text-blue-600', sub: 'vector indexes' },
    { title: 'VSA Facts', value: vsa?.available ? (vsa.fact_count ?? 0).toLocaleString() : 'off', icon: Network, color: 'text-cyan-600', sub: vsa?.available ? `${vsa.shard_count ?? 0} shards / ${vsa.member_count ?? 0} members` : vsa?.reason || 'sql graph index' },
    { title: 'Feedback', value: feedback?.total ?? 0, icon: Star, color: 'text-amber-500', sub: feedback?.total ? `avg: ${feedback.avg_rating}/5` : 'no feedback' },
    { title: 'LLM Cache', value: cache?.hit_rate != null ? `${(cache.hit_rate * 100).toFixed(0)}%` : '—', icon: Zap, color: 'text-purple-600', sub: cache ? `${cache.hits} hits / ${cache.misses} misses` : '' },
  ]

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h1 className="text-2xl font-bold">Analytics</h1>
        <Badge variant="default">Auto-refresh 30s</Badge>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mb-8">
        {widgets.map((w) => (
          <div key={w.title} className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4">
            <div className="flex items-center justify-between mb-2">
              <span className="text-sm text-gray-500">{w.title}</span>
              <w.icon className={`h-4 w-4 ${w.color}`} />
            </div>
            <span className="text-2xl font-bold">{w.value}</span>
            <p className="text-xs text-gray-400 mt-1">{w.sub}</p>
          </div>
        ))}
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {/* Dependencies */}
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><Activity className="h-5 w-5 text-green-600" /> Dependency Health</h2>
          {healthDetails?.services ? (
            <div className="space-y-2">
              {['backend', 'database', 'storage', 'postgres', 'neo4j', 'embed', 'llm', 'rerank', 'collections', 'grpc', 'ocr', 'whisper'].map((name) => {
                const svc = healthDetails.services?.[name]
                if (!svc) return null
                const status = String(svc.status || 'unknown')
                const ok = status === 'connected' || status === 'ready' || status === 'listening'
                const idle = status === 'not_configured'
                return (
                  <div key={name} className="flex items-center justify-between gap-3 text-sm">
                    <span className="capitalize text-gray-500">{name}</span>
                    <div className="flex items-center gap-2 min-w-0">
                      {(svc.endpoint || svc.url || svc.model) && (
                        <span className="truncate text-xs text-gray-400 max-w-[220px]">{String(svc.model || svc.endpoint || svc.url)}</span>
                      )}
                      <Badge variant={ok ? 'success' : idle ? 'default' : 'error'}>{status}</Badge>
                    </div>
                  </div>
                )
              })}
            </div>
          ) : (
            <p className="text-sm text-gray-400">Dependency details are not available.</p>
          )}
        </div>

        {/* VSA */}
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><Network className="h-5 w-5 text-cyan-600" /> VSA Graph Memory</h2>
          {vsa?.available ? (
            <div className="space-y-3">
              <div className="flex justify-between text-sm"><span className="text-gray-500">Facts</span><span>{(vsa.fact_count ?? 0).toLocaleString()}</span></div>
              <div className="flex justify-between text-sm"><span className="text-gray-500">Shards</span><span>{(vsa.shard_count ?? 0).toLocaleString()}</span></div>
              <div className="flex justify-between text-sm"><span className="text-gray-500">Members</span><span>{(vsa.member_count ?? 0).toLocaleString()}</span></div>
              <div className="flex justify-between text-sm"><span className="text-gray-500">Max dim</span><span>{vsa.max_dim || '—'}</span></div>
              <div className="flex justify-between text-sm"><span className="text-gray-500">Datasets</span><span>{vsa.datasets?.length ?? 0}</span></div>
              <div className="flex justify-between text-sm"><span className="text-gray-500">Predicates</span><span>{vsa.predicates?.length ?? 0}</span></div>
              {vsa.last_updated_at && (
                <div className="pt-2 border-t border-gray-100 dark:border-gray-800">
                  <p className="text-xs text-gray-400">Last rebuild</p>
                  <p className="text-sm">{new Date(vsa.last_updated_at).toLocaleString()}</p>
                </div>
              )}
              <div className="pt-3 border-t border-gray-100 dark:border-gray-800 space-y-3">
                <div className="grid grid-cols-1 md:grid-cols-[1fr_auto] gap-2">
                  <select
                    value={vsaDataset}
                    onChange={(e) => setVSADataset(e.target.value)}
                    className="h-9 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 text-sm"
                    aria-label="VSA dataset"
                  >
                    <option value="">All / default dataset</option>
                    {(vsa.datasets ?? []).map((id) => (
                      <option key={id} value={id}>{id || 'default'}</option>
                    ))}
                  </select>
                  <Button variant="secondary" size="sm" onClick={handleVSARebuild} loading={vsaRebuild.isPending}>
                    <RefreshCw className="h-4 w-4" />
                    Rebuild
                  </Button>
                </div>
                {vsaRebuild.isSuccess && (
                  <p className="text-xs text-green-600">VSA rebuild requested for {vsaRebuild.data.dataset_id || 'default/all'}.</p>
                )}
                {vsaRebuild.isError && (
                  <p className="text-xs text-red-600">{vsaRebuild.error instanceof Error ? vsaRebuild.error.message : 'VSA rebuild failed'}</p>
                )}
                <div className="grid grid-cols-1 md:grid-cols-[1fr_1fr_80px_auto] gap-2">
                  <Input
                    value={vsaSourceId}
                    onChange={(e) => setVSASourceId(e.target.value)}
                    placeholder="source node id"
                    aria-label="VSA source node id"
                  />
                  <Input
                    value={vsaPredicate}
                    onChange={(e) => setVSAPredicate(e.target.value)}
                    placeholder={(vsa.predicates ?? [])[0] || 'predicate'}
                    aria-label="VSA predicate"
                  />
                  <Input
                    value={vsaTopK}
                    onChange={(e) => setVSATopK(e.target.value)}
                    inputMode="numeric"
                    aria-label="VSA top K"
                  />
                  <Button size="sm" onClick={handleVSAQuery} loading={vsaQuery.isPending} disabled={!vsaSourceId.trim() || !vsaPredicate.trim()}>
                    <Search className="h-4 w-4" />
                    Query
                  </Button>
                </div>
                {vsaQuery.isError && (
                  <p className="text-xs text-red-600">{vsaQuery.error instanceof Error ? vsaQuery.error.message : 'VSA query failed'}</p>
                )}
                {vsaQuery.data && (
                  <div className="space-y-2">
                    {vsaQuery.data.candidates.length === 0 ? (
                      <p className="text-sm text-gray-400">No VSA candidates found.</p>
                    ) : (
                      vsaQuery.data.candidates.map((c: VSACandidate) => (
                        <div key={`${c.shard_id}:${c.edge_id}:${c.target_id}`} className="rounded-md bg-gray-50 dark:bg-gray-800 px-3 py-2 text-sm">
                          <div className="flex items-center justify-between gap-3">
                            <span className="font-medium truncate">{c.target_name || c.target_id}</span>
                            <Badge variant="info">{c.similarity.toFixed(3)}</Badge>
                          </div>
                          <p className="mt-1 text-xs text-gray-400 truncate">{c.predicate} · {c.edge_id} · {c.dataset_id || 'default'}</p>
                        </div>
                      ))
                    )}
                  </div>
                )}
              </div>
            </div>
          ) : (
            <p className="text-sm text-gray-400">{vsa?.reason || 'VSA status not available'}</p>
          )}
        </div>

        {/* Cache */}
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><Zap className="h-5 w-5 text-purple-600" /> LLM Cache</h2>
          {cache ? (
            <div className="space-y-3">
              <div className="flex justify-between text-sm"><span className="text-gray-500">Size</span><span>{cache.size} / {cache.max_size}</span></div>
              <div className="flex justify-between text-sm"><span className="text-gray-500">Hits</span><span className="text-green-600">{cache.hits}</span></div>
              <div className="flex justify-between text-sm"><span className="text-gray-500">Misses</span><span className="text-red-500">{cache.misses}</span></div>
              <div className="flex justify-between text-sm"><span className="text-gray-500">Hit Rate</span><span className="font-medium">{(cache.hit_rate * 100).toFixed(1)}%</span></div>
              <div className="w-full bg-gray-200 dark:bg-gray-700 rounded-full h-2">
                <div className="bg-purple-600 h-2 rounded-full transition-all" style={{ width: `${cache.hit_rate * 100}%` }} />
              </div>
            </div>
          ) : <p className="text-sm text-gray-400">Cache not available</p>}
        </div>

        {/* Feedback */}
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><MessageCircle className="h-5 w-5 text-amber-500" /> Feedback</h2>
          {feedback?.total ? (
            <div className="space-y-3">
              <div className="flex justify-between text-sm"><span className="text-gray-500">Total</span><span>{feedback.total}</span></div>
              <div className="flex justify-between text-sm">
                <span className="text-gray-500">Average</span>
                <div className="flex items-center gap-1">
                  {[1,2,3,4,5].map((s) => <Star key={s} className={`h-3 w-3 ${s <= feedback.avg_rating ? 'text-amber-400 fill-amber-400' : 'text-gray-300'}`} />)}
                  <span className="ml-1">{feedback.avg_rating}/5</span>
                </div>
              </div>
              {feedback.worst_query && <div><span className="text-xs text-gray-500">Worst:</span><p className="text-sm text-red-600 italic mt-1">{feedback.worst_query}</p></div>}
            </div>
          ) : <p className="text-sm text-gray-400">No feedback yet</p>}
        </div>

        {/* Errors */}
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5 lg:col-span-2">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><AlertCircle className="h-5 w-5 text-red-500" /> Recent Errors</h2>
          {errors && errors.length > 0 ? (
            <div className="space-y-2">
              {errors.slice(0, 5).map((e, i) => (
                <div key={i} className="flex items-start gap-2 text-sm p-2 bg-red-50 dark:bg-red-900/10 rounded">
                  <AlertCircle className="h-4 w-4 text-red-500 mt-0.5 flex-shrink-0" />
                  <div><p className="text-red-800 dark:text-red-300 truncate">{e.message}</p>
                    {e.timestamp && <p className="text-xs text-red-400">{new Date(e.timestamp).toLocaleString()}</p>}</div>
                </div>
              ))}
            </div>
          ) : <p className="text-sm text-gray-400">No errors. System is healthy.</p>}
        </div>
      </div>
    </div>
  )
}
