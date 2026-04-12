'use client'

import { useState, useEffect } from 'react'
import { levara, type CollectionMeta } from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { Skeleton } from '@/components/ui/skeleton'
import { FolderOpen, Box, Hash, Ruler } from 'lucide-react'

export default function CollectionsPage() {
  const [collections, setCollections] = useState<CollectionMeta[]>([])
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    levara.collections()
      .then(setCollections)
      .catch(() => setCollections([]))
      .finally(() => setLoading(false))
  }, [])

  if (loading) {
    return (
      <div>
        <h1 className="text-2xl font-bold mb-6">Collections</h1>
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {[...Array(6)].map((_, i) => <Skeleton key={i} className="h-36 rounded-lg" />)}
        </div>
      </div>
    )
  }

  return (
    <div>
      <h1 className="text-2xl font-bold mb-6">Collections</h1>

      {collections.length === 0 ? (
        <EmptyState
          icon={FolderOpen}
          title="No collections"
          description="Collections are created automatically when you upload and process data"
        />
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4">
          {collections.map((c) => (
            <div
              key={c.name}
              className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4"
            >
              <div className="flex items-center gap-2 mb-3">
                <FolderOpen className="h-5 w-5 text-blue-500" />
                <h3 className="font-medium truncate">{c.name}</h3>
              </div>

              <div className="space-y-2 text-sm">
                <div className="flex items-center justify-between">
                  <span className="text-gray-500 flex items-center gap-1"><Box className="h-3 w-3" /> Records</span>
                  <span className="font-medium">{c.record_count.toLocaleString()}</span>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-gray-500 flex items-center gap-1"><Hash className="h-3 w-3" /> Dimension</span>
                  <span className="font-medium">{c.embedding_dim}</span>
                </div>
                <div className="flex items-center justify-between">
                  <span className="text-gray-500 flex items-center gap-1"><Ruler className="h-3 w-3" /> Metric</span>
                  <Badge>{c.distance_metric || 'cosine'}</Badge>
                </div>
              </div>

              <div className="mt-3 pt-3 border-t border-gray-100 dark:border-gray-800">
                <p className="text-xs text-gray-400 truncate" title={c.embedding_model}>
                  Model: {c.embedding_model || 'default'}
                </p>
                {c.domain && (
                  <Badge variant="info" className="mt-1">{c.domain}</Badge>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
