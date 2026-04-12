'use client'

import { useState } from 'react'
import { levara } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { BookOpen, Plus, Play, Trash2, Code, FileText, Loader2 } from 'lucide-react'

interface Cell {
  id: string
  type: 'code' | 'markdown'
  content: string
  output?: string
  status: 'idle' | 'running' | 'done' | 'error'
}

export default function NotebooksPage() {
  const [cells, setCells] = useState<Cell[]>([
    { id: '1', type: 'markdown', content: '# My Notebook\n\nUse code cells to run search queries against Levara.', status: 'idle' },
    { id: '2', type: 'code', content: 'search("climate change", type="HYBRID", top_k=5)', status: 'idle' },
  ])

  const addCell = (type: 'code' | 'markdown') => {
    setCells([...cells, { id: crypto.randomUUID(), type, content: '', status: 'idle' }])
  }

  const updateCell = (id: string, content: string) => {
    setCells(cells.map((c) => (c.id === id ? { ...c, content } : c)))
  }

  const deleteCell = (id: string) => {
    setCells(cells.filter((c) => c.id !== id))
  }

  const runCell = async (id: string) => {
    setCells(cells.map((c) => (c.id === id ? { ...c, status: 'running', output: undefined } : c)))

    const cell = cells.find((c) => c.id === id)
    if (!cell || cell.type !== 'code') {
      setCells(cells.map((c) => (c.id === id ? { ...c, status: 'done' } : c)))
      return
    }

    try {
      // Parse simple search command: search("query", type="MODE", top_k=N)
      const match = cell.content.match(/search\(["'](.+?)["'](?:,\s*type=["'](\w+)["'])?(?:,\s*top_k=(\d+))?\)/)
      if (match) {
        const [, query, qtype, topk] = match
        const results = await levara.search({
          query_text: query,
          query_type: qtype || 'HYBRID',
          top_k: parseInt(topk || '5'),
        })
        const output = Array.isArray(results)
          ? results.map((r, i) => `[${i + 1}] score=${(r.fused_score || r.score || 0).toFixed(4)} | ${((r.metadata?.text as string) || r.id).slice(0, 100)}`).join('\n')
          : JSON.stringify(results, null, 2)
        setCells((prev) => prev.map((c) => (c.id === id ? { ...c, status: 'done', output } : c)))
      } else {
        setCells((prev) => prev.map((c) => (c.id === id ? { ...c, status: 'error', output: 'Unknown command. Use: search("query", type="HYBRID", top_k=5)' } : c)))
      }
    } catch (err) {
      setCells((prev) => prev.map((c) => (c.id === id ? { ...c, status: 'error', output: `Error: ${err instanceof Error ? err.message : 'Unknown'}` } : c)))
    }
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h1 className="text-2xl font-bold">Notebook</h1>
        <div className="flex gap-2">
          <Button variant="secondary" size="sm" onClick={() => addCell('markdown')}>
            <FileText className="h-4 w-4" /> Markdown
          </Button>
          <Button variant="secondary" size="sm" onClick={() => addCell('code')}>
            <Code className="h-4 w-4" /> Code
          </Button>
        </div>
      </div>

      {cells.length === 0 ? (
        <EmptyState
          icon={BookOpen}
          title="Empty notebook"
          description="Add code or markdown cells to get started"
          action={{ label: 'Add Code Cell', onClick: () => addCell('code') }}
        />
      ) : (
        <div className="space-y-3">
          {cells.map((cell) => (
            <div key={cell.id} className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 overflow-hidden">
              {/* Cell header */}
              <div className="flex items-center justify-between px-3 py-1.5 bg-gray-50 dark:bg-gray-800 border-b border-gray-200 dark:border-gray-700">
                <div className="flex items-center gap-2">
                  {cell.type === 'code' ? <Code className="h-3.5 w-3.5 text-blue-500" /> : <FileText className="h-3.5 w-3.5 text-gray-400" />}
                  <Badge variant={cell.type === 'code' ? 'info' : 'default'} className="text-[10px]">{cell.type}</Badge>
                  {cell.status === 'running' && <Loader2 className="h-3 w-3 animate-spin text-blue-500" />}
                  {cell.status === 'done' && <Badge variant="success" className="text-[10px]">done</Badge>}
                  {cell.status === 'error' && <Badge variant="error" className="text-[10px]">error</Badge>}
                </div>
                <div className="flex gap-1">
                  {cell.type === 'code' && (
                    <Button variant="ghost" size="sm" onClick={() => runCell(cell.id)} disabled={cell.status === 'running'}>
                      <Play className="h-3.5 w-3.5" />
                    </Button>
                  )}
                  <Button variant="ghost" size="sm" onClick={() => deleteCell(cell.id)}>
                    <Trash2 className="h-3.5 w-3.5 text-red-400" />
                  </Button>
                </div>
              </div>

              {/* Cell content */}
              <textarea
                value={cell.content}
                onChange={(e) => updateCell(cell.id, e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && cell.type === 'code') {
                    e.preventDefault()
                    runCell(cell.id)
                  }
                }}
                rows={Math.max(3, cell.content.split('\n').length)}
                className={`w-full px-4 py-3 text-sm resize-none focus:outline-none bg-transparent ${
                  cell.type === 'code' ? 'font-mono' : ''
                }`}
                placeholder={cell.type === 'code' ? 'search("your query", type="HYBRID", top_k=5)' : 'Write markdown...'}
              />

              {/* Output */}
              {cell.output && (
                <div className={`px-4 py-3 border-t text-sm font-mono whitespace-pre-wrap ${
                  cell.status === 'error'
                    ? 'bg-red-50 dark:bg-red-900/10 text-red-700 dark:text-red-300 border-red-200 dark:border-red-800'
                    : 'bg-gray-50 dark:bg-gray-800 text-gray-700 dark:text-gray-300 border-gray-200 dark:border-gray-700'
                }`}>
                  {cell.output}
                </div>
              )}
            </div>
          ))}
        </div>
      )}

      {/* Bottom add buttons */}
      <div className="flex justify-center gap-2 mt-4">
        <Button variant="ghost" size="sm" onClick={() => addCell('code')}>
          <Plus className="h-3.5 w-3.5" /> Code
        </Button>
        <Button variant="ghost" size="sm" onClick={() => addCell('markdown')}>
          <Plus className="h-3.5 w-3.5" /> Markdown
        </Button>
      </div>
    </div>
  )
}
