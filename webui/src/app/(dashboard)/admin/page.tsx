'use client'

import { useMemo, useState } from 'react'
import { useMCPAdminSummary, useMCPSessions, useMCPTools } from '@/hooks/use-levara'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Brain, ListChecks, Search, Shield, Users } from 'lucide-react'

function statusVariant(status?: string) {
  if (status === 'canonical') return 'success'
  if (status === 'deprecated') return 'warning'
  if (status === 'legacy') return 'default'
  return 'info'
}

export default function AdminPage() {
  const [toolSearch, setToolSearch] = useState('')
  const [groupFilter, setGroupFilter] = useState('')
  const summary = useMCPAdminSummary()
  const tools = useMCPTools()
  const sessions = useMCPSessions(20)

  const groups = useMemo(
    () => Object.keys(summary.data?.tools_by_group ?? {}).sort(),
    [summary.data?.tools_by_group],
  )
  const filteredTools = useMemo(() => {
    const q = toolSearch.trim().toLowerCase()
    return (tools.data?.tools ?? []).filter((tool) => {
      if (groupFilter && tool.group !== groupFilter) return false
      if (!q) return true
      return tool.name.toLowerCase().includes(q) || String(tool.description || '').toLowerCase().includes(q)
    })
  }, [tools.data?.tools, toolSearch, groupFilter])

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <h1 className="text-2xl font-bold">Admin</h1>
        <Badge variant={summary.data?.audit_enabled ? 'success' : 'default'}>{summary.data?.audit_enabled ? 'MCP audit on' : 'MCP audit off'}</Badge>
      </div>

      <div className="grid grid-cols-1 md:grid-cols-4 gap-4 mb-6">
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4">
          <div className="flex items-center justify-between mb-2"><span className="text-sm text-gray-500">MCP tools</span><ListChecks className="h-4 w-4 text-blue-600" /></div>
          <span className="text-2xl font-bold">{summary.data?.tools_total ?? 0}</span>
        </div>
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4">
          <div className="flex items-center justify-between mb-2"><span className="text-sm text-gray-500">Recent sessions</span><Users className="h-4 w-4 text-green-600" /></div>
          <span className="text-2xl font-bold">{sessions.data?.total ?? 0}</span>
        </div>
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4">
          <div className="flex items-center justify-between mb-2"><span className="text-sm text-gray-500">Pinned memories</span><Brain className="h-4 w-4 text-purple-600" /></div>
          <span className="text-2xl font-bold">{summary.data?.pinned_memories ?? 0}</span>
        </div>
        <div className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-4">
          <div className="flex items-center justify-between mb-2"><span className="text-sm text-gray-500">Memory warnings</span><Shield className="h-4 w-4 text-amber-600" /></div>
          <span className="text-2xl font-bold">{summary.data?.memory_metadata_warnings ?? 0}</span>
        </div>
      </div>

      <div className="grid grid-cols-1 xl:grid-cols-[1.35fr_0.65fr] gap-6">
        <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
          <div className="flex items-center justify-between gap-3 mb-4">
            <h2 className="text-lg font-semibold flex items-center gap-2"><Search className="h-5 w-5 text-blue-600" /> MCP Tools</h2>
            <div className="flex gap-2">
              <Input value={toolSearch} onChange={(e) => setToolSearch(e.target.value)} placeholder="search tools" className="w-52" />
              <select value={groupFilter} onChange={(e) => setGroupFilter(e.target.value)} className="h-9 rounded-md border border-gray-300 dark:border-gray-600 bg-white dark:bg-gray-900 px-3 text-sm">
                <option value="">All groups</option>
                {groups.map((group) => <option key={group} value={group}>{group}</option>)}
              </select>
            </div>
          </div>
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="text-left text-gray-500">
                <tr>
                  <th className="py-2 pr-3">Tool</th>
                  <th className="py-2 pr-3">Group</th>
                  <th className="py-2 pr-3">Status</th>
                  <th className="py-2 pr-3">Description</th>
                </tr>
              </thead>
              <tbody>
                {filteredTools.map((tool) => (
                  <tr key={tool.name} className="border-t border-gray-100 dark:border-gray-800">
                    <td className="py-2 pr-3 font-mono text-xs">{tool.name}</td>
                    <td className="py-2 pr-3"><Badge variant="default">{tool.group || 'unknown'}</Badge></td>
                    <td className="py-2 pr-3"><Badge variant={statusVariant(tool.status)}>{tool.status || 'canonical'}</Badge></td>
                    <td className="py-2 pr-3 text-gray-500 max-w-xl"><span className="line-clamp-2">{tool.description || '-'}</span></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>

        <div className="space-y-6">
          <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
            <h2 className="text-lg font-semibold mb-4">Tool Groups</h2>
            <div className="space-y-2">
              {Object.entries(summary.data?.tools_by_group ?? {}).sort(([a], [b]) => a.localeCompare(b)).map(([group, count]) => (
                <div key={group} className="flex justify-between text-sm">
                  <span className="text-gray-500">{group}</span>
                  <span>{count}</span>
                </div>
              ))}
            </div>
          </section>

          <section className="bg-white dark:bg-gray-900 rounded-lg border border-gray-200 dark:border-gray-800 p-5">
            <h2 className="text-lg font-semibold mb-4">Recent Sessions</h2>
            <div className="space-y-2">
              {(sessions.data?.sessions ?? []).map((session) => (
                <div key={session.session_id} className="rounded-md border border-gray-100 dark:border-gray-800 p-3">
                  <code className="block text-xs truncate">{session.session_id}</code>
                  <div className="mt-2 flex items-center justify-between text-xs text-gray-500">
                    <span>{session.count} interactions</span>
                    <span>{session.last_at ? new Date(session.last_at).toLocaleString() : '-'}</span>
                  </div>
                </div>
              ))}
              {sessions.isSuccess && (sessions.data?.sessions ?? []).length === 0 && <p className="text-sm text-gray-400">No recent sessions.</p>}
            </div>
          </section>
        </div>
      </div>
    </div>
  )
}
