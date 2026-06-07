'use client'

import { useInfo, useCollections, useFeedbackStats, useCacheStats, useErrors } from '@/hooks/use-levara'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { BarChart3, Activity, MessageCircle, Star, AlertCircle, Zap, Database } from 'lucide-react'

export default function AnalyticsPage() {
  const { data: info } = useInfo()
  const { data: collections } = useCollections()
  const { data: feedback } = useFeedbackStats()
  const { data: cache } = useCacheStats()
  const { data: errors } = useErrors()
  const isLoading = !info

  if (isLoading) {
    return (
      <div>
        <h1 className="text-2xl font-bold mb-6">Analytics</h1>
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
          {[...Array(4)].map((_, i) => <Skeleton key={i} className="h-28 rounded-lg" />)}
        </div>
      </div>
    )
  }

  const widgets = [
    { title: 'System Status', value: info?.status || 'unknown', icon: Activity, color: 'text-green-600', sub: `dim=${info?.dimension}, shards=${info?.shards}` },
    { title: 'Collections', value: collections?.length ?? 0, icon: Database, color: 'text-blue-600', sub: 'vector indexes' },
    { title: 'Feedback', value: feedback?.total ?? 0, icon: Star, color: 'text-amber-500', sub: feedback?.total ? `avg: ${feedback.avg_rating}/5` : 'no feedback' },
    { title: 'LLM Cache', value: cache?.hit_rate != null ? `${(cache.hit_rate * 100).toFixed(0)}%` : '—', icon: Zap, color: 'text-purple-600', sub: cache ? `${cache.hits} hits / ${cache.misses} misses` : '' },
  ]

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h1 className="text-2xl font-bold">Analytics</h1>
        <Badge variant="default">Auto-refresh 30s</Badge>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mb-8">
        {widgets.map((w) => (
          <div key={w.title} className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4">
            <div className="flex items-center justify-between mb-2">
              <span className="text-sm text-gray-500">{w.title}</span>
              <w.icon className={`h-4 w-4 ${w.color}`} />
            </div>
            <span className="text-2xl font-bold">{w.value}</span>
            <p className="text-xs text-gray-400 mt-1">{w.sub}</p>
          </div>
        ))}
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {/* Cache */}
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><Zap className="h-5 w-5 text-purple-600" /> LLM Cache</h2>
          {cache ? (
            <div className="space-y-3">
              <div className="flex justify-between text-sm"><span className="text-gray-500">Size</span><span>{cache.size} / {cache.max_size}</span></div>
              <div className="flex justify-between text-sm"><span className="text-gray-500">Hits</span><span className="text-green-600">{cache.hits}</span></div>
              <div className="flex justify-between text-sm"><span className="text-gray-500">Misses</span><span className="text-red-500">{cache.misses}</span></div>
              <div className="flex justify-between text-sm"><span className="text-gray-500">Hit Rate</span><span className="font-medium">{(cache.hit_rate * 100).toFixed(1)}%</span></div>
              <div className="w-full bg-gray-200 dark:bg-gray-700 rounded-full h-2">
                <div className="bg-purple-600 h-2 rounded-full transition-all" style={{ width: `${cache.hit_rate * 100}%` }} />
              </div>
            </div>
          ) : <p className="text-sm text-gray-400">Cache not available</p>}
        </div>

        {/* Feedback */}
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><MessageCircle className="h-5 w-5 text-amber-500" /> Feedback</h2>
          {feedback?.total ? (
            <div className="space-y-3">
              <div className="flex justify-between text-sm"><span className="text-gray-500">Total</span><span>{feedback.total}</span></div>
              <div className="flex justify-between text-sm">
                <span className="text-gray-500">Average</span>
                <div className="flex items-center gap-1">
                  {[1,2,3,4,5].map((s) => <Star key={s} className={`h-3 w-3 ${s <= feedback.avg_rating ? 'text-amber-400 fill-amber-400' : 'text-gray-300'}`} />)}
                  <span className="ml-1">{feedback.avg_rating}/5</span>
                </div>
              </div>
              {feedback.worst_query && <div><span className="text-xs text-gray-500">Worst:</span><p className="text-sm text-red-600 italic mt-1">"{feedback.worst_query}"</p></div>}
            </div>
          ) : <p className="text-sm text-gray-400">No feedback yet</p>}
        </div>

        {/* Errors */}
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5 lg:col-span-2">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2"><AlertCircle className="h-5 w-5 text-red-500" /> Recent Errors</h2>
          {errors && errors.length > 0 ? (
            <div className="space-y-2">
              {errors.slice(0, 5).map((e, i) => (
                <div key={i} className="flex items-start gap-2 text-sm p-2 bg-red-50 dark:bg-red-900/10 rounded">
                  <AlertCircle className="h-4 w-4 text-red-500 mt-0.5 flex-shrink-0" />
                  <div><p className="text-red-800 dark:text-red-300 truncate">{e.message}</p>
                    {e.timestamp && <p className="text-xs text-red-400">{new Date(e.timestamp).toLocaleString()}</p>}</div>
                </div>
              ))}
            </div>
          ) : <p className="text-sm text-gray-400">No errors. System is healthy.</p>}
        </div>
      </div>
    </div>
  )
}
