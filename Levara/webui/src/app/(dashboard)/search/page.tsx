'use client'

import { useState } from 'react'
import { type SearchResult, type SearchRequest } from '@/lib/api'
import { useSearch, useSubmitFeedback } from '@/hooks/use-levara'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { Skeleton } from '@/components/ui/skeleton'
import { Search as SearchIcon, Star, ChevronDown } from 'lucide-react'

const SEARCH_MODES = [
  { value: 'AUTO', label: 'Auto', description: 'Smart routing' },
  { value: 'CHUNKS', label: 'Dense', description: 'Semantic similarity' },
  { value: 'CHUNKS_LEXICAL', label: 'Sparse (BM25)', description: 'Keyword matching' },
  { value: 'HYBRID', label: 'Hybrid', description: 'Dense + BM25 + RRF' },
  { value: 'RAG_COMPLETION', label: 'RAG', description: 'AI-generated answer' },
  { value: 'GRAPH_COMPLETION', label: 'Graph', description: 'Knowledge graph traversal' },
]

export default function SearchPage() {
  const [query, setQuery] = useState('')
  const [mode, setMode] = useState('AUTO')
  const [collection, setCollection] = useState('')
  const [results, setResults] = useState<SearchResult[]>([])
  const [ragAnswer, setRagAnswer] = useState<string | null>(null)
  const [searched, setSearched] = useState(false)
  const [timing, setTiming] = useState<number | null>(null)
  const [collections, setCollections] = useState<string[]>([])

  // Load collections
  useState(() => {
    import('@/lib/api').then(({ levara }) =>
      levara.collections().then((c) => setCollections(c.filter((x) => !x.name.startsWith('_') && x.name !== 'Triplet_text').map((x) => x.name)))
    ).catch(() => {})
  })

  const searchMutation = useSearch()
  const feedbackMutation = useSubmitFeedback()
  const loading = searchMutation.isPending

  const handleSearch = async () => {
    if (!query.trim()) return
    setRagAnswer(null)
    const t0 = performance.now()
    try {
      const params: SearchRequest = { query_text: query, query_type: mode, top_k: 20, collection: collection || undefined }
      const data = await searchMutation.mutateAsync(params)
      setTiming(Math.round(performance.now() - t0))

      if (Array.isArray(data)) {
        setResults(data as SearchResult[])
      } else if (typeof data === 'object' && data && 'answer' in data) {
        const d = data as Record<string, unknown>
        setRagAnswer(d.answer as string)
        setResults((d.chunks as SearchResult[]) || [])
      } else {
        setResults([])
      }
    } catch {
      setResults([])
    } finally {
      setSearched(true)
    }
  }

  const handleFeedback = (resultId: string, rating: number) => {
    feedbackMutation.mutate({ query, result_id: resultId, rating, search_type: mode })
  }

  return (
    <div>
      <h1 className="text-2xl font-bold mb-6">Search</h1>

      {/* Search bar */}
      <div className="flex gap-2 mb-4">
        <div className="flex-1">
          <Input
            placeholder="Enter your query..."
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && handleSearch()}
          />
        </div>
        <select
          value={collection}
          onChange={(e) => setCollection(e.target.value)}
          className="h-9 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 text-sm"
          aria-label="Collection"
        >
          <option value="">All collections</option>
          {collections.map((c) => <option key={c} value={c}>{c}</option>)}
        </select>
        <select
          value={mode}
          onChange={(e) => setMode(e.target.value)}
          className="h-9 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 text-sm"
          aria-label="Search mode"
        >
          {SEARCH_MODES.map((m) => (
            <option key={m.value} value={m.value}>
              {m.label}
            </option>
          ))}
        </select>
        <Button onClick={handleSearch} loading={loading}>
          <SearchIcon className="h-4 w-4" />
          Search
        </Button>
      </div>

      {/* Mode badges */}
      <div className="flex gap-2 mb-6 flex-wrap">
        {SEARCH_MODES.map((m) => (
          <button
            key={m.value}
            onClick={() => setMode(m.value)}
            className={`px-3 py-1 rounded-full text-xs font-medium transition-colors ${
              mode === m.value
                ? 'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200'
                : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-gray-800 dark:text-gray-400'
            }`}
          >
            {m.label}
          </button>
        ))}
      </div>

      {/* RAG Answer */}
      {ragAnswer && (
        <div className="mb-6 p-4 bg-blue-50 dark:bg-blue-900/20 rounded-lg border border-blue-200 dark:border-blue-800">
          <h3 className="text-sm font-medium text-blue-800 dark:text-blue-300 mb-2">AI Answer</h3>
          <p className="text-gray-900 dark:text-gray-100 whitespace-pre-wrap">{ragAnswer}</p>
        </div>
      )}

      {/* Results */}
      {loading && (
        <div className="space-y-3">
          {[...Array(5)].map((_, i) => (
            <Skeleton key={i} className="h-24 rounded-lg" />
          ))}
        </div>
      )}

      {!loading && searched && results.length === 0 && !ragAnswer && (
        <EmptyState
          icon={SearchIcon}
          title="No results found"
          description="Try a different query or search mode"
        />
      )}

      {!loading && results.length > 0 && (
        <div className="space-y-3">
          <div className="flex items-center justify-between text-sm text-gray-500 dark:text-gray-400">
            <span>{results.length} results</span>
            {timing && <span>{timing}ms</span>}
          </div>
          {results.map((r, i) => (
            <div
              key={r.id}
              className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4"
            >
              <div className="flex items-start justify-between">
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 mb-1">
                    <span className="text-xs text-gray-400">#{i + 1}</span>
                    <code className="text-xs text-gray-500 truncate">{r.id}</code>
                    <Badge variant="info">{r.collection}</Badge>
                    {r.reranked && <Badge variant="success">reranked</Badge>}
                  </div>
                  <p className="text-sm text-gray-900 dark:text-gray-100 line-clamp-3">
                    {(r.metadata?.text as string) || (r.metadata?.title as string) || r.id}
                  </p>
                  <div className="flex gap-3 mt-2 text-xs text-gray-400">
                    <span>score: {(r.fused_score || r.score || 0).toFixed(4)}</span>
                    {r.vector_score !== undefined && <span>dense: {r.vector_score.toFixed(4)}</span>}
                    {r.bm25_score !== undefined && <span>bm25: {r.bm25_score.toFixed(2)}</span>}
                  </div>
                </div>
                {/* Feedback */}
                <div className="flex gap-1 ml-3">
                  {[1, 2, 3, 4, 5].map((star) => (
                    <button
                      key={star}
                      onClick={() => handleFeedback(r.id, star)}
                      className="p-1 text-gray-300 hover:text-amber-400 transition-colors"
                      aria-label={`Rate ${star} stars`}
                    >
                      <Star className="h-4 w-4" />
                    </button>
                  ))}
                </div>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
