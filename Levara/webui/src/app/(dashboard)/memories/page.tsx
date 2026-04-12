'use client'

import { useState, useEffect } from 'react'
import { levara, type Memory } from '@/lib/api'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { EmptyState } from '@/components/ui/empty-state'
import { Skeleton } from '@/components/ui/skeleton'
import { Brain, Plus, Trash2, Star } from 'lucide-react'

const TYPES = ['all', 'fact', 'decision', 'event', 'preference', 'advice', 'discovery']

export default function MemoriesPage() {
  const [memories, setMemories] = useState<Memory[]>([])
  const [loading, setLoading] = useState(true)
  const [filter, setFilter] = useState('all')
  const [showAdd, setShowAdd] = useState(false)
  const [newKey, setNewKey] = useState('')
  const [newValue, setNewValue] = useState('')
  const [newType, setNewType] = useState('fact')

  const fetchMemories = async () => {
    try {
      const data = await levara.memories(filter === 'all' ? undefined : filter)
      setMemories(Array.isArray(data) ? data : [])
    } catch {
      setMemories([])
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { fetchMemories() }, [filter])

  const handleAdd = async () => {
    if (!newKey.trim() || !newValue.trim()) return
    try {
      await levara.saveMemory(newKey.trim(), newValue.trim(), newType)
      setNewKey('')
      setNewValue('')
      setShowAdd(false)
      await fetchMemories()
    } catch {}
  }

  const typeBadge = (type?: string) => {
    const map: Record<string, 'info' | 'success' | 'warning' | 'error' | 'default'> = {
      fact: 'info', decision: 'success', event: 'warning',
      preference: 'default', advice: 'info', discovery: 'success',
    }
    return map[type || ''] || 'default'
  }

  if (loading) {
    return (
      <div>
        <h1 className="text-2xl font-bold mb-6">Memories</h1>
        <div className="space-y-3">
          {[...Array(5)].map((_, i) => <Skeleton key={i} className="h-16 rounded-lg" />)}
        </div>
      </div>
    )
  }

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h1 className="text-2xl font-bold">Memories</h1>
        <Button size="sm" onClick={() => setShowAdd(!showAdd)}>
          <Plus className="h-4 w-4" /> Add Memory
        </Button>
      </div>

      {/* Add form */}
      {showAdd && (
        <div className="mb-4 p-4 bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 space-y-3">
          <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
            <Input placeholder="Key" value={newKey} onChange={(e) => setNewKey(e.target.value)} />
            <Input placeholder="Value" value={newValue} onChange={(e) => setNewValue(e.target.value)} />
            <select
              value={newType}
              onChange={(e) => setNewType(e.target.value)}
              className="h-9 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 text-sm"
            >
              {TYPES.filter(t => t !== 'all').map((t) => (
                <option key={t} value={t}>{t}</option>
              ))}
            </select>
          </div>
          <div className="flex gap-2">
            <Button onClick={handleAdd} disabled={!newKey.trim() || !newValue.trim()}>Save</Button>
            <Button variant="ghost" onClick={() => setShowAdd(false)}>Cancel</Button>
          </div>
        </div>
      )}

      {/* Type filter */}
      <div className="flex gap-2 mb-4 flex-wrap">
        {TYPES.map((t) => (
          <button
            key={t}
            onClick={() => { setFilter(t); setLoading(true) }}
            className={`px-3 py-1 rounded-full text-xs font-medium capitalize transition-colors ${
              filter === t
                ? 'bg-blue-100 text-blue-800 dark:bg-blue-900 dark:text-blue-200'
                : 'bg-gray-100 text-gray-600 hover:bg-gray-200 dark:bg-gray-800 dark:text-gray-400'
            }`}
          >
            {t}
          </button>
        ))}
      </div>

      {/* Memory list */}
      {memories.length === 0 ? (
        <EmptyState
          icon={Brain}
          title="No memories yet"
          description="Save facts, decisions, and events for persistent context"
          action={{ label: 'Add Memory', onClick: () => setShowAdd(true) }}
        />
      ) : (
        <div className="space-y-2">
          {memories.map((m) => (
            <div
              key={m.key}
              className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-3 flex items-start justify-between"
            >
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2 mb-1">
                  <code className="text-xs text-gray-500 font-mono">{m.key}</code>
                  {m.type && <Badge variant={typeBadge(m.type)}>{m.type}</Badge>}
                </div>
                <p className="text-sm text-gray-900 dark:text-gray-100">{m.value}</p>
                {m.created_at && (
                  <p className="text-xs text-gray-400 mt-1">{new Date(m.created_at).toLocaleString()}</p>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
