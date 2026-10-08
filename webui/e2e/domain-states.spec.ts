import { test, expect, type Page } from '@playwright/test'

async function stubSession(page: Page) {
  await page.route('**/api/v1/**', route => route.fulfill({ json: {} }))
  await page.route('**/api/v1/auth/me', route => route.fulfill({ json: { id: 'domain-user', email: 'domain@test.local' } }))
  await page.route('**/api/v1/settings', route => route.fulfill({ json: { theme: 'light', locale: 'en' } }))
}

async function openWorkspace(page: Page) {
  await stubSession(page)
  await page.route('**/api/v1/workspace/read?**', route => {
    const url = new URL(route.request().url())
    return route.fulfill({ json: { project_id: url.searchParams.get('project_id'), branch: url.searchParams.get('branch'), path: url.searchParams.get('path'), text: 'server text', file_digest: 'digest-one' } })
  })
  await page.goto('/workspace')
  await page.getByLabel('Project ID', { exact: true }).fill('alpha')
}

test('workspace sends exact-read CAS digest, preserves draft on conflict and allows empty text', async ({ page }) => {
  await openWorkspace(page)
  const writes: Record<string, unknown>[] = []
  await page.route('**/api/v1/workspace/write', route => {
    writes.push(route.request().postDataJSON())
    return route.fulfill({ status: 409, json: { error: 'workspace write conflict: stale digest' } })
  })
  await expect(page.getByRole('button', { name: 'Write + Index', exact: true })).toBeDisabled()
  await page.getByRole('button', { name: 'Read', exact: true }).click()
  await expect(page.getByText('server text', { exact: true })).toBeVisible()
  await page.getByLabel('File text').fill('')
  await page.getByRole('button', { name: 'Write + Index', exact: true }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'stale digest' })).toBeVisible()
  expect(writes).toEqual([expect.objectContaining({ project_id: 'alpha', branch: 'main', path: 'docs/note.md', expected_file_digest: 'digest-one', text: '' })])
  await expect(page.getByLabel('File text')).toHaveValue('')
  await expect(page.getByRole('button', { name: 'Write + Index', exact: true })).toBeDisabled()
  await page.getByLabel('File text').fill('  ')
  await page.getByRole('button', { name: 'Read', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Write + Index', exact: true })).toBeEnabled()
  await page.getByRole('button', { name: 'Write + Index', exact: true }).click()
  await expect.poll(() => writes.length).toBe(2)
  expect(writes[1].text).toBe('  ')
  await expect(page.getByLabel('File text')).toHaveValue('  ')
})

