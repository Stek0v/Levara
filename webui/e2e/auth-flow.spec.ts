// auth-flow.spec.ts — T20 coverage for full login/register/logout.
//
// These are fully client-side assertions: we mock /auth/me and friends via
// page.route so the tests don't require a live backend. Real round-trip
// against the Go server is exercised by the existing upload-flow.spec.ts
// when a dev server is running; here we focus on the redirect/state
// transitions that the UI owns.

import { test, expect } from '@playwright/test'

// i18n reads settings on every page — stub it so tests don't hit the
// proxy and locale is deterministic (en).
async function stubSettings(page: import('@playwright/test').Page) {
  await page.route('**/api/v1/settings', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ theme: 'light', locale: 'en' }) }),
  )
}

test.describe('Auth flow (T20)', () => {
  test('wrong password surfaces error in form', async ({ page }) => {
    await page.route('**/api/v1/auth/login', (route) =>
      route.fulfill({
        status: 401,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'invalid credentials' }),
      }),
    )

    await stubSettings(page)
    await page.goto('/login')
    await page.getByLabel(/email|почта/i).fill('wrong@test.local')
    await page.getByLabel(/password|пароль/i).fill('wrong-pw')
    await page.getByRole('button', { name: /sign in|войти/i }).click()

    // Error should bubble up via the Input error prop — exact message is
    // whatever the backend returned.
    await expect(page.getByText(/invalid credentials/i)).toBeVisible({ timeout: 3000 })
    // Stay on /login — no redirect on failed login.
    await expect(page).toHaveURL(/\/login$/)
  })

  test('successful login honours ?next= and navigates', async ({ page }) => {
    await page.route('**/api/v1/auth/login', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ token: 'fake-jwt' }),
      }),
    )
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ id: 'u1', email: 'u@test.local', username: 'u' }),
      }),
    )
    // Stub datasets so the /datasets page can mount without errors.
    await page.route('**/api/v1/datasets*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([]),
      }),
    )

    await page.goto('/login?next=%2Fdatasets')
    await page.getByLabel(/email|почта/i).fill('u@test.local')
    await page.getByLabel(/password|пароль/i).fill('pw')
    await page.getByRole('button', { name: /sign in|войти/i }).click()

    await expect(page).toHaveURL(/\/datasets$/, { timeout: 5000 })
  })

  test('register → dashboard', async ({ page }) => {
    await page.route('**/api/v1/auth/register', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ token: 'fake-jwt' }),
      }),
    )
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ id: 'u2', email: 'new@test.local', username: 'new' }),
      }),
    )

    await stubSettings(page)
    await page.goto('/login')
    await page.getByText(/register|sign up|регистрация/i).click()
    await page.getByLabel(/email|почта/i).fill('new@test.local')
    await page.getByLabel(/password|пароль/i).fill('pw')
    // The submit button is inside the form; the mode-toggle is not.
    // Bilingual: EN 'Create account' / RU 'Регистрация' (exact — the
    // toggle link also says «Регистрация» but is not a button in form).
    await page.locator('form').getByRole('button').click()

    // Default post-register redirect = "/".
    await expect(page).toHaveURL(/\/$/, { timeout: 15000 })
  })
})
