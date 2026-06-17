'use client'

import { useEffect, useState, startTransition } from 'react'
import { useDatasets, useCreateDataset, useDeleteDataset, useUpload, useCognify } from '@/hooks/use-levara'
import { useCognifyProgress, type CognifyProgress } from '@/hooks/use-sse'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { Skeleton } from '@/components/ui/skeleton'
import { Database, Upload, Trash2, Plus, Loader2, CheckCircle, XCircle } from 'lucide-react'

interface UploadedFile {
  name: string
  dataset: string
  status: 'uploading' | 'processing' | 'ready' | 'error'
  cognifyRunId?: string
  progress?: CognifyProgress
}

export default function DatasetsPage() {
  const { data: datasetsRes, isLoading } = useDatasets()
  const datasets = datasetsRes?.data || []

  const createMutation = useCreateDataset()
  const deleteMutation = useDeleteDataset()
  const uploadMutation = useUpload()
  const cognifyMutation = useCognify()

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const [_dragOver, setDragOver] = useState(false)
  const [newName, setNewName] = useState('')
  const [showCreate, setShowCreate] = useState(false)
  const [targetDataset, setTargetDataset] = useState<string>('')
  const [uploadedFiles, setUploadedFiles] = useState<UploadedFile[]>([])
  const [activeCognifyRunId, setActiveCognifyRunId] = useState<string | null>(null)

  // SSE progress for active cognify
  const cognifyProgress = useCognifyProgress(activeCognifyRunId)

  // SSE-driven completion: when the backend emits `event: done` or the
  // progress payload transitions to a terminal status, useCognifyProgress
  // stamps `_complete` and/or sets `status` to COMPLETED/FAILED. We
  // reconcile the per-file upload state off that single source of truth
  // — no more parallel polling loop (T8).
  useEffect(() => {
    const d = cognifyProgress.data
    if (!d) return
    const terminal = d._complete || d.status === 'COMPLETED' || d.status === 'FAILED'
    if (!terminal) return
    const ok = d.status !== 'FAILED'
    startTransition(() => {
      setUploadedFiles((prev) =>
        prev.map((f) =>
          f.status === 'processing'
            ? { ...f, status: ok ? ('ready' as const) : ('error' as const) }
            : f,
        ),
      )
      setActiveCognifyRunId(null)
    })
  }, [cognifyProgress.data])

  const handleUpload = async (files: FileList | File[]) => {
    const fileArr = Array.from(files)
    if (!fileArr.length) return

    const dsName = targetDataset || undefined

    // Show uploading state
    setUploadedFiles((prev) => [
      ...fileArr.map((f) => ({ name: f.name, dataset: dsName || 'default', status: 'uploading' as const })),
      ...prev,
    ])

    try {
      const res = await uploadMutation.mutateAsync({ files: fileArr, datasetName: dsName })
      const r = res as Record<string, unknown>
      const dsId = r.dataset_id as string || ''
      const actualDsName = r.dataset_name as string || 'default'

      // Update to processing
      setUploadedFiles((prev) =>
        prev.map((f) => f.status === 'uploading' ? { ...f, dataset: actualDsName, status: 'processing' as const } : f)
      )

      // Auto-cognify. SSE (via useCognifyProgress below) drives both live
      // progress and terminal-state transitions — we no longer poll
      // /cognify/:id/status separately (T8). The effect watching
      // cognifyProgress.data picks up _complete / status=FAILED and
      // reconciles the uploadedFiles UI.
      if (dsId) {
        try {
          const cognifyRes = await cognifyMutation.mutateAsync({ dataset_id: dsId, collection: actualDsName })
          const runId = cognifyRes?.pipeline_run_id
          if (!runId) {
            setUploadedFiles((prev) => prev.map((f) => f.status === 'processing' ? { ...f, status: 'ready' as const } : f))
            return
          }
          setActiveCognifyRunId(runId)
        } catch {
          setUploadedFiles((prev) => prev.map((f) => f.status === 'processing' ? { ...f, status: 'error' as const } : f))
        }
      } else {
        setUploadedFiles((prev) => prev.map((f) => f.status === 'processing' ? { ...f, status: 'ready' as const } : f))
      }
    } catch (err) {
      setUploadedFiles((prev) => prev.map((f) => f.status === 'uploading' ? { ...f, status: 'error' as const } : f))
      alert(`Upload failed: ${err instanceof Error ? err.message : 'Unknown error'}`)
    }
  }

  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault(); setDragOver(false)
    if (e.dataTransfer.files.length) handleUpload(e.dataTransfer.files)
  }

  const handleCreate = async () => {
    if (!newName.trim()) return
    try { await createMutation.mutateAsync(newName.trim()); setNewName(''); setShowCreate(false) }
    catch (err) { alert(`Failed: ${err instanceof Error ? err.message : 'Error'}`) }
  }

  const handleDelete = async (id: string, name: string) => {
    if (!confirm(`Delete dataset "${name}"?`)) return
    try { await deleteMutation.mutateAsync(id) }
    catch (err) { alert(`Failed: ${err instanceof Error ? err.message : 'Error'}`) }
  }

  const statusIcon = (s: UploadedFile['status']) => {
    switch (s) {
      case 'uploading': return <Loader2 className="h-4 w-4 animate-spin text-blue-500" />
      case 'processing': return <Loader2 className="h-4 w-4 animate-spin text-amber-500" />
      case 'ready': return <CheckCircle className="h-4 w-4 text-green-500" />
      case 'error': return <XCircle className="h-4 w-4 text-red-500" />
    }
  }

  const statusLabel = (s: UploadedFile['status']) => {
    switch (s) {
      case 'uploading': return 'Uploading...'
      case 'processing': return cognifyProgress.data?.stage ? `${cognifyProgress.data.stage} (${cognifyProgress.data.entities_extracted || 0} entities)` : 'Processing...'
      case 'ready': return 'Ready to search'
      case 'error': return 'Failed'
    }
  }

  if (isLoading) {
    return (
      <div>
        <h1 className="text-2xl font-bold mb-6">Datasets</h1>
        <div className="space-y-3">{[...Array(3)].map((_, i) => <Skeleton key={i} className="h-20 rounded-lg" />)}</div>
      </div>
    )
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h1 className="text-2xl font-bold">Datasets</h1>
        <div className="flex gap-2">
          <Button variant="secondary" size="sm" onClick={() => setShowCreate(!showCreate)}>
            <Plus className="h-4 w-4" /> New Dataset
          </Button>
        </div>
      </div>

      {/* Create form */}
      {showCreate && (
        <div className="mb-4 p-4 bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800">
          <div className="flex gap-2">
            <Input placeholder="Dataset name" value={newName} onChange={(e) => setNewName(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && handleCreate()} />
            <Button onClick={handleCreate} disabled={!newName.trim()} loading={createMutation.isPending}>Create</Button>
            <Button variant="ghost" onClick={() => setShowCreate(false)}>Cancel</Button>
          </div>
        </div>
      )}

      {/* Upload zone with dataset selector */}
      <div className="mb-6 p-6 bg-white dark:bg-gray-900 rounded-lg border-2 border-dashed transition-colors
        ${_dragOver ? 'border-blue-500 bg-blue-50 dark:bg-blue-900/10' : 'border-gray-300 dark:border-gray-700'}"
        onDragOver={(e) => { e.preventDefault(); setDragOver(true) }} onDragLeave={() => setDragOver(false)} onDrop={handleDrop}>
        <div className="flex flex-col md:flex-row items-center gap-4">
          <Upload className="h-8 w-8 text-gray-400 flex-shrink-0" />
          <div className="flex-1 text-center md:text-left">
            <p className="text-sm font-medium text-gray-700 dark:text-gray-300">
              {uploadMutation.isPending ? 'Uploading...' : 'Drag & drop files or click Upload'}
            </p>
            <p className="text-xs text-gray-400 mt-1">PDF, DOCX, PPTX, XLSX, HTML, EPUB, TXT, MD, CSV</p>
          </div>
          <div className="flex items-center gap-2">
            <select value={targetDataset} onChange={(e) => setTargetDataset(e.target.value)}
              className="h-9 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 text-sm"
              aria-label="Target dataset">
              <option value="">Default dataset</option>
              {datasets.map((ds) => <option key={ds.id} value={ds.name}>{ds.name}</option>)}
            </select>
            <label className="cursor-pointer inline-flex items-center gap-2 h-9 px-4 text-sm font-medium rounded-md bg-blue-600 text-white hover:bg-blue-700 transition-colors">
              <Upload className="h-4 w-4" /> Upload
              <input type="file" multiple className="hidden"
                onChange={(e) => e.target.files && handleUpload(e.target.files)}
                accept=".pdf,.docx,.pptx,.xlsx,.html,.htm,.epub,.odt,.txt,.md,.csv,.json" />
            </label>
          </div>
        </div>
      </div>

      {/* Upload progress */}
      {uploadedFiles.length > 0 && (
        <div className="mb-6">
          <h2 className="text-sm font-medium text-gray-500 mb-2">Recent Uploads</h2>
          <div className="space-y-2">
            {uploadedFiles.map((f, i) => (
              <div key={i} className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-3">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2">
                    {statusIcon(f.status)}
                    <span className="font-medium text-sm">{f.name}</span>
                    <Badge variant="default" className="text-[10px]">{f.dataset}</Badge>
                  </div>
                  <span className="text-xs text-gray-400">{statusLabel(f.status)}</span>
                </div>
                {/* Progress bar for processing */}
                {f.status === 'processing' && cognifyProgress.data && (
                  <div className="mt-2">
                    <div className="w-full bg-gray-200 dark:bg-gray-700 rounded-full h-1.5">
                      <div className="bg-amber-500 h-1.5 rounded-full transition-all"
                        style={{ width: cognifyProgress.data.items_total ? `${((cognifyProgress.data.items_processed || 0) / cognifyProgress.data.items_total) * 100}%` : '30%' }} />
                    </div>
                    <p className="text-[10px] text-gray-400 mt-1">
                      {cognifyProgress.data.stage} • {cognifyProgress.data.entities_extracted || 0} entities • {cognifyProgress.data.edges_extracted || 0} edges
                    </p>
                  </div>
                )}
              </div>
            ))}
            <button onClick={() => setUploadedFiles([])} className="text-xs text-gray-400 hover:text-gray-600">Clear history</button>
          </div>
        </div>
      )}

      {/* Dataset list */}
      {datasets.length === 0 && uploadedFiles.length === 0 ? (
        <EmptyState icon={Database} title="No datasets yet" description="Upload files or create a new dataset"
          action={{ label: 'Create Dataset', onClick: () => setShowCreate(true) }} />
      ) : (
        <div className="space-y-3">
          {datasets.map((ds) => (
            <div key={ds.id}
              className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4 flex items-center justify-between cursor-pointer hover:border-blue-300 dark:hover:border-blue-700 transition-colors"
              onClick={() => window.location.href = `/datasets/${ds.id}`}>
              <div>
                <div className="flex items-center gap-2">
                  <Database className="h-4 w-4 text-gray-400" />
                  <span className="font-medium">{ds.name}</span>
                  <Badge variant={ds.record_count > 0 ? 'success' : 'default'}>{ds.record_count} records</Badge>
                </div>
                <p className="text-xs text-gray-400 mt-1">Created {new Date(ds.created_at).toLocaleDateString()}</p>
              </div>
              <div className="flex gap-1" onClick={(e) => e.stopPropagation()}>
                <Button variant="ghost" size="sm" onClick={() => handleDelete(ds.id, ds.name)} title="Delete">
                  <Trash2 className="h-4 w-4 text-red-500" />
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