test('workspace discards a late read and its digest after scope changes', async ({ page }) => {
  await openWorkspace(page)
  let release!: () => void
  let started!: () => void
  const requestStarted = new Promise<void>(resolve => { started = resolve })
  const responseReady = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/v1/workspace/read?**', async route => {
    started()
    await responseReady
    await route.fulfill({ json: { project_id: 'alpha', branch: 'main', path: 'docs/note.md', text: 'old scoped text', file_digest: 'old-digest' } })
  })
  await page.getByRole('button', { name: 'Read', exact: true }).click()
  await requestStarted
  await page.getByLabel('Project ID', { exact: true }).fill('beta')
  const lateResponse = page.waitForResponse(response => response.url().includes('/workspace/read?'))
  release()
  await lateResponse
  await expect(page.getByText('old scoped text', { exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Write + Index', exact: true })).toBeDisabled()
  await page.getByLabel('Project ID', { exact: true }).fill('alpha')
  await expect(page.getByRole('button', { name: 'Write + Index', exact: true })).toBeDisabled()
})

test('workspace explicit create uses absence CAS and surfaces read/job/retry denial', async ({ page }) => {
  await openWorkspace(page)
  await page.route('**/api/v1/workspace/read?**', route => route.fulfill({ status: 403, json: { error: 'read denied' } }))
  await page.route('**/api/v1/workspace/jobs?**', route => route.fulfill({ json: { jobs: [{ id: 'job-one', status: 'failed', last_error: 'index provider unavailable' }] } }))
  await page.route('**/api/v1/workspace/jobs/retry', route => route.fulfill({ status: 403, json: { error: 'retry denied' } }))
  let write: Record<string, unknown> | undefined
  await page.route('**/api/v1/workspace/write', route => {
    write = route.request().postDataJSON()
    return route.fulfill({ json: { project_id: 'beta', branch: 'main', path: 'new.md', bytes: 0 } })
  })
  await page.getByLabel('Project ID', { exact: true }).fill('beta')
  await page.getByRole('button', { name: 'Read', exact: true }).click()
  await expect(page.getByText('read denied', { exact: true })).toBeVisible()
  await expect(page.getByText('index provider unavailable', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Retry', exact: true }).click()
  await expect(page.getByText('retry denied', { exact: true })).toBeVisible()
  await page.getByLabel('Path', { exact: true }).fill('new.md')
  await page.getByLabel('Create new file (fails if it already exists)').check()
  await page.getByRole('button', { name: 'Write + Index', exact: true }).click()
  await expect.poll(() => write).toEqual(expect.objectContaining({ path: 'new.md', text: '', expected_file_digest: '' }))
  await expect(page.getByText('Wrote 0 bytes to new.md.')).toBeVisible()
})

const task = { id: 'task-one', objective: 'Inspect failed task', status: 'blocked', collection_name: 'levara', room: 'task-runtime', owner_id: 'domain-user', blocker_count: 1, step_counts: { pending: 0, claimed: 1, passed: 0, failed: 1 } }

for (const errorStatus of [403, 503]) {
test(`tasks show failures and leases; refresh ${errorStatus} hides denied data or labels stale data`, async ({ page }) => {
  await stubSession(page)
  let denied = false
  await page.route('**/api/v1/tasks?**', route => route.fulfill(denied ? { status: errorStatus, json: { error: 'tasks refresh failed' } } : { json: { tasks: [task] } }))
  await page.route('**/api/v1/tasks/task-one', route => route.fulfill(denied ? { status: errorStatus, json: { error: 'detail refresh failed' } } : { json: { ...task, criteria: [{ id: 'criterion-required', description: 'Verify protected observation', required: true }, { id: 'criterion-optional', description: 'Capture optional notes', required: false }], steps: [{ id: 'step-one', description: 'Publish failed', status: 'failed', attempts: 2, leased_by: 'worker', lease_expires_at: '2026-10-07T10:00:00Z' }], blockers: [{ reason: 'dependency unavailable' }], receipts: [{ status: 'fail', receipt_type: 'command', observation: 'exit 1' }], checkpoints: [] } }))
  await page.goto('/tasks')
  await page.getByRole('button', { name: /Inspect failed task/ }).click()
  await expect(page.getByText('Publish failed')).toBeVisible()
  const requiredCriterion = page.getByRole('listitem').filter({ hasText: 'Verify protected observation' })
  await expect(requiredCriterion).toContainText('criterion-required')
  await expect(requiredCriterion).toContainText('Required')
  const optionalCriterion = page.getByRole('listitem').filter({ hasText: 'Capture optional notes' })
  await expect(optionalCriterion).toContainText('criterion-optional')
  await expect(optionalCriterion).toContainText('Optional')
  await expect(page.getByText('exit 1', { exact: true })).toBeVisible()
  await expect(page.getByText('dependency unavailable')).toBeVisible()
  await expect(page.getByText('lease expires: 2026-10-07T10:00:00Z')).toBeVisible()
  denied = true
  await page.getByRole('button', { name: 'Refresh tasks' }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'Tasks unavailable: tasks refresh failed' })).toBeVisible({ timeout: 15000 })
  await expect(page.getByRole('alert').filter({ hasText: 'Task detail unavailable: detail refresh failed' })).toBeVisible({ timeout: 15000 })
  if (errorStatus === 403) {
    await expect(page.getByText('Publish failed')).toHaveCount(0)
    await expect(requiredCriterion).toHaveCount(0)
    await expect(optionalCriterion).toHaveCount(0)
    await expect(page.getByText('exit 1', { exact: true })).toHaveCount(0)
    await expect(page.getByRole('button', { name: /Inspect failed task/ })).toHaveCount(0)
    await expect(page.getByText(/showing stale data/)).toHaveCount(0)
  } else {
    await expect(page.getByText('Publish failed')).toBeVisible()
    await expect(requiredCriterion).toBeVisible()
    await expect(optionalCriterion).toBeVisible()
    await expect(page.getByText('exit 1', { exact: true })).toBeVisible()
    await expect(page.getByRole('alert').filter({ hasText: 'showing stale data' })).toHaveCount(2)
  }
})
}

test('tasks query failure is visible instead of empty state', async ({ page }) => {
  await stubSession(page)
  await page.route('**/api/v1/tasks?**', route => route.fulfill({ status: 403, json: { error: 'task access denied' } }))
  await page.goto('/tasks')
  await expect(page.getByRole('alert').filter({ hasText: 'task access denied' })).toBeVisible({ timeout: 15000 })
  await expect(page.getByText('No tasks match the filter', { exact: true })).toHaveCount(0)
})

test('selected task initial detail error does not leave a blank panel', async ({ page }) => {
  await stubSession(page)
  await page.route('**/api/v1/tasks?**', route => route.fulfill({ json: { tasks: [task] } }))
  await page.route('**/api/v1/tasks/task-one', route => route.fulfill({ status: 403, json: { error: 'detail access denied' } }))
  await page.goto('/tasks')
  await page.getByRole('button', { name: /Inspect failed task/ }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'Task detail unavailable: detail access denied' })).toBeVisible({ timeout: 15000 })
})

