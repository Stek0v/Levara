// projects-context.spec.ts — block ③: Context/History tabs on project page.
// Backend stubbed via page.route; asserts RU labels, tab switching, and
// that context/history payloads render.

import { test, expect } from '@playwright/test'

const DATASET = { id: 'ds-1', name: 'proj-x', record_count: 2, total_size: 2048, created_at: '2026-09-04T10:00:00Z', updated_at: '2026-09-04T10:00:00Z' }

async function stubBackend(page: import('@playwright/test').Page) {
  await page.route('**/api/v1/auth/me', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: 'u1', email: 'u@test.local', username: 'u' }) }))
  await page.route('**/api/v1/settings', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ theme: 'light', locale: 'ru' }) }))
  await page.route('**/api/v1/datasets/ds-1', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(DATASET) }))
  await page.route('**/api/v1/datasets*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [DATASET], pagination: { page: 1, limit: 20, total: 1, total_pages: 1 } }) }))
  await page.route('**/api/v1/datasets/ds-1/data*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ rows: [], pagination: { page: 1, limit: 20, total: 0, total_pages: 0 } }) }))
  await page.route('**/api/v1/datasets/ds-1/context', (route) =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify([{ id: 'm-1', key: 'arch', value: 'Монолит на Go', type: 'decision', created_at: '2026-09-04T12:00:00Z' }]),
    }))
  await page.route('**/api/v1/datasets/ds-1/activity', (route) =>
    route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify([
        { type: 'upload', title: 'spec.pdf', detail: 'completed', created_at: '2026-09-04T10:05:00Z' },
        { type: 'share_granted', title: 'carol@example.com', detail: 'viewer', created_at: '2026-09-04T11:00:00Z' },
      ]),
    }))
}

test.describe('Project context & history tabs', () => {
  test('context tab shows collection memories', async ({ page }) => {
    await stubBackend(page)
    await page.goto('/datasets/ds-1')
    await page.getByRole('button', { name: 'Контекст' }).click()
    await expect(page.getByText('arch')).toBeVisible()
    await expect(page.getByText('Монолит на Go')).toBeVisible()
  })

  test('history tab shows merged activity feed', async ({ page }) => {
    await stubBackend(page)
    await page.goto('/datasets/ds-1')
    await page.getByRole('button', { name: 'История' }).click()
    await expect(page.getByText('Загрузка файла')).toBeVisible()
    await expect(page.getByText('Выдан доступ')).toBeVisible()
    await expect(page.getByText('spec.pdf')).toBeVisible()
  })

  test('files tab remains default', async ({ page }) => {
    await stubBackend(page)
    await page.goto('/datasets/ds-1')
    await expect(page.getByRole('button', { name: 'Файлы' })).toBeVisible()
    // Files view is active by default: search input (files-only) visible.
    await expect(page.getByPlaceholder(/поиск/i)).toBeVisible()
  })
})
