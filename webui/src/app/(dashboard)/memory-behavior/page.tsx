'use client'

import { useMemo, useState } from 'react'
import { useRouter, useSearchParams } from 'next/navigation'
import { Activity, AlertTriangle, Brain, Database, MessageCircle, RefreshCw, Search } from 'lucide-react'
import { useAgentTrajectories, useMemoryBehavior } from '@/hooks/use-levara'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'

const WINDOWS = [1, 24, 168, 720]

function percent(value?: number) {
  return `${(((value ?? 0) * 100)).toFixed(1)}%`
}

function bytes(value?: number) {
  const n = value ?? 0
  if (n > 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  if (n > 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${Math.round(n)} B`
}

export default function MemoryBehaviorPage() {
  const router = useRouter()
  const searchParams = useSearchParams()
  const hours = Number(searchParams.get('hours') || '24')
  const collection = searchParams.get('collection') || ''
  const client = searchParams.get('client') || ''
  const [collectionInput, setCollectionInput] = useState(collection)
  const [clientInput, setClientInput] = useState(client)

  const params = useMemo(() => ({ hours, collection: collection || undefined, client: client || undefined }), [hours, collection, client])
  const behavior = useMemoryBehavior(params)
  const trajectories = useAgentTrajectories({ ...params, limit: 20 })

  const updateFilters = (next: { hours?: number; collection?: string; client?: string }) => {
    const q = new URLSearchParams(searchParams.toString())
    if (next.hours) q.set('hours', String(next.hours))
    if ('collection' in next) {
      if (next.collection) q.set('collection', next.collection)
      else q.delete('collection')
    }
    if ('client' in next) {
      if (next.client) q.set('client', next.client)
      else q.delete('client')
    }
    router.push(`/memory-behavior?${q.toString()}`)
  }

  const summary = behavior.data?.summary
  const problems = summary?.problem_trajectories ?? []
  const loading = behavior.isLoading || trajectories.isLoading
  const error = behavior.error || trajectories.error

  if (loading) {
    return (
      <div>
        <h1 className="text-2xl font-bold mb-6">Memory Behavior</h1>
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
          {[...Array(8)].map((_, i) => <Skeleton key={i} className="h-28 rounded-lg" />)}
        </div>
      </div>
    )
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold">Memory Behavior</h1>
          <p className="text-sm text-gray-500 mt-1">Agent memory discipline from MCP audit trajectories.</p>
        </div>
        <Badge variant="default">Auto-refresh 30s</Badge>
      </div>

      <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4 mb-6">
        <div className="flex flex-wrap gap-2 items-end">
          <div>
            <label className="block text-xs text-gray-500 mb-1">Window</label>
            <div className="flex gap-1">
              {WINDOWS.map((w) => (
                <Button key={w} size="sm" variant={hours === w ? 'primary' : 'secondary'} onClick={() => updateFilters({ hours: w })}>
                  {w === 1 ? '1h' : w === 24 ? '24h' : w === 168 ? '7d' : '30d'}
                </Button>
              ))}
            </div>
          </div>
          <div>
            <label className="block text-xs text-gray-500 mb-1">Collection</label>
            <Input value={collectionInput} onChange={(e) => setCollectionInput(e.target.value)} placeholder="all collections" className="w-44" />
          </div>
          <div>
            <label className="block text-xs text-gray-500 mb-1">Client</label>
            <Input value={clientInput} onChange={(e) => setClientInput(e.target.value)} placeholder="all clients" className="w-44" />
          </div>
          <Button onClick={() => updateFilters({ collection: collectionInput.trim(), client: clientInput.trim() })}>Apply</Button>
          <Button variant="secondary" onClick={() => { setCollectionInput(''); setClientInput(''); updateFilters({ collection: '', client: '' }) }}>Clear</Button>
        </div>
      </div>

      {error && (
        <div className="mb-6 rounded-lg border border-red-200 bg-red-50 dark:bg-red-950/30 dark:border-red-900 p-4 text-sm text-red-700 dark:text-red-300">
          Memory behavior data is unavailable. Check MCP audit read-model health.
        </div>
      )}

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mb-8">
        <Metric title="Recall before save" value={percent(summary?.recall_before_save_rate)} sub="consult-before-write discipline" icon={Brain} color="text-green-600" />
        <Metric title="Repeat saves" value={percent(summary?.repeat_save_rate)} sub={`${summary?.save_without_room_or_hall_count ?? 0} missing room/hall`} icon={RefreshCw} color="text-orange-600" />
        <Metric title="Zero results" value={percent(summary?.zero_result_rate)} sub={`${percent(summary?.empty_recall_rate)} empty recall/search`} icon={Search} color="text-red-600" />
        <Metric title="Context / trajectory" value={bytes(summary?.context_bytes_per_trajectory)} sub={`${(summary?.memory_ops_per_trajectory ?? 0).toFixed(1)} memory ops avg`} icon={MessageCircle} color="text-indigo-600" />
        <Metric title="Trajectories" value={summary?.total_trajectories ?? 0} sub={`${summary?.total_events ?? 0} MCP events`} icon={Activity} color="text-blue-600" />
        <Metric title="Memory ops" value={summary?.memory_ops ?? 0} sub="search/recall/save/wake_up" icon={Database} color="text-purple-600" />
        <Metric title="Unknown hall" value={summary?.unknown_hall_error_count ?? 0} sub="save_memory validation failures" icon={AlertTriangle} color="text-amber-600" />
        <Metric title="Problem traces" value={problems.length} sub="blind/repeat/zero/error heavy" icon={AlertTriangle} color="text-pink-600" />
      </div>

      <div className="grid grid-cols-1 xl:grid-cols-2 gap-6">
        <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <h2 className="text-lg font-semibold mb-4">Recent trajectories</h2>
          <div className="space-y-3">
            {(trajectories.data?.trajectories ?? []).map((tr) => (
              <div key={tr.id} className="rounded-lg border border-gray-100 dark:border-gray-800 p-3">
                <div className="flex items-center justify-between gap-3">
                  <code className="text-xs truncate">{tr.id}</code>
                  <Badge variant={tr.counters.error_count ? 'error' : tr.counters.zero_result_count ? 'warning' : 'success'}>
                    {tr.event_count} events
                  </Badge>
                </div>
                <div className="mt-2 grid grid-cols-2 md:grid-cols-4 gap-2 text-xs text-gray-500">
                  <span>{tr.client_name || 'unknown client'}</span>
                  <span>{tr.collection || 'no collection'}</span>
                  <span>{tr.counters.recall_count} recall</span>
                  <span>{tr.counters.save_count} save</span>
                  <span>{tr.counters.search_count} search</span>
                  <span>{tr.counters.zero_result_count} zero</span>
                  <span>{tr.counters.error_count} errors</span>
                  <span>{bytes(tr.counters.request_bytes + tr.counters.response_bytes)}</span>
                </div>
              </div>
            ))}
            {trajectories.isSuccess && (trajectories.data?.trajectories ?? []).length === 0 && <p className="text-sm text-gray-400">No trajectories in this window.</p>}
          </div>
        </section>

        <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <h2 className="text-lg font-semibold mb-4">Problem trajectories</h2>
          <div className="space-y-3">
            {problems.slice(0, 20).map((tr) => (
              <div key={tr.id} className="rounded-lg border border-gray-100 dark:border-gray-800 p-3">
                <div className="flex items-center justify-between gap-3">
                  <code className="text-xs truncate">{tr.id}</code>
                  <Badge variant={tr.errors ? 'error' : 'warning'}>{tr.errors ? `${tr.errors} errors` : 'review'}</Badge>
                </div>
                <div className="mt-2 grid grid-cols-2 md:grid-cols-3 gap-2 text-xs text-gray-500">
                  <span>{tr.client_name || 'unknown client'}</span>
                  <span>{tr.collection || 'no collection'}</span>
                  <span>{tr.blind_saves} blind saves</span>
                  <span>{tr.repeat_saves} repeats</span>
                  <span>{tr.zero_results} zero results</span>
                  <span>{bytes(tr.context_bytes)}</span>
                </div>
              </div>
            ))}
            {problems.length === 0 && <p className="text-sm text-gray-400">No problem trajectories in this window.</p>}
          </div>
        </section>
      </div>
    </div>
  )
}

function Metric({ title, value, sub, icon: Icon, color }: { title: string; value: string | number; sub: string; icon: typeof Activity; color: string }) {
  return (
    <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4">
      <div className="flex items-center justify-between mb-2">
        <span className="text-sm text-gray-500">{title}</span>
        <Icon className={`h-4 w-4 ${color}`} />
      </div>
      <span className="text-2xl font-bold">{value}</span>
      <p className="text-xs text-gray-400 mt-1">{sub}</p>
    </div>
  )
}