for (const status of ['error', 'partial', 'running']) {
  test(`sync HTTP200 ${status} remains truthful and denied status is not ready`, async ({ page }) => {
    await stubSession(page)
    await page.route('**/api/v1/sync/status?**', route => route.fulfill({ status: 403, json: { error: 'sync access denied' } }))
    await page.route('**/api/v1/sync/manifest', route => route.fulfill({ status: 403, json: { error: 'manifest access denied' } }))
    await page.route('**/api/v1/sync/run', route => route.fulfill({ json: { status, memories: { imported: status === 'partial' ? 1 : 0, failed: status === 'running' ? 0 : 1 } } }))
    await page.goto('/sync')
    await expect(page.getByRole('alert').filter({ hasText: 'Sync status unavailable' })).toBeVisible({ timeout: 15000 })
    await expect(page.getByText('ready', { exact: true })).toHaveCount(0)
    await expect(page.getByRole('alert').filter({ hasText: 'Manifest unavailable' })).toBeVisible({ timeout: 15000 })
    await page.getByRole('button', { name: 'Run pull', exact: true }).click()
    await expect(page.getByRole('status')).toHaveText(status === 'error' ? 'Sync failed' : status === 'partial' ? 'Sync partially completed' : 'Sync running; completion not confirmed')
  })
}

test('workspace revocation removes previously visible job details', async ({ page }) => {
  await stubSession(page)
  let revoked = false
  await page.route('**/api/v1/workspace/jobs?**', route => route.fulfill(revoked ? { status: 403, json: { error: 'workspace revoked' } } : { json: { jobs: [{ id: 'protected-job', status: 'failed', last_error: 'protected failure detail' }] } }))
  await page.goto('/workspace')
  await page.getByLabel('Project ID', { exact: true }).fill('alpha')
  await expect(page.getByText('protected failure detail', { exact: true })).toBeVisible()
  revoked = true
  await page.getByRole('button', { name: 'Refresh workspace', exact: true }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'Jobs unavailable: workspace revoked' })).toBeVisible({ timeout: 15000 })
  await expect(page.getByText('protected failure detail', { exact: true })).toHaveCount(0)
  await expect(page.getByText('protected-job', { exact: true })).toHaveCount(0)
})

