// Route-mocked browser auth transitions. Real backend auth is tested in Go.

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

test.describe('Auth flow (T20)', () => {
  test('wrong password surfaces error in form', async ({ page }) => {
    await page.route('**/api/v1/auth/login', (route) =>
      route.fulfill({
        status: 401,
        contentType: 'application/json',
        body: JSON.stringify({ error: 'invalid credentials' }),
      }),
    )

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
