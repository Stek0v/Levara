'use client'

import { useState, useEffect } from 'react'
import { useParams, useRouter } from 'next/navigation'
import { levara } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { EmptyState } from '@/components/ui/empty-state'
import { ArrowLeft, Play, Trash2, FileText, ChevronLeft, ChevronRight } from 'lucide-react'

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
  const params = useParams()
  const router = useRouter()
  const datasetId = params.id as string

  const [dsName, setDsName] = useState('')
  const [records, setRecords] = useState<DataRecord[]>([])
  const [loading, setLoading] = useState(true)
  const [page, setPage] = useState(1)
  const [total, setTotal] = useState(0)
  const [search, setSearch] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [cognifyRunning, setCognifyRunning] = useState(false)
  const limit = 20

  useEffect(() => {
    const load = async () => {
      setLoading(true)
      try {
        const ds = await levara.datasets()
        const dataset = ds.data?.find((d) => d.id === datasetId)
        if (dataset) setDsName(dataset.name)

        const res = await fetch(`/api/v1/datasets/${datasetId}/data?page=${page}&limit=${limit}`, { credentials: 'include' }).then((r) => r.json())
        setRecords(res.data || res || [])
        setTotal(res.pagination?.total || (Array.isArray(res) ? res.length : 0))
      } catch {
        setRecords([])
      } finally {
        setLoading(false)
      }
    }
    load()
  }, [datasetId, page])

  const handleCognify = async () => {
    setCognifyRunning(true)
    try {
      const res = await levara.cognify({ dataset_id: datasetId, collection: dsName })
      const runId = res?.pipeline_run_id
      if (!runId) { setCognifyRunning(false); return }
      const poll = setInterval(async () => {
        try {
          const status = await levara.cognifyStatus(runId)
          if (['COMPLETED', 'FAILED', 'completed', 'failed'].includes(status.status)) {
            clearInterval(poll); setCognifyRunning(false)
          }
        } catch { clearInterval(poll); setCognifyRunning(false) }
      }, 3000)
    } catch { setCognifyRunning(false) }
  }

  const handleDelete = async (recordId: string) => {
    try {
      await fetch(`/api/v1/datasets/${datasetId}/data/${recordId}`, { method: 'DELETE', credentials: 'include' })
      setRecords(records.filter((r) => r.id !== recordId))
      setTotal((t) => t - 1)
    } catch (err) { alert(`Failed: ${err instanceof Error ? err.message : 'Error'}`) }
  }

  const handleBulkDelete = async () => {
    if (!selected.size || !confirm(`Delete ${selected.size} record(s)?`)) return
    for (const id of selected) await handleDelete(id)
    setSelected(new Set())
  }

  const toggleSelect = (id: string) => { const n = new Set(selected); n.has(id) ? n.delete(id) : n.add(id); setSelected(n) }
  const toggleAll = () => { selected.size === records.length ? setSelected(new Set()) : setSelected(new Set(records.map((r) => r.id))) }

  const totalPages = Math.ceil(total / limit)
  const filtered = search ? records.filter((r) => (r.name || r.id).toLowerCase().includes(search.toLowerCase())) : records

  if (loading) {
    return (
      <div>
        <h1 className="text-2xl font-bold mb-6">Dataset</h1>
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
          <p className="text-sm text-gray-500">{total} records</p>
        </div>
        <Button variant="secondary" size="sm" onClick={handleCognify} loading={cognifyRunning} disabled={cognifyRunning}>
          <Play className="h-4 w-4" /> Cognify
        </Button>
      </div>

      <div className="flex items-center gap-3 mb-4">
        <Input placeholder="Search by name..." value={search} onChange={(e) => setSearch(e.target.value)} className="w-64" />
        {selected.size > 0 && (
          <div className="flex items-center gap-2 ml-auto">
            <Badge>{selected.size} selected</Badge>
            <Button variant="danger" size="sm" onClick={handleBulkDelete}><Trash2 className="h-4 w-4" /> Delete</Button>
            <Button variant="ghost" size="sm" onClick={() => setSelected(new Set())}>Clear</Button>
          </div>
        )}
      </div>

      {filtered.length === 0 ? (
        <EmptyState icon={FileText} title="No records" description={search ? 'No match' : 'Upload files to add records'} />
      ) : (
        <>
          <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 overflow-hidden">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800">
                  <th className="w-10 px-3 py-2">
                    <input type="checkbox" checked={selected.size === records.length && records.length > 0} onChange={toggleAll} className="rounded" aria-label="Select all" />
                  </th>
                  <th className="text-left px-3 py-2 font-medium text-gray-500">Name</th>
                  <th className="text-left px-3 py-2 font-medium text-gray-500">Type</th>
                  <th className="text-left px-3 py-2 font-medium text-gray-500">Size</th>
                  <th className="text-left px-3 py-2 font-medium text-gray-500">Status</th>
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
                        {r.pipeline_status || 'pending'}
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
      )}
    </div>
  )
}
