'use client'

import { useEffect, useState } from 'react'
import { levara } from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { Database, HardDrive, Cpu, Activity, Search, Brain } from 'lucide-react'

interface HealthData {
  status: string
  services?: Record<string, { status: string; endpoint?: string }>
}

interface InfoData {
  dimension: number
  shards: number
  status: string
  collections?: string[]
}

export default function DashboardPage() {
  const [health, setHealth] = useState<HealthData | null>(null)
  const [info, setInfo] = useState<InfoData | null>(null)
  const [stats, setStats] = useState<{ collections: number; datasets: number } | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    Promise.allSettled([
      levara.health().then(setHealth),
      levara.info().then(setInfo),
      levara.collections().then((c) => setStats({ collections: c.length, datasets: 0 })),
    ]).finally(() => setLoading(false))
  }, [])

  if (loading) {
    return (
      <div>
        <h1 className="text-2xl font-bold mb-6">Dashboard</h1>
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
          {[...Array(4)].map((_, i) => (
            <Skeleton key={i} className="h-28 rounded-lg" />
          ))}
        </div>
      </div>
    )
  }

  const widgets = [
    {
      title: 'Status',
      value: info?.status || 'unknown',
      icon: Activity,
      badge: info?.status === 'ready' ? 'success' : 'warning',
    },
    {
      title: 'Collections',
      value: stats?.collections ?? '—',
      icon: Database,
      badge: 'info',
    },
    {
      title: 'Dimension',
      value: info?.dimension ?? '—',
      icon: Cpu,
      badge: 'default',
    },
    {
      title: 'Shards',
      value: info?.shards ?? '—',
      icon: HardDrive,
      badge: 'default',
    },
  ]

  return (
    <div>
      <h1 className="text-2xl font-bold mb-6">Dashboard</h1>

      {/* Widgets */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mb-8">
        {widgets.map((w) => (
          <div
            key={w.title}
            className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4"
          >
            <div className="flex items-center justify-between mb-2">
              <span className="text-sm text-gray-500 dark:text-gray-400">{w.title}</span>
              <w.icon className="h-4 w-4 text-gray-400" />
            </div>
            <div className="flex items-center gap-2">
              <span className="text-2xl font-bold">{w.value}</span>
              <Badge variant={w.badge as 'success' | 'warning' | 'info' | 'default'}>
                {typeof w.value === 'string' ? w.value : 'active'}
              </Badge>
            </div>
          </div>
        ))}
      </div>

      {/* Quick actions */}
      <h2 className="text-lg font-semibold mb-3">Quick Actions</h2>
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <a
          href="/datasets"
          className="flex items-center gap-3 p-4 bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 hover:border-blue-300 dark:hover:border-blue-700 transition-colors"
        >
          <Database className="h-8 w-8 text-blue-600" />
          <div>
            <p className="font-medium">Upload Data</p>
            <p className="text-sm text-gray-500">Drag & drop files to get started</p>
          </div>
        </a>
        <a
          href="/search"
          className="flex items-center gap-3 p-4 bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 hover:border-blue-300 dark:hover:border-blue-700 transition-colors"
        >
          <Search className="h-8 w-8 text-green-600" />
          <div>
            <p className="font-medium">Search</p>
            <p className="text-sm text-gray-500">Dense, Sparse, or Hybrid</p>
          </div>
        </a>
        <a
          href="/chat"
          className="flex items-center gap-3 p-4 bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 hover:border-blue-300 dark:hover:border-blue-700 transition-colors"
        >
          <Brain className="h-8 w-8 text-purple-600" />
          <div>
            <p className="font-medium">Chat (RAG)</p>
            <p className="text-sm text-gray-500">Ask questions with AI answers</p>
          </div>
        </a>
      </div>
    </div>
  )
}
