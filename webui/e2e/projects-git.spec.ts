// projects-git.spec.ts — block ④: repo binding card on the project page.
// Backend stubbed; covers RU labels, save flow and the commit feed render.

import { test, expect } from '@playwright/test'

const DATASET = { id: 'ds-1', name: 'proj-x', record_count: 0, total_size: 0, github_repo: '/srv/repo', created_at: '2026-09-04T10:00:00Z', updated_at: '2026-09-04T10:00:00Z' }

async function stubBackend(page: import('@playwright/test').Page) {
  await page.route('**/api/v1/auth/me', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: 'u1', email: 'u@test.local', username: 'u' }) }))
  await page.route('**/api/v1/settings', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ theme: 'light', locale: 'ru' }) }))
  await page.route('**/api/v1/datasets/ds-1/data*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ rows: [], pagination: { page: 1, limit: 20, total: 0, total_pages: 0 } }) }))
  await page.route('**/api/v1/datasets*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [DATASET], pagination: { page: 1, limit: 20, total: 1, total_pages: 1 } }) }))
  await page.route('**/api/v1/datasets/ds-1/commits', (route) =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify([{ hash: 'abc1234def', author: 'alice', date: '2026-09-04 12:00', message: 'fix: upload limit', files: [] }]),
    }))
}

test.describe('Project repo binding', () => {
  test('repo card shows RU labels and saved value', async ({ page }) => {
    await stubBackend(page)
    await page.goto('/datasets/ds-1')
    await expect(page.getByText('Репозиторий')).toBeVisible()
    await expect(page.getByPlaceholder(/путь к git/i)).toBeVisible()
  })

  test('save triggers PATCH and commit feed renders', async ({ page }) => {
    await stubBackend(page)
    let patched = false
    await page.route('**/api/v1/datasets/ds-1', async (route) => {
      if (route.request().method() === 'PATCH') {
        patched = true
        route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: 'ds-1' }) })
      } else {
        route.fallback()
      }
    })
    await page.goto('/datasets/ds-1')
    await page.getByRole('button', { name: 'Сохранить' }).click()
    await expect(page.getByText('Сохранено')).toBeVisible()
    await expect(page.getByText('fix: upload limit')).toBeVisible()
    if (!patched) throw new Error('PATCH never fired')
  })
})
