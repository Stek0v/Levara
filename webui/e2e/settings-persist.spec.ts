// settings-persist.spec.ts — T20 coverage for T9 theme/locale persistence.
//
// The live-state flow we care about: user changes theme, optimistic
// update flips DOM, PUT /settings succeeds, refresh loads theme from
// backend cache. On PUT failure the rollback reverts both cache AND DOM
// (M10 fix). No live backend required — page.route stubs it.

import { test, expect } from '@playwright/test'

test.describe('Settings persistence (T20)', () => {
  test.beforeEach(async ({ page }) => {
    // Clear any localStorage left over from other specs so the initial
    // theme resolution is deterministic.
    await page.addInitScript(() => {
      try {
        localStorage.removeItem('levara-theme')
        localStorage.removeItem('levara-locale')
      } catch {
        // ignore
      }
    })
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ id: 'u1', email: 'u@test.local', username: 'u' }),
      }),
    )
  })

  test('theme change persists across reload when backend accepts', async ({ page }) => {
    let stored: Record<string, unknown> = { theme: 'system', locale: 'ru' }
    await page.route('**/api/v1/settings', (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify(stored),
        })
      }
      // PUT — merge into stored and echo back empty success.
      const body = route.request().postDataJSON() as Record<string, unknown>
      stored = { ...stored, ...body }
      return route.fulfill({ status: 200, contentType: 'application/json', body: '' })
    })

    await page.goto('/settings')
    await page.getByRole('button', { name: /^dark$/i }).click()
    await expect(page.locator('html')).toHaveClass(/dark/)

    // Reload — settings page re-reads from backend, which now returns dark.
    await page.reload()
    await expect(page.locator('html')).toHaveClass(/dark/, { timeout: 3000 })
  })

  test('failed PUT reverts DOM via useEffect rollback (M10)', async ({ page }) => {
    await page.route('**/api/v1/settings', (route) => {
      if (route.request().method() === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ theme: 'light', locale: 'ru' }),
        })
      }
      // Every PUT fails.
      return route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ error: { message: 'settings upstream down' } }),
      })
    })

    await page.goto('/settings')
    await expect(page.locator('html')).not.toHaveClass(/dark/)

    // Click dark — optimistic update flips the class briefly, then the
    // onError rollback in useUpdateSettings restores the prev cache, and
    // the useEffect([theme]) driver re-applies 'light'. We only assert the
    // final state.
    await page.getByRole('button', { name: /^dark$/i }).click()
    await expect(page.locator('html')).not.toHaveClass(/dark/, { timeout: 3000 })
  })
})
