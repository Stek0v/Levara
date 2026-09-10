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

test.beforeEach(async ({ page }) => {
  await page.route('**/api/**', route => {
    const path = new URL(route.request().url()).pathname
    return route.fulfill({ json: path.endsWith('/auth/methods')
      ? { password: true, registration: true, directory: false, oidc: false, saml: false }
      : path.endsWith('/settings') ? { theme: 'light', locale: 'en' } : [] })
  })
  await page.route('**/health**', route => route.fulfill({ json: { status: 'ok' } }))
})

test.describe('Auth guard (T1)', () => {
  test('unauthenticated /datasets redirects to /login?next=/datasets', async ({ page }) => {
    // Mock /auth/me to always 401 so the guard fires.
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: 'unauth' }) }),
    )
    await page.route('**/api/v1/settings', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ theme: 'light', locale: 'en' }) }),
    )

    await page.goto('/datasets')

    await expect(page).toHaveURL(/\/login\?next=%2Fdatasets$/, { timeout: 5000 })
    await expect(page.getByRole('heading', { name: /sign in|вход/i })).toBeVisible()
  })

  test('/login never triggers a redirect loop', async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: 'unauth' }) }),
    )
    await page.route('**/api/v1/settings', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ theme: 'light', locale: 'en' }) }),
    )

    await page.goto('/login')
    // Should stay on /login, not ping-pong to /login?next=/login.
    await expect(page).toHaveURL(/\/login$/)
    await expect(page.getByRole('heading', { name: /sign in|вход/i })).toBeVisible()
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

test('offline session probe keeps protected UI hidden and gives a retryable login message', async ({ page }) => {
  await page.route('**/api/v1/auth/me', route => route.abort('failed'))
  await page.goto('/datasets?view=shared')
  await expect(page).toHaveURL(/\/login\?next=%2Fdatasets%3Fview%3Dshared&error=unavailable$/)
  await expect(page.getByText('Could not verify the server connection. Please try again.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Log out' })).toHaveCount(0)
})

test('malformed successful session response cannot reveal protected UI', async ({ page }) => {
  await page.route('**/api/v1/auth/me', route => route.fulfill({ json: {} }))
  await page.goto('/datasets')
  await expect(page).toHaveURL(/\/login\?next=%2Fdatasets&error=unavailable$/)
  await expect(page.getByRole('button', { name: 'Log out' })).toHaveCount(0)
})

test('retry after a network outage rechecks the cookie before returning to the requested page', async ({ page }) => {
  let offline = true
  await page.route('**/api/v1/auth/me', route => offline
    ? route.abort('failed') : route.fulfill({ json: { id: 'restored', email: 'restored@example.test' } }))
  await page.goto('/datasets?view=shared')
  await expect(page).toHaveURL(/\/login\?next=%2Fdatasets%3Fview%3Dshared&error=unavailable$/)
  offline = false
  await page.getByRole('button', { name: 'Retry' }).click()
  await expect(page).toHaveURL(/\/datasets\?view=shared$/)
  await expect(page.getByRole('button', { name: 'Log out' })).toBeVisible()
})
