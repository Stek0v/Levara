// error-scenarios.spec.ts — T20 coverage for the error-handling UI.
//
// Verifies that backend 5xx surfaces through ErrorBoundary and that
// global 401 still redirects even from arbitrary API calls (T18 + T1).

import { test, expect } from '@playwright/test'

test.describe('Error scenarios (T20)', () => {
  test('backend 500 on a protected page surfaces via React Query error state', async ({ page }) => {
    // Let /auth/me succeed so the guard allows render; then fail the data call.
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ id: 'u1', email: 'u@test.local', username: 'u' }),
      }),
    )
    await page.route('**/api/v1/datasets*', (route) =>
      route.fulfill({
        status: 500,
        contentType: 'application/json',
        body: JSON.stringify({ error: { message: 'database unreachable' } }),
      }),
    )

    await page.goto('/datasets')
    // Error is raised by React Query into either the ErrorBoundary (if a
    // useQuery is configured to throw) or surfaces inline. Exact placement
    // varies per page; we just assert SOMETHING error-shaped shows up.
    const somethingError = page
      .getByText(/error|something went wrong|database/i)
      .first()
    await expect(somethingError).toBeVisible({ timeout: 5000 })
  })

  test('401 on /datasets redirects to /login', async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ id: 'u1', email: 'u@test.local', username: 'u' }),
      }),
    )
    // Simulate mid-session token expiry — any protected endpoint returns 401.
    await page.route('**/api/v1/datasets*', (route) =>
      route.fulfill({
        status: 401,
        contentType: 'application/json',
        body: JSON.stringify({ error: { message: 'unauthorized' } }),
      }),
    )

    await page.goto('/datasets')
    await expect(page).toHaveURL(/\/login/, { timeout: 5000 })
  })
})