test('sync revocation removes protected events and collection selector requires explicit names', async ({ page }) => {
  await stubSession(page)
  let revoked = false
  await page.route('**/api/v1/sync/status?**', route => route.fulfill(revoked ? { status: 403, json: { error: 'sync revoked' } } : { json: { events: [{ id: 'event-one', remote: 'protected-peer', direction: 'pull', types: ['memories'] }] } }))
  await page.goto('/sync')
  await expect(page.getByText('protected-peer', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'collections', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Run pull', exact: true })).toBeDisabled()
  await expect(page.getByText('Select explicit collection names before syncing collections.')).toBeVisible()
  await page.getByLabel('Collections', { exact: true }).fill('levara')
  await expect(page.getByRole('button', { name: 'Run pull', exact: true })).toBeEnabled()
  revoked = true
  await page.getByRole('button', { name: 'Refresh sync status', exact: true }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'Sync status unavailable: sync revoked' })).toBeVisible({ timeout: 15000 })
  await expect(page.getByText('protected-peer', { exact: true })).toHaveCount(0)
})

test('workspace observation revocation clears exact-read and search authority until fresh authorized reads', async ({ page }) => {
  await openWorkspace(page)
  let revoked = false
  await page.route('**/api/v1/workspace/jobs?**', route => route.fulfill(revoked ? { status: 403, json: { error: 'project observation revoked' } } : { json: { jobs: [] } }))
  await page.route('**/api/v1/workspace/search', route => route.fulfill({ json: { results: [{ path: 'protected-hit.md', text: 'protected search text' }] } }))
  await page.getByLabel('File text').fill('preserved draft')
  await page.getByRole('button', { name: 'Read', exact: true }).click()
  await expect(page.getByText('server text', { exact: true })).toBeVisible()
  await page.getByPlaceholder('rate limiting architecture').fill('protected')
  await page.getByRole('button', { name: 'Search', exact: true }).click()
  await expect(page.getByText('protected search text', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Write + Index', exact: true })).toBeEnabled()
  revoked = true
  await page.getByRole('button', { name: 'Refresh workspace', exact: true }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'Jobs unavailable: project observation revoked' })).toBeVisible({ timeout: 15000 })
  await expect(page.getByText('server text', { exact: true })).toHaveCount(0)
  await expect(page.getByText('protected search text', { exact: true })).toHaveCount(0)
  await expect(page.getByText('protected-hit.md', { exact: true })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Write + Index', exact: true })).toBeDisabled()
  await expect(page.getByLabel('File text')).toHaveValue('preserved draft')
  revoked = false
  await page.getByRole('button', { name: 'Refresh workspace', exact: true }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'project observation revoked' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Write + Index', exact: true })).toBeDisabled()
  await expect(page.getByText('server text', { exact: true })).toHaveCount(0)
  await expect(page.getByText('protected search text', { exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: 'Read', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Write + Index', exact: true })).toBeEnabled()
  await expect(page.getByLabel('File text')).toHaveValue('preserved draft')
})

test('workspace observation503 retains explicitly stale read and search results without losing draft', async ({ page }) => {
  await openWorkspace(page)
  let unavailable = false
  await page.route('**/api/v1/workspace/jobs?**', route => route.fulfill(unavailable ? { status: 503, json: { error: 'observation temporarily unavailable' } } : { json: { jobs: [] } }))
  await page.route('**/api/v1/workspace/search', route => route.fulfill({ json: { results: [{ path: 'known-hit.md', text: 'known search text' }] } }))
  await page.getByRole('button', { name: 'Refresh workspace', exact: true }).click()
  await page.getByLabel('File text').fill('preserved network draft')
  await page.getByRole('button', { name: 'Read', exact: true }).click()
  await expect(page.getByText('server text', { exact: true })).toBeVisible()
  await page.getByPlaceholder('rate limiting architecture').fill('known')
  await page.getByRole('button', { name: 'Search', exact: true }).click()
  await expect(page.getByText('known search text', { exact: true })).toBeVisible()
  unavailable = true
  await page.getByRole('button', { name: 'Refresh workspace', exact: true }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'Jobs unavailable: observation temporarily unavailable (showing stale data)' })).toBeVisible({ timeout: 15000 })
  await expect(page.getByText('server text', { exact: true })).toBeVisible()
  await expect(page.getByText('known search text', { exact: true })).toBeVisible()
  await expect(page.getByLabel('File text')).toHaveValue('preserved network draft')
  await expect(page.getByRole('button', { name: 'Write + Index', exact: true })).toBeEnabled()
})
