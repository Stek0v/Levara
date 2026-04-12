'use client'

import { useState, useEffect } from 'react'
import { useParams, useRouter } from 'next/navigation'
import { levara } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import { EmptyState } from '@/components/ui/empty-state'
import { ArrowLeft, Play, Trash2, FileText, Download, ChevronLeft, ChevronRight } from 'lucide-react'

interface DataRecord {
  id: string
  metadata?: Record<string, unknown>
  created_at?: string
}

export default function DatasetDetailPage() {
  const params = useParams()
  const router = useRouter()
  const datasetId = params.id as string

  const [name, setName] = useState('')
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
        if (dataset) setName(dataset.name)

        const url = `${process.env.NEXT_PUBLIC_API_URL || ''}/api/v1/datasets/${datasetId}/data?page=${page}&limit=${limit}`
        const res = await fetch(url, { credentials: 'include' }).then((r) => r.json())
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
      const res = await levara.cognify({ dataset_id: datasetId })
      const runId = res.pipeline_run_id
      const poll = setInterval(async () => {
        const status = await levara.cognifyStatus(runId)
        if (['COMPLETED', 'FAILED', 'completed', 'failed'].includes(status.status)) {
          clearInterval(poll)
          setCognifyRunning(false)
        }
      }, 2000)
    } catch {
      setCognifyRunning(false)
    }
  }

  const handleDelete = async (recordId: string) => {
    try {
      await fetch(
        `${process.env.NEXT_PUBLIC_API_URL || ''}/api/v1/datasets/${datasetId}/data/${recordId}`,
        { method: 'DELETE', credentials: 'include' },
      )
      setRecords(records.filter((r) => r.id !== recordId))
      setTotal((t) => t - 1)
    } catch {}
  }

  const handleBulkDelete = async () => {
    if (!selected.size) return
    const msg = selected.size > 10
      ? `Delete ${selected.size} records? This cannot be undone.`
      : `Delete ${selected.size} record(s)?`
    if (!confirm(msg)) return
    for (const id of selected) {
      await handleDelete(id)
    }
    setSelected(new Set())
  }

  const toggleSelect = (id: string) => {
    const next = new Set(selected)
    next.has(id) ? next.delete(id) : next.add(id)
    setSelected(next)
  }

  const toggleAll = () => {
    if (selected.size === records.length) setSelected(new Set())
    else setSelected(new Set(records.map((r) => r.id)))
  }

  const totalPages = Math.ceil(total / limit)
  const filtered = search
    ? records.filter((r) => {
        const text = JSON.stringify(r.metadata || {}).toLowerCase()
        return text.includes(search.toLowerCase())
      })
    : records

  return (
    <div>
      {/* Header */}
      <div className="flex items-center gap-3 mb-6">
        <Button variant="ghost" size="sm" onClick={() => router.push('/datasets')}>
          <ArrowLeft className="h-4 w-4" />
        </Button>
        <div className="flex-1">
          <h1 className="text-2xl font-bold">{name || 'Dataset'}</h1>
          <p className="text-sm text-gray-500">{total} records</p>
        </div>
        <Button variant="secondary" size="sm" onClick={handleCognify} loading={cognifyRunning} disabled={cognifyRunning}>
          <Play className="h-4 w-4" /> Cognify
        </Button>
      </div>

      {/* Search + bulk actions */}
      <div className="flex items-center gap-3 mb-4">
        <Input
          placeholder="Search records..."
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="w-64"
        />
        {selected.size > 0 && (
          <div className="flex items-center gap-2 ml-auto">
            <Badge>{selected.size} selected</Badge>
            <Button variant="danger" size="sm" onClick={handleBulkDelete}>
              <Trash2 className="h-4 w-4" /> Delete selected
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setSelected(new Set())}>
              Clear
            </Button>
          </div>
        )}
      </div>

      {/* Records table */}
      {loading ? (
        <div className="space-y-2">
          {[...Array(5)].map((_, i) => <Skeleton key={i} className="h-16 rounded-lg" />)}
        </div>
      ) : filtered.length === 0 ? (
        <EmptyState
          icon={FileText}
          title="No records"
          description={search ? 'No records match your search' : 'Upload files to add records'}
        />
      ) : (
        <>
          <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 overflow-hidden">
            <table className="w-full text-sm">
              <thead>
                <tr className="border-b border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800">
                  <th className="w-10 px-3 py-2">
                    <input
                      type="checkbox"
                      checked={selected.size === records.length && records.length > 0}
                      onChange={toggleAll}
                      className="rounded"
                      aria-label="Select all"
                    />
                  </th>
                  <th className="text-left px-3 py-2 font-medium text-gray-500">ID</th>
                  <th className="text-left px-3 py-2 font-medium text-gray-500">Content</th>
                  <th className="text-left px-3 py-2 font-medium text-gray-500">Title</th>
                  <th className="w-20 px-3 py-2"></th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((r) => (
                  <tr key={r.id} className="border-b border-gray-100 dark:border-gray-800 hover:bg-gray-50 dark:hover:bg-gray-800/50">
                    <td className="px-3 py-2">
                      <input
                        type="checkbox"
                        checked={selected.has(r.id)}
                        onChange={() => toggleSelect(r.id)}
                        className="rounded"
                        aria-label={`Select ${r.id}`}
                      />
                    </td>
                    <td className="px-3 py-2">
                      <code className="text-xs text-gray-400 truncate block max-w-[120px]">{r.id}</code>
                    </td>
                    <td className="px-3 py-2">
                      <p className="text-gray-900 dark:text-gray-100 line-clamp-2 max-w-md">
                        {(r.metadata?.text as string)?.slice(0, 200) || '—'}
                      </p>
                    </td>
                    <td className="px-3 py-2 text-gray-500">
                      {(r.metadata?.title as string) || '—'}
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

          {/* Pagination */}
          {totalPages > 1 && (
            <div className="flex items-center justify-between mt-4">
              <p className="text-sm text-gray-500">
                Page {page} of {totalPages} ({total} records)
              </p>
              <div className="flex gap-1">
                <Button variant="ghost" size="sm" disabled={page <= 1} onClick={() => setPage(page - 1)}>
                  <ChevronLeft className="h-4 w-4" /> Prev
                </Button>
                <Button variant="ghost" size="sm" disabled={page >= totalPages} onClick={() => setPage(page + 1)}>
                  Next <ChevronRight className="h-4 w-4" />
                </Button>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  )
}
