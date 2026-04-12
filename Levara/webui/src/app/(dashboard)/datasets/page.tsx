'use client'

import { useState } from 'react'
import { useDatasets, useCreateDataset, useDeleteDataset, useUpload, useCognify } from '@/hooks/use-levara'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { Skeleton } from '@/components/ui/skeleton'
import { Database, Upload, Trash2, Play, Plus } from 'lucide-react'

export default function DatasetsPage() {
  const { data: datasetsRes, isLoading } = useDatasets()
  const datasets = datasetsRes?.data || []

  const createMutation = useCreateDataset()
  const deleteMutation = useDeleteDataset()
  const uploadMutation = useUpload()
  const cognifyMutation = useCognify()

  const [dragOver, setDragOver] = useState(false)
  const [newName, setNewName] = useState('')
  const [showCreate, setShowCreate] = useState(false)
  const [cognifyRunning, setCognifyRunning] = useState<string | null>(null)
  const [uploadedFiles, setUploadedFiles] = useState<{ name: string; status: 'processing' | 'ready' | 'error' }[]>([])

  const handleUpload = async (files: FileList | File[]) => {
    const fileArr = Array.from(files)
    if (!fileArr.length) return

    setUploadedFiles((prev) => [
      ...fileArr.map((f) => ({ name: f.name, status: 'processing' as const })),
      ...prev,
    ])

    try {
      const res = await uploadMutation.mutateAsync(fileArr)
      const r = res as Record<string, unknown>
      const dsId = r.dataset_id as string || ''

      // Auto-cognify
      if (dsId) {
        setCognifyRunning(dsId)
        try {
          const cognifyRes = await cognifyMutation.mutateAsync({ dataset_id: dsId })
          const runId = cognifyRes.pipeline_run_id
          const { cognifyStatus } = await import('@/lib/api').then((m) => m.levara)
          const poll = setInterval(async () => {
            try {
              const status = await cognifyStatus(runId)
              if (['COMPLETED', 'FAILED', 'completed', 'failed'].includes(status.status)) {
                clearInterval(poll)
                setCognifyRunning(null)
                setUploadedFiles((prev) =>
                  prev.map((f) => ({ ...f, status: status.status.toLowerCase().includes('complete') ? 'ready' as const : 'error' as const }))
                )
              }
            } catch { clearInterval(poll); setCognifyRunning(null) }
          }, 2000)
        } catch { setCognifyRunning(null) }
      } else {
        setUploadedFiles((prev) => prev.map((f) => ({ ...f, status: 'ready' as const })))
      }
    } catch (err) {
      setUploadedFiles((prev) => prev.map((f) => ({ ...f, status: 'error' as const })))
      alert(`Upload failed: ${err instanceof Error ? err.message : 'Unknown error'}`)
    }
  }

  const handleDrop = (e: React.DragEvent) => {
    e.preventDefault()
    setDragOver(false)
    if (e.dataTransfer.files.length) handleUpload(e.dataTransfer.files)
  }

  const handleCreate = async () => {
    if (!newName.trim()) return
    try {
      await createMutation.mutateAsync(newName.trim())
      setNewName('')
      setShowCreate(false)
    } catch (err) {
      alert(`Failed: ${err instanceof Error ? err.message : 'Unknown error'}`)
    }
  }

  const handleDelete = async (id: string, name: string) => {
    if (!confirm(`Delete dataset "${name}"?`)) return
    try { await deleteMutation.mutateAsync(id) }
    catch (err) { alert(`Failed: ${err instanceof Error ? err.message : 'Unknown error'}`) }
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
          <label className="cursor-pointer inline-flex items-center gap-2 h-8 px-3 text-sm font-medium rounded-md bg-blue-600 text-white hover:bg-blue-700 transition-colors">
            <Upload className="h-4 w-4" /> Upload Files
            <input type="file" multiple className="hidden"
              onChange={(e) => e.target.files && handleUpload(e.target.files)}
              accept=".pdf,.docx,.pptx,.xlsx,.html,.htm,.epub,.odt,.txt,.md,.csv,.json" />
          </label>
        </div>
      </div>

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

      <div onDragOver={(e) => { e.preventDefault(); setDragOver(true) }} onDragLeave={() => setDragOver(false)} onDrop={handleDrop}
        className={`mb-6 border-2 border-dashed rounded-lg p-8 text-center transition-colors ${dragOver ? 'border-blue-500 bg-blue-50 dark:bg-blue-900/10' : 'border-gray-300 dark:border-gray-700'} ${uploadMutation.isPending ? 'opacity-50 pointer-events-none' : ''}`}>
        <Upload className="h-8 w-8 mx-auto text-gray-400 mb-2" />
        <p className="text-sm text-gray-500">
          {uploadMutation.isPending ? 'Uploading...' : 'Drag & drop files here (PDF, DOCX, PPTX, XLSX, HTML, EPUB, TXT, MD, CSV)'}
        </p>
      </div>

      {uploadedFiles.length > 0 && (
        <div className="mb-4">
          <h2 className="text-sm font-medium text-gray-500 mb-2">Recent Uploads</h2>
          <div className="space-y-2">
            {uploadedFiles.map((f, i) => (
              <div key={i} className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-3 flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <Database className="h-4 w-4 text-gray-400" />
                  <span className="font-medium text-sm">{f.name}</span>
                  <Badge variant={f.status === 'ready' ? 'success' : f.status === 'error' ? 'error' : 'warning'}>
                    {f.status === 'processing' ? '⏳ processing...' : f.status === 'ready' ? '✓ ready' : '✗ error'}
                  </Badge>
                </div>
              </div>
            ))}
          </div>
        </div>
      )}

      {datasets.length === 0 && uploadedFiles.length === 0 ? (
        <EmptyState icon={Database} title="No datasets yet" description="Upload files or create a new dataset to get started"
          action={{ label: 'Upload Files', onClick: () => document.querySelector<HTMLInputElement>('input[type=file]')?.click() }} />
      ) : (
        <div className="space-y-3">
          {datasets.map((ds) => (
            <div key={ds.id} className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4 flex items-center justify-between cursor-pointer hover:border-blue-300 dark:hover:border-blue-700 transition-colors"
              onClick={() => window.location.href = `/datasets/${ds.id}`}>
              <div>
                <div className="flex items-center gap-2">
                  <Database className="h-4 w-4 text-gray-400" />
                  <span className="font-medium">{ds.name}</span>
                  <Badge>{ds.record_count} records</Badge>
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
