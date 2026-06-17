// invalidation.spec.ts — T20 coverage for React Query invalidation after
// mutations (T7). The typical regression looks like: user creates a
// dataset, but the table doesn't update without a manual refresh.

import { test, expect } from '@playwright/test'

test.describe('React Query invalidation (T20)', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ id: 'u1', email: 'u@test.local', username: 'u' }),
      }),
    )
    await page.route('**/api/v1/info', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ dimension: 768, shards: 1, status: 'ready' }),
      }),
    )
  })

  test('create dataset → list reflects new row without reload', async ({ page }) => {
    let datasets: Array<{ id: string; name: string; record_count: number; created_at: string; updated_at: string }> = []
    await page.route('**/api/v1/datasets*', (route) => {
      const method = route.request().method()
      if (method === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(datasets),
        })
      }
      if (method === 'POST') {
        const body = route.request().postDataJSON() as { name: string }
        const row = {
          id: 'd-' + datasets.length,
          name: body.name,
          record_count: 0,
          created_at: '2026-04-21T00:00:00Z',
          updated_at: '2026-04-21T00:00:00Z',
        }
        datasets = [row, ...datasets]
        return route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify(row),
        })
      }
      return route.continue()
    })
    await page.route('**/api/v1/collections', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      }),
    )

    await page.goto('/datasets')
    // Open the create form — button label varies; we try both.
    const createBtn = page.getByRole('button', { name: /new dataset|create/i }).first()
    await createBtn.click()
    await page.getByLabel(/name/i).fill('playwright-ds')
    const confirm = page.getByRole('button', { name: /^(create|save)$/i }).first()
    await confirm.click()

    // Without page reload, the dataset should appear in the list thanks to
    // queryClient.invalidateQueries(['datasets']).
    await expect(page.locator('main span.font-medium', { hasText: 'playwright-ds' })).toBeVisible({ timeout: 5000 })
  })
})
