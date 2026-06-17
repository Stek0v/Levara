'use client'

import { useMemo, useState } from 'react'
import { useCognify, useCreateDataset, useDatasets, useHealthDetails, useSearch, useUpload } from '@/hooks/use-levara'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { CheckCircle2, Database, FileUp, Search, ServerCog, Sparkles } from 'lucide-react'

const profiles = [
  { id: 'solo', name: 'Solo', description: 'Local memory, local docs, one developer workflow.' },
  { id: 'team', name: 'Team', description: 'Shared projects, sync, workspace audit, several developers.' },
  { id: 'enterprise', name: 'Enterprise', description: 'RBAC, audit, policy, stronger operational monitoring.' },
]

function serviceStatus(status?: string) {
  if (status === 'connected' || status === 'ok' || status === 'ready' || status === 'configured') return 'success'
  if (status === 'unreachable' || status === 'error') return 'error'
  return 'default'
}

export default function OnboardingPage() {
  const [profile, setProfile] = useState('solo')
  const [datasetName, setDatasetName] = useState('project-docs')
  const [selectedDatasetId, setSelectedDatasetId] = useState('')
  const [files, setFiles] = useState<File[]>([])
  const [question, setQuestion] = useState('What are the most important facts in this project?')

  const health = useHealthDetails()
  const datasets = useDatasets()
  const createDataset = useCreateDataset()
  const upload = useUpload()
  const cognify = useCognify()
  const search = useSearch()

  const datasetOptions = datasets.data?.data ?? []
  const activeDatasetId = selectedDatasetId || datasetOptions.find((d) => d.name === datasetName)?.id || ''
  const activeDatasetName = datasetOptions.find((d) => d.id === activeDatasetId)?.name || datasetName
  const canUpload = files.length > 0 && activeDatasetName.trim() !== ''
  const canCognify = Boolean(activeDatasetId || activeDatasetName)

  const dependencyItems = useMemo(() => {
    const services = health.data?.services ?? {}
    return ['database', 'storage', 'embed', 'llm', 'collections', 'neo4j', 'ocr'].map((name) => ({
      name,
      status: String(services[name]?.status || 'not_configured'),
      model: String(services[name]?.provider || services[name]?.backend || services[name]?.model || services[name]?.endpoint || services[name]?.url || ''),
    }))
  }, [health.data?.services])

  const handleCreateDataset = async () => {
    if (!datasetName.trim()) return
    const created = await createDataset.mutateAsync(datasetName.trim())
    setSelectedDatasetId(created.id)
  }

  const handleUpload = async () => {
    if (!canUpload) return
    const res = await upload.mutateAsync({ files, datasetName: activeDatasetName })
    if (res.dataset_id) setSelectedDatasetId(res.dataset_id)
  }

  const handleCognify = () => {
    cognify.mutate({
      dataset_id: activeDatasetId || undefined,
      collection: activeDatasetName,
      mode: 'rag',
    })
  }

  const handleFirstSearch = () => {
    if (!question.trim()) return
    search.mutate({
      query_text: question.trim(),
      collection: activeDatasetName,
      query_type: 'HYBRID',
      top_k: 5,
    })
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h1 className="text-2xl font-bold">Onboarding</h1>
        <Badge variant="info">guided setup</Badge>
      </div>

      <div className="space-y-6">
        <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><Sparkles className="h-5 w-5 text-blue-600" /> Profile</h2>
          <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
            {profiles.map((item) => (
              <button
                key={item.id}
                onClick={() => setProfile(item.id)}
                className={`rounded-lg border p-4 text-left ${profile === item.id ? 'border-blue-500 bg-blue-50 dark:bg-blue-950/30' : 'border-gray-200 dark:border-gray-800'}`}
              >
                <span className="font-medium">{item.name}</span>
                <p className="mt-1 text-sm text-gray-500">{item.description}</p>
              </button>
            ))}
          </div>
        </section>

        <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><ServerCog className="h-5 w-5 text-green-600" /> Dependency Check</h2>
          <div className="grid grid-cols-1 md:grid-cols-5 gap-3">
            {dependencyItems.map((service) => (
              <div key={service.name} className="rounded-md border border-gray-100 dark:border-gray-800 p-3">
                <div className="flex items-center justify-between gap-2">
                  <span className="text-sm font-medium">{service.name}</span>
                  <Badge variant={serviceStatus(service.status)}>{service.status}</Badge>
                </div>
                <p className="mt-2 text-xs text-gray-500 truncate">{service.model}</p>
              </div>
            ))}
          </div>
        </section>

        <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><Database className="h-5 w-5 text-purple-600" /> Dataset</h2>
          <div className="grid grid-cols-1 md:grid-cols-[1fr_auto_1fr] gap-3">
            <Input label="Dataset name" value={datasetName} onChange={(e) => setDatasetName(e.target.value)} placeholder="project-docs" />
            <div className="flex items-end">
              <Button onClick={handleCreateDataset} loading={createDataset.isPending} disabled={!datasetName.trim()}>
                Create
              </Button>
            </div>
            <label className="block">
              <span className="block text-sm font-medium mb-1">Existing dataset</span>
              <select value={selectedDatasetId} onChange={(e) => setSelectedDatasetId(e.target.value)} className="h-9 w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 text-sm">
                <option value="">Use by name</option>
                {datasetOptions.map((dataset) => <option key={dataset.id} value={dataset.id}>{dataset.name}</option>)}
              </select>
            </label>
          </div>
        </section>

        <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><FileUp className="h-5 w-5 text-cyan-600" /> Upload And Index</h2>
          <div className="space-y-3">
            <input
              type="file"
              multiple
              onChange={(e) => setFiles(Array.from(e.target.files ?? []))}
              className="block w-full text-sm"
            />
            <div className="flex gap-2 flex-wrap">
              <Button onClick={handleUpload} loading={upload.isPending} disabled={!canUpload}>
                <FileUp className="h-4 w-4" />
                Upload
              </Button>
              <Button variant="secondary" onClick={handleCognify} loading={cognify.isPending} disabled={!canCognify}>
                <CheckCircle2 className="h-4 w-4" />
                Cognify RAG
              </Button>
            </div>
            {upload.isSuccess && <p className="text-xs text-green-600">Uploaded {upload.data.items?.length ?? files.length} files.</p>}
            {cognify.isSuccess && <p className="text-xs text-green-600">Cognify started: {cognify.data.pipeline_run_id || cognify.data.status}</p>}
            {(upload.isError || cognify.isError) && <p className="text-xs text-red-600">Upload or cognify failed.</p>}
          </div>
        </section>

        <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><Search className="h-5 w-5 text-amber-600" /> First RAG Query</h2>
          <div className="space-y-3">
            <div className="flex gap-2">
              <Input value={question} onChange={(e) => setQuestion(e.target.value)} placeholder="Ask your first project question" />
              <Button onClick={handleFirstSearch} loading={search.isPending} disabled={!question.trim()}>
                <Search className="h-4 w-4" />
                Ask
              </Button>
            </div>
            {search.data && (
              <pre className="max-h-72 overflow-auto rounded-md bg-gray-50 dark:bg-gray-950 p-3 text-xs">{JSON.stringify(search.data, null, 2)}</pre>
            )}
            {search.isError && <p className="text-xs text-red-600">{search.error instanceof Error ? search.error.message : 'Search failed'}</p>}
          </div>
        </section>
      </div>
    </div>
  )
}
