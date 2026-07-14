import { test, expect } from '@playwright/test'

test.describe('Memory scaffold proposals', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ id: 'admin', email: 'admin@test.local', username: 'admin' }),
      }),
    )
  })

  test('lists proposals, opens detail, approves', async ({ page }) => {
    let proposal = {
      id: 'p1',
      target: 'project_agents',
      collection: 'levara',
      summary: 'Add consult-before-write guidance',
      current_problem: 'Blind save pattern in recent trajectories.',
      proposed_change: 'Add explicit recall_memory before save_memory rule.',
      risk: 'Low risk; wording-only scaffold change.',
      status: 'open',
      source_run_id: 'run-1',
      source_finding_ids: ['f1'],
      created_at: '2026-07-14T00:00:00Z',
      updated_at: '2026-07-14T00:00:00Z',
    }

    await page.route('**/api/v1/memory-scaffold/proposals?*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ proposals: [proposal] }),
      }),
    )
    await page.route('**/api/v1/memory-scaffold/proposals/p1/decision', async (route) => {
      const body = route.request().postDataJSON() as { status: string; note?: string }
      proposal = { ...proposal, status: body.status, decision_note: body.note || '' }
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(proposal) })
    })
    await page.route('**/api/v1/memory-scaffold/proposals/p1', (route) => {
      if (route.request().method() !== 'GET') return route.fallback()
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(proposal) })
    })

    await page.goto('/memory-scaffold?status=open')
    await expect(page.getByRole('heading', { name: 'Memory Scaffold Proposals' })).toBeVisible()
    await page.getByText('Add consult-before-write guidance').click()
    await expect(page.getByText('Blind save pattern')).toBeVisible()
    await page.getByPlaceholder('decision note (optional)').fill('accepted')
    await page.getByRole('button', { name: 'Approve', exact: true }).click()

    await expect(page.getByText('approved')).toBeVisible({ timeout: 5000 })
  })

  test('shows permission message when decision is forbidden', async ({ page }) => {
    const proposal = {
      id: 'p2',
      target: 'memory_policy',
      collection: 'levara',
      summary: 'Clarify hall vocabulary',
      current_problem: 'Wrong hall saves.',
      proposed_change: 'Document allowed halls.',
      risk: 'Medium risk.',
      status: 'open',
      source_run_id: 'run-2',
      source_finding_ids: ['f2'],
      created_at: '2026-07-14T00:00:00Z',
      updated_at: '2026-07-14T00:00:00Z',
    }
    await page.route('**/api/v1/memory-scaffold/proposals?*', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ proposals: [proposal] }) }),
    )
    await page.route('**/api/v1/memory-scaffold/proposals/p2/decision', (route) =>
      route.fulfill({ status: 403, contentType: 'application/json', body: JSON.stringify({ detail: 'superuser role required' }) }),
    )
    await page.route('**/api/v1/memory-scaffold/proposals/p2', (route) => {
      if (route.request().method() !== 'GET') return route.fallback()
      return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(proposal) })
    })

    await page.goto('/memory-scaffold')
    await page.getByText('Clarify hall vocabulary').click()
    await page.getByRole('button', { name: 'Reject', exact: true }).click()

    await expect(page.getByText('Decision requires admin permissions.')).toBeVisible()
  })
})
