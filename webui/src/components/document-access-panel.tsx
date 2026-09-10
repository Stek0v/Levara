'use client'

import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ApiError, levara, type DocumentRole } from '@/lib/api'
import { useT } from '@/lib/i18n'
import { Button } from '@/components/ui/button'
import { Modal } from '@/components/ui/modal'

interface Props {
  datasetId: string
  document: { id: string; name: string }
  open: boolean
  onClose: () => void
}

export function DocumentAccessPanel({ datasetId, document, open, onClose }: Props) {
  const t = useT()
  const client = useQueryClient()
  const key = ['document-policy', datasetId, document.id]
  const policy = useQuery({
    queryKey: key,
    queryFn: () => levara.getDocumentPolicy(datasetId, document.id),
    enabled: open,
    retry: false,
  })
  const recipients = useQuery({
    queryKey: ['document-recipients', datasetId, document.id],
    queryFn: () => levara.getDocumentRecipients(datasetId, document.id),
    enabled: open && policy.isSuccess,
    retry: false,
  })
  const [kind, setKind] = useState<'user' | 'group'>('user')
  const [principalId, setPrincipalId] = useState('')
  const [role, setRole] = useState<DocumentRole>('viewer')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const options = kind === 'user' ? (recipients.data?.users ?? []) : (recipients.data?.groups ?? [])
  const selectedId = options.some((item) => item.id === principalId) ? principalId : options[0]?.id ?? ''
  const principalName = (principalKind: string, id: string) => {
    if (principalKind === 'user') return recipients.data?.users.find((item) => item.id === id)?.email ?? id
    return recipients.data?.groups.find((item) => item.id === id)?.name ?? id
  }
  const refresh = async () => {
    await client.invalidateQueries({ queryKey: key })
    await client.invalidateQueries({ queryKey: ['shared-documents'] })
  }
  const mutate = async (operation: (revision: number) => Promise<unknown>) => {
    if (!policy.data) return
    setBusy(true)
    setError('')
    try {
      await operation(policy.data.acl_revision)
      await refresh()
    } catch (cause) {
      if (cause instanceof ApiError && cause.status === 409) {
        await policy.refetch()
        setError(t('document.access.stale'))
      } else {
        setError(cause instanceof Error ? cause.message : t('document.access.error'))
      }
    } finally {
      setBusy(false)
    }
  }
  const register = async () => {
    setBusy(true)
    setError('')
    try {
      await levara.registerDocumentPolicy(datasetId, document.id)
      await refresh()
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t('document.access.error'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={t('document.access.title')} description={document.name} size="lg">
      {policy.isLoading && <p className="text-sm text-gray-500">{t('common.loading')}</p>}
      {policy.error instanceof ApiError && policy.error.status === 404 && (
        <div className="space-y-3">
          <p className="text-sm text-gray-600 dark:text-gray-300">{t('document.access.unregistered')}</p>
          <Button onClick={register} loading={busy}>{t('document.access.enable')}</Button>
        </div>
      )}
      {policy.error && (!(policy.error instanceof ApiError) || policy.error.status !== 404) && (
        <p role="alert" className="text-sm text-red-600">{policy.error.message}</p>
      )}
      {policy.data && (
        <div className="space-y-4">
          <p className="text-xs text-gray-500">
            {t('document.access.policy')}: <strong>{policy.data.mode}</strong> · {t('document.access.revision')} {policy.data.acl_revision}
          </p>
          {error && <p role="alert" className="text-sm text-red-600">{error}</p>}
          {recipients.error && <p role="alert" className="text-sm text-red-600">{recipients.error.message}</p>}
          <div className="grid gap-2 sm:grid-cols-[7rem_1fr_8rem_auto]">
            <select aria-label={t('document.access.kind')} value={kind} onChange={(event) => { setKind(event.target.value as 'user' | 'group'); setPrincipalId('') }} className="h-9 rounded-md border bg-transparent px-2 text-sm">
              <option value="user">{t('document.access.user')}</option>
              <option value="group">{t('document.access.group')}</option>
            </select>
            <select aria-label={t('document.access.recipient')} value={selectedId} onChange={(event) => setPrincipalId(event.target.value)} className="h-9 min-w-0 rounded-md border bg-transparent px-2 text-sm">
              {options.map((item) => <option key={item.id} value={item.id}>{kind === 'user' ? 'email' in item && item.email : 'name' in item && item.name}</option>)}
            </select>
            <select aria-label={t('project.shares.role')} value={role} onChange={(event) => setRole(event.target.value as DocumentRole)} className="h-9 rounded-md border bg-transparent px-2 text-sm">
              {(['viewer', 'editor', 'admin'] as DocumentRole[]).map((item) => <option key={item} value={item}>{t(`project.shares.role.${item}`)}</option>)}
            </select>
            <Button size="sm" disabled={!selectedId} loading={busy} onClick={() => mutate((revision) => levara.grantDocument(datasetId, document.id, revision, kind, selectedId, role))}>{t('project.shares.grant')}</Button>
          </div>
          {!recipients.isLoading && options.length === 0 && <p className="text-xs text-gray-500">{t('document.access.noRecipients')}</p>}
          <div className="space-y-2">
            {policy.data.grants.length === 0 && <p className="text-sm text-gray-500">{t('project.shares.empty')}</p>}
            {policy.data.grants.map((grant) => (
              <div key={`${grant.principal_kind}:${grant.principal_id}`} className="flex items-center justify-between rounded-md border px-3 py-2 text-sm">
                <span>{principalName(grant.principal_kind, grant.principal_id)} · {t(`project.shares.role.${grant.role}`)}</span>
                <Button variant="ghost" size="sm" loading={busy} onClick={() => mutate((revision) => levara.revokeDocument(datasetId, document.id, revision, grant.principal_kind, grant.principal_id))}>{t('project.shares.revoke')}</Button>
              </div>
            ))}
          </div>
        </div>
      )}
    </Modal>
  )
}
