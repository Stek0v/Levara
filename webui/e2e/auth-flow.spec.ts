// auth-flow.spec.ts — T20 coverage for full login/register/logout.
//
// These are fully client-side assertions: we mock /auth/me and friends via
// page.route so the tests don't require a live backend. Real round-trip
// against the Go server is exercised by the existing upload-flow.spec.ts
// when a dev server is running; here we focus on the redirect/state
// transitions that the UI owns.

import { test, expect } from '@playwright/test'

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
    await page.getByLabel(/email/i).fill('wrong@test.local')
    await page.getByLabel(/password/i).fill('wrong-pw')
    await page.getByRole('button', { name: /sign in/i }).click()

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
    await page.getByLabel(/email/i).fill('u@test.local')
    await page.getByLabel(/password/i).fill('pw')
    await page.getByRole('button', { name: /sign in/i }).click()

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
    await page.getByText(/register/i).click()
    await page.getByLabel(/email/i).fill('new@test.local')
    await page.getByLabel(/password/i).fill('pw')
    await page.getByRole('button', { name: /create account/i }).click()

    // Default post-register redirect = "/".
    await expect(page).toHaveURL(/\/$/, { timeout: 5000 })
  })
})
