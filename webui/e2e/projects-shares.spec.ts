// projects-shares.spec.ts — block ⑤: access management card.
// Backend stubbed; covers the share list, grant flow and revoke.

import { test, expect } from '@playwright/test'

const DATASET = { id: 'ds-1', name: 'proj-x', record_count: 0, total_size: 0, created_at: '2026-09-04T10:00:00Z', updated_at: '2026-09-04T10:00:00Z' }
const SHARE = { id: 'sh-1', dataset_id: 'ds-1', user_id: 'u-carol', user_email: 'carol@example.com', role: 'viewer', created_at: '2026-09-04T11:00:00Z' }

async function stubBackend(page: import('@playwright/test').Page, shares: unknown[] = []) {
  await page.route('**/api/v1/auth/me', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: 'u1', email: 'u@test.local', username: 'u' }) }))
  await page.route('**/api/v1/settings', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ theme: 'light', locale: 'ru' }) }))
  await page.route('**/api/v1/datasets/ds-1/data*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ rows: [], pagination: { page: 1, limit: 20, total: 0, total_pages: 0 } }) }))
  await page.route('**/api/v1/datasets/ds-1/shares', (route) => {
    const m = route.request().method()
    if (m === 'GET') {
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(shares) })
    } else if (m === 'POST') {
      route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ ...SHARE, role: 'editor' }) })
    } else {
      route.fulfill({ status: 200, contentType: 'application/json', body: '{}' })
    }
  })
  await page.route('**/api/v1/datasets/ds-1/shares/sh-1', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: '{}' }))
  await page.route('**/api/v1/datasets*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [DATASET], pagination: { page: 1, limit: 20, total: 1, total_pages: 1 } }) }))
}

test.describe('Project access management', () => {
  test('shows existing shares with RU role labels', async ({ page }) => {
    await stubBackend(page, [SHARE])
    await page.goto('/datasets/ds-1')
    await expect(page.getByRole('heading', { name: 'Доступ' })).toBeVisible()
    await expect(page.getByText('carol@example.com')).toBeVisible()
    await expect(page.locator('span', { hasText: 'Наблюдатель' })).toBeVisible()
  })

  test('empty state when no shares', async ({ page }) => {
    await stubBackend(page, [])
    await page.goto('/datasets/ds-1')
    await expect(page.getByText('Проект не расшарен никому')).toBeVisible()
  })

  test('grant then revoke flows hit the API', async ({ page }) => {
    await stubBackend(page, [SHARE])
    let posted = false
    let deleted = false
    await page.route('**/api/v1/datasets/ds-1/shares', async (route) => {
      if (route.request().method() === 'POST') { posted = true }
      route.fallback()
    })
    await page.route('**/api/v1/datasets/ds-1/shares/sh-1', async (route) => {
      if (route.request().method() === 'DELETE') { deleted = true }
      route.fulfill({ status: 200, contentType: 'application/json', body: '{}' })
    })
    await page.goto('/datasets/ds-1')
    await page.getByPlaceholder(/email пользователя/i).fill('bob@example.com')
    await page.getByRole('button', { name: 'Выдать доступ' }).click()
    await expect.poll(() => posted).toBe(true)
    await page.getByTitle('Отозвать').click()
    await expect.poll(() => deleted).toBe(true)
  })
})
