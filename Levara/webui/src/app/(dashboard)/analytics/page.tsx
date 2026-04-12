'use client'

import { useState, useEffect } from 'react'
import { levara } from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { BarChart3, Activity, MessageCircle, Star, AlertCircle, Zap, Database, Clock } from 'lucide-react'

interface Stats {
  health: { status: string } | null
  info: { dimension: number; shards: number; status: string; collections?: string[] } | null
  feedback: { total: number; avg_rating: number; worst_query?: string } | null
  cache: { size?: number; max_size?: number; hits?: number; misses?: number; hit_rate?: number; Size?: number; MaxSize?: number; Hits?: number; Misses?: number; HitRate?: number } | null
  collections: number
  errors: { message: string; timestamp: string }[]
}

export default function AnalyticsPage() {
  const [stats, setStats] = useState<Stats | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    const load = async () => {
      const [health, info, feedback, cache, colls, errors] = await Promise.allSettled([
        levara.health(),
        levara.info(),
        levara.feedbackStats(),
        fetch(`${process.env.NEXT_PUBLIC_API_URL || ''}/api/v1/cache/stats`, { credentials: 'include' }).then((r) => r.json()),
        levara.collections(),
        fetch(`${process.env.NEXT_PUBLIC_API_URL || ''}/api/v1/errors?limit=10`, { credentials: 'include' }).then((r) => r.json()),
      ])
      setStats({
        health: health.status === 'fulfilled' ? health.value : null,
        info: info.status === 'fulfilled' ? info.value : null,
        feedback: feedback.status === 'fulfilled' ? feedback.value : null,
        cache: cache.status === 'fulfilled' ? (() => {
          const c = cache.value as Record<string, unknown>
          return {
            size: (c.size ?? c.Size ?? 0) as number,
            max_size: (c.max_size ?? c.MaxSize ?? 0) as number,
            hits: (c.hits ?? c.Hits ?? 0) as number,
            misses: (c.misses ?? c.Misses ?? 0) as number,
            hit_rate: (c.hit_rate ?? c.HitRate ?? 0) as number,
          }
        })() : null,
        collections: colls.status === 'fulfilled' ? colls.value.length : 0,
        errors: errors.status === 'fulfilled' ? (Array.isArray(errors.value) ? errors.value : []) : [],
      })
      setLoading(false)
    }
    load()
    const interval = setInterval(load, 30000)
    return () => clearInterval(interval)
  }, [])

  if (loading) {
    return (
      <div>
        <h1 className="text-2xl font-bold mb-6">Analytics</h1>
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
          {[...Array(8)].map((_, i) => <Skeleton key={i} className="h-28 rounded-lg" />)}
        </div>
      </div>
    )
  }

  const widgets = [
    {
      title: 'System Status',
      value: stats?.info?.status || 'unknown',
      icon: Activity,
      color: stats?.info?.status === 'ready' ? 'text-green-600' : 'text-amber-600',
      badge: stats?.info?.status === 'ready' ? 'success' : 'warning',
      sub: `dim=${stats?.info?.dimension || '?'}, shards=${stats?.info?.shards || '?'}`,
    },
    {
      title: 'Collections',
      value: stats?.collections ?? 0,
      icon: Database,
      color: 'text-blue-600',
      badge: 'info',
      sub: 'vector indexes',
    },
    {
      title: 'Feedback',
      value: stats?.feedback?.total ?? 0,
      icon: Star,
      color: 'text-amber-500',
      badge: 'warning',
      sub: stats?.feedback?.total ? `avg rating: ${stats.feedback.avg_rating}/5` : 'no feedback yet',
    },
    {
      title: 'LLM Cache',
      value: stats?.cache?.hit_rate != null ? `${(stats.cache.hit_rate * 100).toFixed(0)}%` : '—',
      icon: Zap,
      color: 'text-purple-600',
      badge: 'info',
      sub: stats?.cache?.hits != null ? `${stats.cache.hits} hits / ${stats.cache.misses} misses` : 'no cache data',
    },
  ]

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h1 className="text-2xl font-bold">Analytics</h1>
        <Badge variant="default">Auto-refresh 30s</Badge>
      </div>

      {/* Top widgets */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mb-8">
        {widgets.map((w) => (
          <div key={w.title} className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4">
            <div className="flex items-center justify-between mb-2">
              <span className="text-sm text-gray-500">{w.title}</span>
              <w.icon className={`h-4 w-4 ${w.color}`} />
            </div>
            <div className="flex items-center gap-2">
              <span className="text-2xl font-bold">{w.value}</span>
              <Badge variant={w.badge as 'success' | 'warning' | 'info'}>{String(w.value)}</Badge>
            </div>
            <p className="text-xs text-gray-400 mt-1">{w.sub}</p>
          </div>
        ))}
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-6">
        {/* Cache details */}
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2">
            <Zap className="h-5 w-5 text-purple-600" /> LLM Cache
          </h2>
          {stats?.cache ? (
            <div className="space-y-3">
              <div className="flex justify-between text-sm">
                <span className="text-gray-500">Size</span>
                <span>{stats.cache.size ?? 0} / {stats.cache.max_size ?? 0}</span>
              </div>
              <div className="flex justify-between text-sm">
                <span className="text-gray-500">Hits</span>
                <span className="text-green-600">{stats.cache.hits ?? 0}</span>
              </div>
              <div className="flex justify-between text-sm">
                <span className="text-gray-500">Misses</span>
                <span className="text-red-500">{stats.cache.misses ?? 0}</span>
              </div>
              <div className="flex justify-between text-sm">
                <span className="text-gray-500">Hit Rate</span>
                <span className="font-medium">
                  {stats.cache.hit_rate != null ? `${(stats.cache.hit_rate * 100).toFixed(1)}%` : '—'}
                </span>
              </div>
              {/* Hit rate bar */}
              <div className="w-full bg-gray-200 dark:bg-gray-700 rounded-full h-2">
                <div
                  className="bg-purple-600 h-2 rounded-full transition-all"
                  style={{ width: `${(stats.cache.hit_rate ?? 0) * 100}%` }}
                />
              </div>
            </div>
          ) : (
            <p className="text-sm text-gray-400">Cache not available</p>
          )}
        </div>

        {/* Worst queries */}
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2">
            <MessageCircle className="h-5 w-5 text-amber-500" /> Feedback Insights
          </h2>
          {stats?.feedback?.total ? (
            <div className="space-y-3">
              <div className="flex justify-between text-sm">
                <span className="text-gray-500">Total Ratings</span>
                <span>{stats.feedback.total}</span>
              </div>
              <div className="flex justify-between text-sm">
                <span className="text-gray-500">Average Rating</span>
                <div className="flex items-center gap-1">
                  {[1, 2, 3, 4, 5].map((s) => (
                    <Star key={s} className={`h-3 w-3 ${s <= stats.feedback!.avg_rating ? 'text-amber-400 fill-amber-400' : 'text-gray-300'}`} />
                  ))}
                  <span className="ml-1">{stats.feedback.avg_rating}/5</span>
                </div>
              </div>
              {stats.feedback.worst_query && (
                <div>
                  <span className="text-xs text-gray-500">Worst query:</span>
                  <p className="text-sm mt-1 text-red-600 dark:text-red-400 italic">"{stats.feedback.worst_query}"</p>
                </div>
              )}
            </div>
          ) : (
            <p className="text-sm text-gray-400">No feedback data yet. Rate search results to see insights.</p>
          )}
        </div>

        {/* Recent errors */}
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5 lg:col-span-2">
          <h2 className="text-lg font-semibold mb-4 flex items-center gap-2">
            <AlertCircle className="h-5 w-5 text-red-500" /> Recent Errors
          </h2>
          {stats?.errors && stats.errors.length > 0 ? (
            <div className="space-y-2">
              {stats.errors.slice(0, 5).map((e, i) => (
                <div key={i} className="flex items-start gap-2 text-sm p-2 bg-red-50 dark:bg-red-900/10 rounded">
                  <AlertCircle className="h-4 w-4 text-red-500 mt-0.5 flex-shrink-0" />
                  <div className="flex-1 min-w-0">
                    <p className="text-red-800 dark:text-red-300 truncate">{e.message}</p>
                    {e.timestamp && <p className="text-xs text-red-400">{new Date(e.timestamp).toLocaleString()}</p>}
                  </div>
                </div>
              ))}
            </div>
          ) : (
            <p className="text-sm text-gray-400">No errors. System is healthy.</p>
          )}
        </div>
      </div>
    </div>
  )
}
