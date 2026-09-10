import { expect, test, type Page } from '@playwright/test'

const dataset = { id: 'alpha', name: 'Alpha', record_count: 1, created_at: '2026-09-10T00:00:00Z', updated_at: '2026-09-10T00:00:00Z' }
const record = { id: 'blob', name: 'Quarterly plan.pdf', extension: '.pdf', pipeline_status: '{}' }

async function mockSharingAPI(page: Page, registered = true) {
  const state = {
    registered,
    revision: 2,
    conflictNext: false,
    grants: [] as Array<{ principal_kind: 'user' | 'group'; principal_id: string; role: 'viewer' | 'editor' | 'admin' }>,
  }
  await page.addInitScript(() => localStorage.setItem('levara_token', 'document-test-token'))
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname
    if (path === '/api/v1/auth/me') return route.fulfill({ json: { id: 'owner', email: 'owner@test.local' } })
    if (path === '/api/v1/settings') return route.fulfill({ json: { locale: 'en', theme: 'light' } })
    if (path === '/api/v1/datasets') return route.fulfill({ json: [dataset] })
    if (path === `/api/v1/datasets/${dataset.id}/data`) return route.fulfill({ json: [record] })
    if (path === `/api/v1/datasets/${dataset.id}/shares`) return route.fulfill({ json: [] })
    if (path === '/api/v1/documents/shared') return route.fulfill({ json: { limit: 50, documents: [{
      dataset_id: dataset.id, dataset_name: dataset.name, data_id: record.id, name: record.name,
      tenant_id: 'tenant-a', mode: 'restricted', role: 'viewer', acl_revision: 2, content_revision: 1, hold: false,
    }] } })
    const base = `/api/v1/datasets/${dataset.id}/data/${record.id}`
    if (path === `${base}/policy`) {
      if (request.method() === 'POST') {
        state.registered = true
        state.revision = 1
      }
      if (!state.registered) return route.fulfill({ status: 404, json: { error: 'not found' } })
      return route.fulfill({ json: {
        dataset_id: dataset.id, data_id: record.id, tenant_id: 'tenant-a', mode: 'restricted',
        acl_revision: state.revision, content_revision: 1, hold: false, grants: state.grants,
      } })
    }
    if (path === `${base}/recipients`) return route.fulfill({ json: {
      tenant_id: 'tenant-a', users: [{ id: 'peer', email: 'peer@test.local' }],
      groups: [{ id: 'reviewers', name: 'Reviewers', revision: 1, member_count: 2, externally_managed: false }],
    } })
    if (path === `${base}/grants` && request.method() === 'POST') {
      if (state.conflictNext) {
        state.conflictNext = false
        state.revision++
        return route.fulfill({ status: 409, json: { error: 'policy version conflict' } })
      }
      const body = request.postDataJSON()
      expect(body.acl_revision).toBe(state.revision)
      state.revision++
      state.grants = [...state.grants.filter((grant) => grant.principal_kind !== body.principal_kind || grant.principal_id !== body.principal_id), body]
      return route.fulfill({ json: { acl_revision: state.revision } })
    }
    if (path.startsWith(`${base}/grants/`) && request.method() === 'DELETE') {
      const body = request.postDataJSON()
      expect(body.acl_revision).toBe(state.revision)
      const [, kind, id] = path.slice(`${base}/grants/`.length).match(/^([^/]+)\/(.+)$/)!
      state.revision++
      state.grants = state.grants.filter((grant) => grant.principal_kind !== kind || grant.principal_id !== decodeURIComponent(id))
      return route.fulfill({ json: { acl_revision: state.revision } })
    }
    return route.fulfill({ json: [] })
  })
  return state
}

test('manager grants users and groups, refreshes stale CAS, and revokes', async ({ page }) => {
  const state = await mockSharingAPI(page)
  await page.goto(`/datasets/${dataset.id}`)
  await page.getByRole('button', { name: `Document access: ${record.name}` }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog.getByText('restricted', { exact: true })).toBeVisible()
  await expect(dialog.getByRole('option', { name: 'peer@test.local' })).toBeAttached()
  await dialog.getByRole('button', { name: 'Grant access' }).click()
  await expect(dialog.getByText(/peer@test.local.*Viewer/)).toBeVisible()

  state.conflictNext = true
  await dialog.getByLabel('Role').selectOption('editor')
  await dialog.getByRole('button', { name: 'Grant access' }).click()
  await expect(dialog.getByText('The policy changed. Data was refreshed; try again.')).toBeVisible()
  await expect(dialog.getByText(`revision ${state.revision}`, { exact: false })).toBeVisible()

  await dialog.getByLabel('Recipient type').selectOption('group')
  await dialog.getByRole('button', { name: 'Grant access' }).click()
  await expect(dialog.getByText(/Reviewers.*Editor/)).toBeVisible()
  await dialog.getByText(/peer@test.local.*Viewer/).locator('..').getByRole('button', { name: 'Revoke' }).click()
  await expect(dialog.getByText(/peer@test.local.*Viewer/)).toHaveCount(0)
})

test('unregistered document can enable an individual restricted policy', async ({ page }) => {
  const state = await mockSharingAPI(page, false)
  await page.goto(`/datasets/${dataset.id}`)
  await page.getByRole('button', { name: `Document access: ${record.name}` }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByRole('button', { name: 'Enable document access' }).click()
  await expect(dialog.getByText('restricted', { exact: true })).toBeVisible()
  expect(state.registered).toBe(true)
})

test('directly shared documents are discoverable from the projects page', async ({ page }) => {
  await mockSharingAPI(page)
  await page.goto('/datasets')
  await expect(page.getByRole('heading', { name: 'Documents shared directly with you' })).toBeVisible()
  await expect(page.getByRole('button', { name: /Quarterly plan.pdf.*Alpha/ })).toBeVisible()
})
