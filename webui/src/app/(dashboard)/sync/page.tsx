'use client'

import { useMemo, useState } from 'react'
import { useRunSync, useSyncManifest, useSyncStatus } from '@/hooks/use-levara'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Database, RefreshCw, UploadCloud, DownloadCloud } from 'lucide-react'

const SYNC_TYPES = ['memories', 'interactions', 'graph', 'collections']

function splitCSV(value: string) {
  return value.split(',').map((v) => v.trim()).filter(Boolean)
}

function toggleItem(items: string[], item: string) {
  return items.includes(item) ? items.filter((x) => x !== item) : [...items, item]
}

export default function SyncPage() {
  const [remoteURL, setRemoteURL] = useState('http://10.23.0.53:8080/api/v1')
  const [direction, setDirection] = useState<'pull' | 'push'>('pull')
  const [types, setTypes] = useState<string[]>(['memories', 'interactions', 'graph'])
  const [since, setSince] = useState('')
  const [collections, setCollections] = useState('')

  const manifest = useSyncManifest()
  const status = useSyncStatus(20)
  const syncMutation = useRunSync()

  const selectedCollections = useMemo(() => splitCSV(collections), [collections])
  const canRun = remoteURL.trim() !== '' && types.length > 0

  const run = () => {
    if (!canRun) return
    syncMutation.mutate({
      remote_url: remoteURL.trim().replace(/\/$/, ''),
      direction,
      types,
      since: since.trim() || undefined,
      collections: types.includes('collections') ? selectedCollections : undefined,
    })
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h1 className="text-2xl font-bold">Sync</h1>
        <Badge variant={status.data?.error ? 'error' : 'success'}>{status.data?.error ? 'degraded' : 'ready'}</Badge>
      </div>

      <div className="grid grid-cols-1 xl:grid-cols-[1.1fr_0.9fr] gap-6">
        <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><RefreshCw className="h-5 w-5 text-blue-600" /> Run Sync</h2>
          <div className="space-y-4">
            <Input label="Remote API URL" value={remoteURL} onChange={(e) => setRemoteURL(e.target.value)} placeholder="http://host:8080/api/v1" />
            <div className="flex gap-2">
              <Button variant={direction === 'pull' ? 'primary' : 'secondary'} onClick={() => setDirection('pull')}>
                <DownloadCloud className="h-4 w-4" />
                Pull
              </Button>
              <Button variant={direction === 'push' ? 'primary' : 'secondary'} onClick={() => setDirection('push')}>
                <UploadCloud className="h-4 w-4" />
                Push
              </Button>
            </div>
            <div>
              <label className="block text-sm font-medium mb-2">Types</label>
              <div className="flex gap-2 flex-wrap">
                {SYNC_TYPES.map((type) => (
                  <button
                    key={type}
                    onClick={() => setTypes((current) => toggleItem(current, type))}
                    className={`px-3 py-1.5 rounded-md text-sm ${types.includes(type) ? 'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200' : 'bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-300'}`}
                  >
                    {type}
                  </button>
                ))}
              </div>
            </div>
            <Input label="Since" value={since} onChange={(e) => setSince(e.target.value)} placeholder="2026-06-17T00:00:00Z" />
            {types.includes('collections') && (
              <Input label="Collections" value={collections} onChange={(e) => setCollections(e.target.value)} placeholder="docs,levara,project_x" />
            )}
            <Button onClick={run} loading={syncMutation.isPending} disabled={!canRun}>
              <RefreshCw className="h-4 w-4" />
              Run {direction}
            </Button>
            {syncMutation.isError && <p className="text-xs text-red-600">{syncMutation.error instanceof Error ? syncMutation.error.message : 'Sync failed'}</p>}
            {syncMutation.data && (
              <pre className="max-h-72 overflow-auto rounded-md bg-gray-50 dark:bg-gray-950 p-3 text-xs">{JSON.stringify(syncMutation.data, null, 2)}</pre>
            )}
          </div>
        </section>

        <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><Database className="h-5 w-5 text-green-600" /> Local Manifest</h2>
          <div className="space-y-3 text-sm">
            <div className="flex justify-between"><span className="text-gray-500">Version</span><span className="truncate">{manifest.data?.version || '-'}</span></div>
            <div className="flex justify-between"><span className="text-gray-500">Embed model</span><span className="truncate">{manifest.data?.embed_model || '-'}</span></div>
            <div className="flex justify-between"><span className="text-gray-500">Embed dim</span><span>{manifest.data?.embed_dim || 0}</span></div>
            <div className="grid grid-cols-2 gap-2 pt-2">
              <Badge variant="info">memories {manifest.data?.memories?.count ?? 0}</Badge>
              <Badge variant="info">interactions {manifest.data?.interactions?.count ?? 0}</Badge>
              <Badge variant="info">nodes {manifest.data?.graph_nodes?.count ?? 0}</Badge>
              <Badge variant="info">edges {manifest.data?.graph_edges?.count ?? 0}</Badge>
            </div>
            <div className="pt-3">
              <p className="text-xs font-medium text-gray-500 mb-2">Collections</p>
              <div className="space-y-2">
                {(manifest.data?.collections ?? []).slice(0, 10).map((collection) => (
                  <div key={collection.name} className="flex items-center justify-between gap-3 rounded-md border border-gray-100 dark:border-gray-800 px-3 py-2">
                    <span className="truncate">{collection.name}</span>
                    <span className="text-xs text-gray-500">{collection.records} rec / dim {collection.dim}</span>
                  </div>
                ))}
                {manifest.isSuccess && (manifest.data?.collections ?? []).length === 0 && <p className="text-sm text-gray-400">No collections.</p>}
              </div>
            </div>
          </div>
        </section>
      </div>

      <section className="mt-6 bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
        <h2 className="text-lg font-semibold mb-4">Recent Sync Events</h2>
        <div className="grid grid-cols-1 md:grid-cols-2 gap-3 mb-4">
          {Object.entries(status.data?.by_direction ?? {}).map(([dir, item]) => (
            <div key={dir} className="rounded-md border border-gray-100 dark:border-gray-800 p-3">
              <div className="flex items-center justify-between">
                <Badge variant={dir === 'pull' ? 'info' : 'success'}>{dir}</Badge>
                <span className="text-sm">{item.count} runs</span>
              </div>
              <p className="mt-2 text-xs text-gray-500 truncate">{item.last_remote || '-'}</p>
              <p className="text-xs text-gray-400">{item.last_at ? new Date(item.last_at).toLocaleString() : ''}</p>
            </div>
          ))}
        </div>
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-gray-500">
              <tr>
                <th className="py-2 pr-3">Direction</th>
                <th className="py-2 pr-3">Remote</th>
                <th className="py-2 pr-3">Types</th>
                <th className="py-2 pr-3">At</th>
              </tr>
            </thead>
            <tbody>
              {(status.data?.events ?? []).map((event) => (
                <tr key={event.id} className="border-t border-gray-100 dark:border-gray-800">
                  <td className="py-2 pr-3"><Badge variant={event.direction === 'pull' ? 'info' : 'success'}>{event.direction}</Badge></td>
                  <td className="py-2 pr-3 truncate max-w-md">{event.remote}</td>
                  <td className="py-2 pr-3">{(event.types ?? []).join(', ') || '-'}</td>
                  <td className="py-2 pr-3 text-gray-500">{event.at ? new Date(event.at).toLocaleString() : '-'}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {status.isSuccess && (status.data?.events ?? []).length === 0 && <p className="text-sm text-gray-400">No sync events yet.</p>}
        </div>
      </section>
    </div>
  )
}
