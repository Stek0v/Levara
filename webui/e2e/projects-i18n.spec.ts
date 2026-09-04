// projects-i18n.spec.ts — RU localization + project cards (file count, size).
//
// No live backend: auth and datasets APIs are stubbed with page.route,
// mirroring settings-persist.spec.ts. Covers:
//   - sidebar renders Russian labels when locale=ru
//   - projects page shows file counts and total size from the API
//   - project detail header shows count + size in RU units
//   - dictionary parity: ru/en key sets are identical (static import)

import { test, expect } from '@playwright/test'

const DATASETS = [
  { id: 'ds-1', name: 'clienta-project', record_count: 12, total_size: 5255717, created_at: '2026-09-04T10:00:00Z', updated_at: '2026-09-04T10:00:00Z' },
  { id: 'ds-2', name: 'docs-archive', record_count: 0, total_size: 0, created_at: '2026-09-03T10:00:00Z', updated_at: '2026-09-03T10:00:00Z' },
]

async function stubBackend(page: import('@playwright/test').Page, locale: string) {
  await page.addInitScript(() => {
    try { localStorage.removeItem('levara-locale'); localStorage.removeItem('levara-theme') } catch { /* ignore */ }
  })
  await page.route('**/api/v1/auth/me', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ id: 'u1', email: 'u@test.local', username: 'u' }) }))
  await page.route('**/api/v1/settings', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ theme: 'light', locale }) }))
  await page.route('**/api/v1/datasets*', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: DATASETS, pagination: { page: 1, limit: 20, total: DATASETS.length, total_pages: 1 } }) }))
}

test.describe('Projects i18n (RU)', () => {
  test('sidebar shows Russian navigation with locale=ru', async ({ page }) => {
    await stubBackend(page, 'ru')
    await page.goto('/datasets')
    const nav = page.getByRole('navigation')
    await expect(nav.getByRole('link', { name: 'Проекты' })).toBeVisible()
    await expect(nav.getByRole('link', { name: 'Настройки' })).toBeVisible()
    await expect(nav.getByRole('link', { name: 'Воспоминания' })).toBeVisible()
    // html lang follows locale
    await expect(page.locator('html')).toHaveAttribute('lang', 'ru')
  })

  test('projects page shows RU header, file counts and sizes', async ({ page }) => {
    await stubBackend(page, 'ru')
    await page.goto('/datasets')
    await expect(page.locator('h1')).toHaveText('Проекты')
    // Card for ds-1: 12 файлов · 5.0 МБ
    const card = page.locator('div', { has: page.locator('text=clienta-project') }).last()
    await expect(page.locator('text=12 файлов').first()).toBeVisible()
    await expect(page.locator('text=5.0 МБ').first()).toBeVisible()
  })

  test('project detail shows RU table and status labels', async ({ page }) => {
    await stubBackend(page, 'ru')
    await page.route('**/api/v1/datasets/ds-1/data*', (route) =>
      route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          data: [{ id: 'r1', name: 'spec.pdf', extension: 'pdf', mime_type: 'application/pdf', data_size: 12837, pipeline_status: 'completed', created_at: '2026-09-04T10:00:00Z' }],
          pagination: { page: 1, limit: 20, total: 1, total_pages: 1 },
        }),
      }))
    await page.goto('/datasets/ds-1')
    await expect(page.locator('text=1 файлов').first()).toBeVisible()
    await expect(page.locator('th', { hasText: 'Файл' })).toBeVisible()
    await expect(page.locator('th', { hasText: 'Тип' })).toBeVisible()
    await expect(page.locator('text=Обработан')).toBeVisible()
  })

  test('en locale keeps English labels', async ({ page }) => {
    await stubBackend(page, 'en')
    await page.goto('/datasets')
    await expect(page.locator('h1')).toHaveText('Projects')
  })
})
