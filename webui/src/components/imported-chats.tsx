'use client'

import { useCallback, useEffect, useRef, useState } from 'react'
import Link from 'next/link'
import { levara, type ImportedChatDetail, type ImportedChatIdentity, type ImportedChatSession } from '@/lib/api'
import { Button } from '@/components/ui/button'

function sameChat(a: ImportedChatIdentity, b: ImportedChatIdentity) {
  return a.chat_id === b.chat_id && a.platform === b.platform && a.session_id === b.session_id
}

export function ImportedChats({ userId, onAccountChanged }: { userId: string; onAccountChanged: () => void }) {
  const [sessions, setSessions] = useState<ImportedChatSession[]>([])
  const [selected, setSelected] = useState<ImportedChatSession | null>(null)
  const [detail, setDetail] = useState<ImportedChatDetail | null>(null)
  const [project, setProject] = useState('')
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const epoch = useRef(0)
  const controller = useRef<AbortController | null>(null)
  const selection = useRef<ImportedChatSession | null>(null)

  const begin = useCallback(() => {
    controller.current?.abort()
    controller.current = new AbortController()
    setDetail(null)
    setSaving(false)
    setError('')
    setLoading(true)
    return { version: ++epoch.current, signal: controller.current.signal }
  }, [])

  const refresh = useCallback(async (chat: ImportedChatSession | null, list: boolean) => {
    const { version, signal } = begin()
    selection.current = chat
    setSelected(chat)
    try {
      const user = await levara.me()
      if (version !== epoch.current) return
      if (user.id !== userId) {
        setSessions([])
        selection.current = null
        setSelected(null)
        onAccountChanged()
        return
      }
      if (list) {
        const response = await levara.importedChats(signal)
        if (version !== epoch.current) return
        const items = Array.isArray(response?.sessions) ? response.sessions : []
        setSessions(items)
        chat = chat ? items.find(item => sameChat(item, chat!)) || null : null
        selection.current = chat
        setSelected(chat)
      }
      if (chat) {
        const response = await levara.importedChat(chat, signal)
        if (version !== epoch.current) return
        if (!response || !sameChat(chat, response) || !Array.isArray(response.messages)) {
          throw new Error('Invalid imported chat response.')
        }
        setDetail(response)
        setProject(response.project_id || '')
      }
    } catch (err) {
      if (version !== epoch.current) return
      if (list) setSessions([])
      setError(err instanceof Error ? err.message : 'Unable to load imported chats.')
    } finally {
      if (version === epoch.current) setLoading(false)
    }
  }, [begin, userId, onAccountChanged])

  useEffect(() => {
    void refresh(null, true)
    const recheck = () => { void refresh(selection.current, true) }
    const credentialsChanged = (event: StorageEvent) => {
      if (event.key === 'levara_token' || event.key === null) recheck()
    }
    window.addEventListener('focus', recheck)
    window.addEventListener('storage', credentialsChanged)
    return () => {
      ++epoch.current
      controller.current?.abort()
      window.removeEventListener('focus', recheck)
      window.removeEventListener('storage', credentialsChanged)
    }
  }, [refresh])

  const changeProject = async (projectId: string) => {
    const chat = selection.current
    if (!chat || !detail || saving) return
    const version = epoch.current
    setSaving(true)
    setError('')
    try {
      const ack = await levara.setImportedChatProject(chat, projectId, controller.current?.signal)
      if (version !== epoch.current) return
      if (!ack || !sameChat(chat, ack) || ack.project_id !== projectId) throw new Error('Invalid sharing acknowledgement. Refresh before retrying.')
      await refresh(chat, true)
    } catch (err) {
      if (version !== epoch.current) return
      // A failed response cannot confirm whether sharing changed; require a fresh read.
      setDetail(null)
      setError(err instanceof Error ? err.message : 'Unable to change project sharing.')
    } finally {
      if (version === epoch.current) setSaving(false)
    }
  }

  return <section aria-label="Imported chats" className="mb-6 rounded-lg border border-gray-200 dark:border-gray-800 p-4 space-y-3">
    <div className="flex items-center justify-between">
      <h2 className="text-lg font-semibold">Imported chats</h2>
      <Button variant="ghost" size="sm" onClick={() => { void refresh(selection.current, true) }}>Refresh imported chats</Button>
    </div>
    <p className="text-sm text-gray-500">Owners consent to project sharing. Project administrators manage the audience and can revoke sharing. The server checks each action.</p>
    {loading && <p role="status">Loading imported chats…</p>}
    {error && <p role="alert">{error}</p>}
    {!loading && !error && sessions.length === 0 && <p>No imported chats available.</p>}
    <ul className="flex flex-wrap gap-2">
      {sessions.map(chat => <li key={chat.chat_id + ':' + chat.platform}>
        <Button variant={selected && sameChat(selected, chat) ? 'primary' : 'secondary'} size="sm" onClick={() => { void refresh(chat, false) }}>{chat.title || chat.session_id}</Button>
      </li>)}
    </ul>
    {detail && <div className="space-y-3">
      <h3 className="font-medium">{selected?.title || detail.session_id}</h3>
      <p>Project: {detail.project_id || 'Private'}</p>
      {detail.project_id && <Link className="text-blue-600 underline" href={`/datasets/${encodeURIComponent(detail.project_id)}`}>Manage project audience</Link>}
      <form className="flex flex-wrap items-center gap-2" onSubmit={event => { event.preventDefault(); void changeProject(project.trim()) }}>
        <label>Project ID <input aria-label="Imported chat project ID" value={project} onChange={event => setProject(event.target.value)} className="rounded border px-2 py-1" /></label>
        <Button type="submit" size="sm" disabled={saving || !project.trim()}>Share with project</Button>
        {detail.project_id && <Button type="button" variant="secondary" size="sm" disabled={saving} onClick={() => { void changeProject('') }}>Revoke project sharing</Button>}
      </form>
      {saving && <p role="status">Updating project sharing…</p>}
      <ol className="max-h-80 overflow-y-auto space-y-2" aria-label="Imported chat messages">
        {detail.messages.map(message => <li key={message.external_id + ':' + message.ordinal} className="rounded border p-2">
          <span className="text-xs text-gray-500">{message.role}</span><p className="whitespace-pre-wrap">{message.content}</p>
        </li>)}
      </ol>
      {detail.messages.length === 0 && <p>No messages in this imported chat.</p>}
    </div>}
  </section>
}
