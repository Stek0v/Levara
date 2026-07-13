'use client'

import { useMemo, useState } from 'react'
import { useRouter, useSearchParams } from 'next/navigation'
import { CheckCircle2, FileText, Filter, XCircle } from 'lucide-react'
import {
  useDecideMemoryScaffoldProposal,
  useMemoryScaffoldProposal,
  useMemoryScaffoldProposals,
} from '@/hooks/use-levara'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'

const STATUSES = ['open', 'approved', 'rejected']

export default function MemoryScaffoldPage() {
  const router = useRouter()
  const searchParams = useSearchParams()
  const status = searchParams.get('status') || 'open'
  const collection = searchParams.get('collection') || ''
  const target = searchParams.get('target') || ''
  const selected = searchParams.get('proposal') || ''
  const [collectionInput, setCollectionInput] = useState(collection)
  const [targetInput, setTargetInput] = useState(target)
  const [note, setNote] = useState('')

  const params = useMemo(() => ({ status, collection: collection || undefined, target: target || undefined }), [status, collection, target])
  const proposals = useMemoryScaffoldProposals(params)
  const detail = useMemoryScaffoldProposal(selected)
  const decide = useDecideMemoryScaffoldProposal()

  const updateFilters = (next: { status?: string; collection?: string; target?: string; proposal?: string }) => {
    const q = new URLSearchParams(searchParams.toString())
    if (next.status) q.set('status', next.status)
    if ('collection' in next) {
      if (next.collection) q.set('collection', next.collection)
      else q.delete('collection')
    }
    if ('target' in next) {
      if (next.target) q.set('target', next.target)
      else q.delete('target')
    }
    if ('proposal' in next) {
      if (next.proposal) q.set('proposal', next.proposal)
      else q.delete('proposal')
    }
    router.push(`/memory-scaffold?${q.toString()}`)
  }

  const onDecision = async (decision: 'approved' | 'rejected') => {
    if (!selected) return
    await decide.mutateAsync({ id: selected, status: decision, note: note.trim() || undefined })
    setNote('')
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h1 className="text-2xl font-bold">Memory Scaffold Proposals</h1>
          <p className="text-sm text-gray-500 mt-1">Human-gated AGENTS.md / memory policy improvements from trajectory reviews.</p>
        </div>
        <Badge variant="default">No auto-apply</Badge>
      </div>

      <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4 mb-6">
        <div className="flex flex-wrap gap-2 items-end">
          <div>
            <label className="block text-xs text-gray-500 mb-1">Status</label>
            <div className="flex gap-1">
              {STATUSES.map((s) => (
                <Button key={s} size="sm" variant={status === s ? 'primary' : 'secondary'} onClick={() => updateFilters({ status: s, proposal: '' })}>
                  {s}
                </Button>
              ))}
            </div>
          </div>
          <div>
            <label className="block text-xs text-gray-500 mb-1">Collection</label>
            <Input value={collectionInput} onChange={(e) => setCollectionInput(e.target.value)} placeholder="all collections" className="w-44" />
          </div>
          <div>
            <label className="block text-xs text-gray-500 mb-1">Target</label>
            <Input value={targetInput} onChange={(e) => setTargetInput(e.target.value)} placeholder="project_agents" className="w-44" />
          </div>
          <Button onClick={() => updateFilters({ collection: collectionInput.trim(), target: targetInput.trim(), proposal: '' })}>
            <Filter className="h-4 w-4 mr-1" />
            Apply
          </Button>
          <Button variant="secondary" onClick={() => { setCollectionInput(''); setTargetInput(''); updateFilters({ collection: '', target: '', proposal: '' }) }}>
            Clear
          </Button>
        </div>
      </div>

      {proposals.isLoading ? (
        <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
          {[...Array(6)].map((_, i) => <Skeleton key={i} className="h-28 rounded-lg" />)}
        </div>
      ) : proposals.error ? (
        <div className="rounded-lg border border-red-200 bg-red-50 dark:bg-red-950/30 dark:border-red-900 p-4 text-sm text-red-700 dark:text-red-300">
          Scaffold proposals are unavailable. Check database/API health.
        </div>
      ) : (
        <div className="grid grid-cols-1 xl:grid-cols-3 gap-6">
          <section className="xl:col-span-2 bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
            <h2 className="text-lg font-semibold mb-4">Proposals</h2>
            <div className="space-y-3">
              {(proposals.data?.proposals ?? []).map((p) => (
                <button
                  key={p.id}
                  type="button"
                  onClick={() => updateFilters({ proposal: p.id })}
                  className={`w-full text-left rounded-lg border p-4 transition-colors ${selected === p.id ? 'border-blue-400 bg-blue-50 dark:bg-blue-950/30' : 'border-gray-100 dark:border-gray-800 hover:bg-gray-50 dark:hover:bg-gray-800/50'}`}
                >
                  <div className="flex items-center justify-between gap-3">
                    <div className="flex items-center gap-2 min-w-0">
                      <FileText className="h-4 w-4 text-blue-600 flex-shrink-0" />
                      <span className="font-medium truncate">{p.summary}</span>
                    </div>
                    <Badge variant={p.status === 'approved' ? 'success' : p.status === 'rejected' ? 'error' : 'warning'}>{p.status}</Badge>
                  </div>
                  <div className="mt-2 flex flex-wrap gap-2 text-xs text-gray-500">
                    <span>{p.target}</span>
                    <span>{p.collection || 'no collection'}</span>
                    <span>{p.source_finding_ids?.length ?? 0} findings</span>
                  </div>
                </button>
              ))}
              {(proposals.data?.proposals ?? []).length === 0 && <p className="text-sm text-gray-400">No proposals for this filter.</p>}
            </div>
          </section>

          <aside className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
            <h2 className="text-lg font-semibold mb-4">Detail</h2>
            {!selected && <p className="text-sm text-gray-400">Select a proposal to inspect and decide.</p>}
            {selected && detail.isLoading && <Skeleton className="h-64 rounded-lg" />}
            {selected && detail.data && (
              <div className="space-y-4">
                <div>
                  <div className="flex items-center justify-between gap-2">
                    <h3 className="font-semibold">{detail.data.summary}</h3>
                    <Badge variant={detail.data.status === 'approved' ? 'success' : detail.data.status === 'rejected' ? 'error' : 'warning'}>{detail.data.status}</Badge>
                  </div>
                  <p className="text-xs text-gray-500 mt-1">{detail.data.target} · {detail.data.collection || 'no collection'}</p>
                </div>
                <TextBlock title="Current problem" value={detail.data.current_problem} />
                <TextBlock title="Proposed change" value={detail.data.proposed_change} />
                <TextBlock title="Risk" value={detail.data.risk} />
                <TextBlock title="Source" value={`${detail.data.source_run_id || 'unknown run'} · ${(detail.data.source_finding_ids ?? []).length} findings`} />
                {detail.data.status === 'open' ? (
                  <div className="space-y-3 pt-2 border-t border-gray-100 dark:border-gray-800">
                    <Input value={note} onChange={(e) => setNote(e.target.value)} placeholder="decision note (optional)" />
                    <div className="flex gap-2">
                      <Button disabled={decide.isPending} onClick={() => onDecision('approved')}>
                        <CheckCircle2 className="h-4 w-4 mr-1" />
                        Approve
                      </Button>
                      <Button variant="secondary" disabled={decide.isPending} onClick={() => onDecision('rejected')}>
                        <XCircle className="h-4 w-4 mr-1" />
                        Reject
                      </Button>
                    </div>
                    {decide.error && <p className="text-xs text-red-500">Decision requires admin permissions.</p>}
                  </div>
                ) : (
                  <TextBlock title="Decision" value={`${detail.data.decided_by || 'unknown'} · ${detail.data.decision_note || 'no note'}`} />
                )}
              </div>
            )}
          </aside>
        </div>
      )}
    </div>
  )
}

function TextBlock({ title, value }: { title: string; value?: string }) {
  return (
    <div>
      <h4 className="text-xs font-medium uppercase tracking-wide text-gray-400 mb-1">{title}</h4>
      <p className="text-sm whitespace-pre-wrap">{value || '—'}</p>
    </div>
  )
}
