'use client'

import Link from 'next/link'
import { useInfo, useDatasets, useCollections } from '@/hooks/use-levara'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { Database, FolderOpen, Cpu, Activity, Search, Brain } from 'lucide-react'

export default function DashboardPage() {
  const { data: info, isLoading: infoLoading } = useInfo()
  const { data: datasets } = useDatasets()
  const { data: collections } = useCollections()

  const loading = infoLoading

  if (loading) {
    return (
      <div>
        <h1 className="text-2xl font-bold mb-6">Dashboard</h1>
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4">
          {[...Array(4)].map((_, i) => <Skeleton key={i} className="h-28 rounded-lg" />)}
        </div>
      </div>
    )
  }

  const widgets = [
    { title: 'Status', value: info?.status || 'unknown', icon: Activity, badge: info?.status === 'ready' ? 'success' as const : 'warning' as const },
    { title: 'Datasets', value: datasets?.data?.length ?? '—', icon: Database, badge: 'info' as const },
    { title: 'Collections', value: collections?.length ?? '—', icon: FolderOpen, badge: 'default' as const },
    { title: 'Dimension', value: info?.dimension ?? '—', icon: Cpu, badge: 'default' as const },
  ]

  return (
    <div>
      <h1 className="text-2xl font-bold mb-6">Dashboard</h1>
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-4 gap-4 mb-8">
        {widgets.map((w) => (
          <div key={w.title} className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4">
            <div className="flex items-center justify-between mb-2">
              <span className="text-sm text-gray-500 dark:text-gray-400">{w.title}</span>
              <w.icon className="h-4 w-4 text-gray-400" />
            </div>
            <div className="flex items-center gap-2">
              <span className="text-2xl font-bold">{w.value}</span>
              <Badge variant={w.badge}>{String(w.value)}</Badge>
            </div>
          </div>
        ))}
      </div>
      <h2 className="text-lg font-semibold mb-3">Quick Actions</h2>
      <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
        <Link href="/datasets" className="flex items-center gap-3 p-4 bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 hover:border-blue-300 dark:hover:border-blue-700 transition-colors">
          <Database className="h-8 w-8 text-blue-600" />
          <div><p className="font-medium">Upload Data</p><p className="text-sm text-gray-500">Drag & drop files to get started</p></div>
        </Link>
        <Link href="/search" className="flex items-center gap-3 p-4 bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 hover:border-blue-300 dark:hover:border-blue-700 transition-colors">
          <Search className="h-8 w-8 text-green-600" />
          <div><p className="font-medium">Search</p><p className="text-sm text-gray-500">Dense, Sparse, or Hybrid</p></div>
        </Link>
        <Link href="/chat" className="flex items-center gap-3 p-4 bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 hover:border-blue-300 dark:hover:border-blue-700 transition-colors">
          <Brain className="h-8 w-8 text-purple-600" />
          <div><p className="font-medium">Chat (RAG)</p><p className="text-sm text-gray-500">Ask questions with AI answers</p></div>
        </Link>
      </div>
    </div>
  )
}
