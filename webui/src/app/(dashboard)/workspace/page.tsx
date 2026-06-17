'use client'

import { useMemo, useState } from 'react'
import {
  useWorkspaceArtifacts,
  useWorkspaceAudit,
  useWorkspaceConflicts,
  useWorkspaceIndex,
  useWorkspaceJobs,
  useWorkspaceManifest,
  useWorkspaceOps,
  useWorkspaceRead,
  useWorkspaceReindex,
  useWorkspaceRetryJob,
  useWorkspaceSearch,
  useWorkspaceWrite,
} from '@/hooks/use-levara'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Input } from '@/components/ui/input'
import { AlertTriangle, BookOpen, Files, History, RefreshCw, RotateCcw, Search, Upload } from 'lucide-react'

function splitLines(value: string) {
  return value.split('\n').map((v) => v.trim()).filter(Boolean)
}

function statusVariant(status?: string) {
  if (status === 'completed' || status === 'success') return 'success'
  if (status === 'failed' || status === 'dead_letter' || status === 'denied') return 'error'
  if (status === 'pending' || status === 'running') return 'warning'
  return 'default'
}

function resultPath(result: Record<string, unknown>) {
  return String(result.path || result.Path || result.source_path || result.id || 'unknown')
}

export default function WorkspacePage() {
  const [projectId, setProjectId] = useState('')
  const [branch, setBranch] = useState('main')
  const [generation, setGeneration] = useState('main')
  const [path, setPath] = useState('docs/note.md')
  const [title, setTitle] = useState('')
  const [text, setText] = useState('')
  const [reindexPaths, setReindexPaths] = useState('')
  const [searchQuery, setSearchQuery] = useState('')
  const [readPath, setReadPath] = useState('docs/note.md')
  const [artifactKind, setArtifactKind] = useState('')
  const [jobStatus, setJobStatus] = useState('')

  const scope = useMemo(() => ({
    project_id: projectId.trim() || undefined,
    branch: branch.trim() || 'main',
  }), [projectId, branch])
  const canUseWorkspace = Boolean(scope.project_id)

  const ops = useWorkspaceOps(scope)
  const manifest = useWorkspaceManifest(scope)
  const jobs = useWorkspaceJobs({ ...scope, status: jobStatus || undefined })
  const artifacts = useWorkspaceArtifacts({ ...scope, kind: artifactKind || undefined })
  const conflicts = useWorkspaceConflicts(scope)
  const audit = useWorkspaceAudit({ ...scope, limit: 20 })
  const indexMutation = useWorkspaceIndex()
  const writeMutation = useWorkspaceWrite()
  const reindexMutation = useWorkspaceReindex()
  const readMutation = useWorkspaceRead()
  const searchMutation = useWorkspaceSearch()
  const retryMutation = useWorkspaceRetryJob()

  const handleIndex = () => {
    if (!canUseWorkspace || !generation.trim() || !path.trim() || !text.trim()) return
    indexMutation.mutate({
      project_id: projectId.trim(),
      branch: scope.branch,
      generation: generation.trim(),
      path: path.trim(),
      title: title.trim() || undefined,
      text,
      activate_generation: true,
    })
  }

  const handleWrite = () => {
    if (!canUseWorkspace || !generation.trim() || !path.trim() || !text.trim()) return
    writeMutation.mutate({
      project_id: projectId.trim(),
      branch: scope.branch,
      generation: generation.trim(),
      path: path.trim(),
      title: title.trim() || undefined,
      text,
      index: true,
      activate_generation: true,
    })
  }

  const handleReindex = () => {
    const paths = splitLines(reindexPaths)
    if (!canUseWorkspace || !generation.trim() || paths.length === 0) return
    reindexMutation.mutate({
      project_id: projectId.trim(),
      branch: scope.branch,
      generation: generation.trim(),
      paths,
      activate_generation: true,
    })
  }

  const handleRead = (targetPath = readPath) => {
    if (!canUseWorkspace || !targetPath.trim()) return
    setReadPath(targetPath.trim())
    readMutation.mutate({
      project_id: projectId.trim(),
      branch: scope.branch,
      path: targetPath.trim(),
    })
  }

  const handleSearch = () => {
    if (!canUseWorkspace || !searchQuery.trim()) return
    searchMutation.mutate({
      project_id: projectId.trim(),
      branch: scope.branch,
      generation: generation.trim() || undefined,
      search_query: searchQuery.trim(),
      search_type: 'HYBRID',
      mode: 'rag',
      top_k: 8,
    })
  }

  const conflictCount =
    (conflicts.data?.dirty_paths?.length ?? 0) +
    (conflicts.data?.unindexed_paths?.length ?? 0) +
    (conflicts.data?.missing_indexed_paths?.length ?? 0)

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h1 className="text-2xl font-bold">Workspace</h1>
        <Badge variant={canUseWorkspace ? 'success' : 'default'}>{canUseWorkspace ? 'scoped' : 'set project_id'}</Badge>
      </div>

      <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5 mb-6">
        <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><Files className="h-5 w-5 text-blue-600" /> Project Scope</h2>
        <div className="grid grid-cols-1 md:grid-cols-4 gap-3">
          <Input label="Project ID" value={projectId} onChange={(e) => setProjectId(e.target.value)} placeholder="payments" />
          <Input label="Branch" value={branch} onChange={(e) => setBranch(e.target.value)} placeholder="main" />
          <Input label="Generation" value={generation} onChange={(e) => setGeneration(e.target.value)} placeholder="main" />
          <Input label="Job status" value={jobStatus} onChange={(e) => setJobStatus(e.target.value)} placeholder="failed" />
        </div>
      </section>

      {!canUseWorkspace ? (
        <EmptyState icon={Files} title="Workspace scope required" description="Enter a project_id to load manifest, jobs, search, read, and audit tools." />
      ) : (
        <div className="space-y-6">
          <div className="grid grid-cols-1 xl:grid-cols-3 gap-6">
            <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
              <h2 className="text-lg font-semibold mb-4">Operational Status</h2>
              <div className="space-y-3 text-sm">
                <div className="flex justify-between"><span className="text-gray-500">Jobs</span><span>{ops.data?.jobs?.total ?? 0}</span></div>
                <div className="flex justify-between"><span className="text-gray-500">Dead letters</span><span>{ops.data?.jobs?.dead_letter_count ?? 0}</span></div>
                <div className="flex justify-between"><span className="text-gray-500">Audit events</span><span>{ops.data?.audit?.total_events ?? 0}</span></div>
                <div className="flex justify-between"><span className="text-gray-500">Audit files</span><span>{ops.data?.audit?.files ?? 0}</span></div>
                {ops.data?.jobs?.by_status && (
                  <div className="flex gap-2 flex-wrap pt-2">
                    {Object.entries(ops.data.jobs.by_status).map(([status, count]) => (
                      <Badge key={status} variant={statusVariant(status)}>{status}: {count}</Badge>
                    ))}
                  </div>
                )}
              </div>
            </section>

            <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
              <h2 className="text-lg font-semibold mb-4">Manifest</h2>
              <div className="space-y-3 text-sm">
                <div className="flex justify-between gap-3"><span className="text-gray-500">Active generation</span><span className="truncate">{manifest.data?.active_generation || '-'}</span></div>
                <div className="flex justify-between"><span className="text-gray-500">Chunks</span><span>{manifest.data?.chunks_count ?? manifest.data?.chunks?.length ?? 0}</span></div>
                <div className="flex justify-between"><span className="text-gray-500">Generations</span><span>{manifest.data?.generations?.length ?? 0}</span></div>
                <p className="text-xs text-gray-400 break-all">{manifest.data?.manifest_path || 'Manifest unavailable'}</p>
              </div>
            </section>

            <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
              <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><AlertTriangle className="h-5 w-5 text-amber-600" /> Conflicts</h2>
              <div className="space-y-3 text-sm">
                <div className="flex justify-between"><span className="text-gray-500">State</span><Badge variant={conflicts.data?.has_conflicts ? 'warning' : 'success'}>{conflicts.data?.has_conflicts ? 'needs reconcile' : 'clean'}</Badge></div>
                <div className="flex justify-between"><span className="text-gray-500">Paths</span><span>{conflictCount}</span></div>
                <div className="flex gap-2 flex-wrap">
                  {(conflicts.data?.recommended_actions ?? []).map((action) => <Badge key={action} variant="info">{action}</Badge>)}
                </div>
              </div>
            </section>
          </div>

          <div className="grid grid-cols-1 xl:grid-cols-2 gap-6">
            <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
              <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><Search className="h-5 w-5 text-purple-600" /> Search Workspace</h2>
              <div className="space-y-3">
                <div className="flex gap-2">
                  <Input value={searchQuery} onChange={(e) => setSearchQuery(e.target.value)} placeholder="rate limiting architecture" />
                  <Button onClick={handleSearch} loading={searchMutation.isPending} disabled={!searchQuery.trim()}>
                    <Search className="h-4 w-4" />
                    Search
                  </Button>
                </div>
                {searchMutation.data?.freshness && (
                  <div className="flex gap-2 flex-wrap">
                    <Badge variant={searchMutation.data.freshness.stale ? 'warning' : 'success'}>{searchMutation.data.generation || 'generation'}</Badge>
                    <Badge variant={searchMutation.data.freshness.potentially_stale ? 'warning' : 'default'}>{searchMutation.data.freshness.reason || 'fresh'}</Badge>
                    <Badge variant="info">{searchMutation.data.collection || 'collection'}</Badge>
                  </div>
                )}
                <div className="space-y-2">
                  {(searchMutation.data?.results ?? []).slice(0, 8).map((result, i) => {
                    const p = resultPath(result)
                    return (
                      <div key={`${p}-${i}`} className="rounded-md border border-gray-100 dark:border-gray-800 p-3 text-sm">
                        <div className="flex items-center justify-between gap-3">
                          <button onClick={() => handleRead(p)} className="font-mono text-xs text-blue-600 hover:underline truncate">{p}</button>
                          <Badge variant="default">{String(result.score ?? result.fused_score ?? 'hit')}</Badge>
                        </div>
                        <p className="mt-2 text-xs text-gray-500 line-clamp-2">{String(result.text || result.content || result.heading_path || '')}</p>
                      </div>
                    )
                  })}
                  {searchMutation.isSuccess && (searchMutation.data?.results ?? []).length === 0 && <p className="text-sm text-gray-400">No hits.</p>}
                  {searchMutation.isError && <p className="text-xs text-red-600">{searchMutation.error instanceof Error ? searchMutation.error.message : 'Search failed'}</p>}
                </div>
              </div>
            </section>

            <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
              <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><BookOpen className="h-5 w-5 text-cyan-600" /> Exact Read</h2>
              <div className="space-y-3">
                <div className="flex gap-2">
                  <Input value={readPath} onChange={(e) => setReadPath(e.target.value)} placeholder="docs/note.md" />
                  <Button variant="secondary" onClick={() => handleRead()} loading={readMutation.isPending} disabled={!readPath.trim()}>
                    <BookOpen className="h-4 w-4" />
                    Read
                  </Button>
                </div>
                {readMutation.data && (
                  <div className="rounded-md border border-gray-100 dark:border-gray-800">
                    <div className="flex items-center justify-between px-3 py-2 border-b border-gray-100 dark:border-gray-800">
                      <code className="text-xs truncate">{readMutation.data.path}</code>
                      <Badge variant="info">{readMutation.data.chunks?.length ?? 0} chunks</Badge>
                    </div>
                    <pre className="max-h-72 overflow-auto whitespace-pre-wrap p-3 text-xs">{readMutation.data.text}</pre>
                  </div>
                )}
                {readMutation.isError && <p className="text-xs text-red-600">{readMutation.error instanceof Error ? readMutation.error.message : 'Read failed'}</p>}
              </div>
            </section>
          </div>

          <div className="grid grid-cols-1 xl:grid-cols-2 gap-6">
            <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
              <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><Upload className="h-5 w-5 text-green-600" /> Write / Index</h2>
              <div className="space-y-3">
                <Input label="Path" value={path} onChange={(e) => setPath(e.target.value)} placeholder="docs/note.md" />
                <Input label="Title" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="Optional title" />
                <textarea value={text} onChange={(e) => setText(e.target.value)} placeholder="Markdown or text to write and index" className="min-h-36 w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-2 text-sm" />
                <div className="flex gap-2">
                  <Button onClick={handleWrite} loading={writeMutation.isPending} disabled={!path.trim() || !text.trim()}>
                    <Upload className="h-4 w-4" />
                    Write + Index
                  </Button>
                  <Button variant="secondary" onClick={handleIndex} loading={indexMutation.isPending} disabled={!path.trim() || !text.trim()}>
                    Index Only
                  </Button>
                </div>
                {writeMutation.isSuccess && <p className="text-xs text-green-600">Wrote {writeMutation.data.bytes} bytes to {writeMutation.data.path}.</p>}
                {indexMutation.isSuccess && <p className="text-xs text-green-600">Indexed into {indexMutation.data.active_generation || generation}.</p>}
                {(writeMutation.isError || indexMutation.isError) && <p className="text-xs text-red-600">Workspace write/index failed.</p>}
              </div>
            </section>

            <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
              <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><RefreshCw className="h-5 w-5 text-cyan-600" /> Reindex Paths</h2>
              <div className="space-y-3">
                <textarea value={reindexPaths} onChange={(e) => setReindexPaths(e.target.value)} placeholder={'docs/architecture.md\ndocs/runbook.md'} className="min-h-36 w-full rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 py-2 text-sm" />
                <Button variant="secondary" onClick={handleReindex} loading={reindexMutation.isPending} disabled={splitLines(reindexPaths).length === 0}>
                  <RefreshCw className="h-4 w-4" />
                  Reindex
                </Button>
                {reindexMutation.isSuccess && <p className="text-xs text-green-600">Reindexed {reindexMutation.data.results?.length ?? 0} paths.</p>}
                {reindexMutation.isError && <p className="text-xs text-red-600">{reindexMutation.error instanceof Error ? reindexMutation.error.message : 'Reindex failed'}</p>}
              </div>
            </section>
          </div>

          <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
            <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><RotateCcw className="h-5 w-5 text-purple-600" /> Index Jobs</h2>
            {jobs.data?.jobs?.length ? (
              <div className="overflow-x-auto">
                <table className="w-full text-sm">
                  <thead className="text-left text-gray-500">
                    <tr>
                      <th className="py-2 pr-3">Job</th>
                      <th className="py-2 pr-3">Status</th>
                      <th className="py-2 pr-3">Attempts</th>
                      <th className="py-2 pr-3">Operation</th>
                      <th className="py-2 pr-3">Updated</th>
                      <th className="py-2 pr-3">Action</th>
                    </tr>
                  </thead>
                  <tbody>
                    {jobs.data.jobs.slice(0, 20).map((job) => (
                      <tr key={job.id} className="border-t border-gray-100 dark:border-gray-800">
                        <td className="py-2 pr-3 font-mono text-xs">{job.id}</td>
                        <td className="py-2 pr-3"><Badge variant={statusVariant(job.status)}>{job.status}</Badge></td>
                        <td className="py-2 pr-3">{job.attempts ?? 0}</td>
                        <td className="py-2 pr-3">{String(job.request?.operation || '-')}</td>
                        <td className="py-2 pr-3 text-gray-500">{job.updated_at ? new Date(job.updated_at).toLocaleString() : '-'}</td>
                        <td className="py-2 pr-3">
                          <Button variant="secondary" size="sm" onClick={() => retryMutation.mutate({ project_id: projectId.trim(), branch: scope.branch, job_id: job.id })} loading={retryMutation.isPending}>
                            Retry
                          </Button>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            ) : (
              <p className="text-sm text-gray-400">{jobs.isFetching ? 'Loading jobs...' : 'No workspace jobs for this scope.'}</p>
            )}
          </section>

          <div className="grid grid-cols-1 xl:grid-cols-2 gap-6">
            <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
              <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><Files className="h-5 w-5 text-blue-600" /> Context Artifacts</h2>
              <div className="mb-3">
                <Input label="Kind filter" value={artifactKind} onChange={(e) => setArtifactKind(e.target.value)} placeholder="run, note, decision" />
              </div>
              <div className="space-y-2">
                {(artifacts.data?.artifacts ?? []).slice(0, 12).map((artifact) => (
                  <div key={artifact.id} className="rounded-md border border-gray-100 dark:border-gray-800 p-3 text-sm">
                    <div className="flex items-center justify-between gap-3">
                      <span className="font-medium truncate">{artifact.title || artifact.id}</span>
                      <Badge variant="default">{artifact.kind || 'artifact'}</Badge>
                    </div>
                    <p className="mt-1 text-xs text-gray-500 break-all">{artifact.path || artifact.project_id || ''}</p>
                  </div>
                ))}
                {artifacts.isSuccess && (artifacts.data?.artifacts ?? []).length === 0 && <p className="text-sm text-gray-400">No context artifacts.</p>}
              </div>
            </section>

            <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
              <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><History className="h-5 w-5 text-gray-600" /> Audit</h2>
              <div className="space-y-2">
                {(audit.data?.events ?? []).map((event) => (
                  <div key={event.id} className="rounded-md border border-gray-100 dark:border-gray-800 p-3 text-sm">
                    <div className="flex items-center justify-between gap-3">
                      <span>{event.operation}</span>
                      <Badge variant={statusVariant(event.result)}>{event.result}</Badge>
                    </div>
                    <p className="mt-1 text-xs text-gray-500">{event.at ? new Date(event.at).toLocaleString() : ''} {event.status ? `status ${event.status}` : ''}</p>
                  </div>
                ))}
                {audit.isSuccess && (audit.data?.events ?? []).length === 0 && <p className="text-sm text-gray-400">No audit events.</p>}
              </div>
            </section>
          </div>
        </div>
      )}
    </div>
  )
}
