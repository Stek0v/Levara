// auth-guard.spec.ts — T1 coverage for the dashboard auth guard.
//
// Verifies that:
//   1. Unauthenticated users hitting a protected route get redirected to /login
//      with ?next=<original-path> so login can return them.
//   2. /login itself never redirects (no loop on missing JWT).
//   3. After a 401 from any API call, the page redirects to /login.
//
// The API mock uses `page.route` so these tests don't need a live backend —
// they assert the client-side guard, not end-to-end login.

import { test, expect } from '@playwright/test'

test.describe('Auth guard (T1)', () => {
  test('unauthenticated /datasets redirects to /login?next=/datasets', async ({ page }) => {
    // Mock /auth/me to always 401 so the guard fires.
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: 'unauth' }) }),
    )

    await page.goto('/datasets')

    await expect(page).toHaveURL(/\/login\?next=%2Fdatasets$/, { timeout: 5000 })
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
  })

  test('/login never triggers a redirect loop', async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: 'unauth' }) }),
    )

    await page.goto('/login')
    // Should stay on /login, not ping-pong to /login?next=/login.
    await expect(page).toHaveURL(/\/login$/)
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
  })

  test('API 401 on an arbitrary endpoint redirects to /login', async ({ page }) => {
    // Allow /auth/me so the guard lets the page render.
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ id: 'u1', email: 'u@test.local', username: 'u' }),
      }),
    )
    // Make /datasets itself 401 to simulate mid-session expiry.
    await page.route('**/api/v1/datasets*', (route) =>
      route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: 'expired' }) }),
    )

    await page.goto('/datasets')
    // Expect redirect triggered by api.ts handleResponse.
    await expect(page).toHaveURL(/\/login\?next=/, { timeout: 7000 })
  })
})
