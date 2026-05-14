// search-rerank-badge.spec.ts — Phase 2 contract for the /search page.
//
// Two things this spec locks:
//
//   1. The WebUI does NOT send a `rerank` field on /search/text. Phase 2
//      flipped rerank to default-on server-side; the UI relies on that
//      and surfaces per-result `reranked` from the response instead of
//      its own toggle. If a future PR adds a UI toggle that sets
//      `rerank: true|false`, this assertion forces an explicit doc
//      update (see docs/phase2-rerank-default-design.md migration plan
//      PR C).
//
//   2. The `reranked` Badge appears on result rows where the response
//      sets `reranked: true` and is absent on the rest. The A.2 fix in
//      api_search.go made `reranked` per-result rather than per-call; a
//      regression to per-call would either show the badge on every row
//      or on none.

import { test, expect } from '@playwright/test'

test.describe('Search /search rerank badge (Phase 2)', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ id: 'u1', email: 'u@test.local', username: 'u' }),
      }),
    )
    await page.route('**/api/v1/collections', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([{ name: 'entities' }]),
      }),
    )
  })

  test('per-result reranked badge + no rerank field in request', async ({ page }) => {
    let lastBody: unknown = null
    await page.route('**/api/v1/search/text', async (route) => {
      const req = route.request()
      lastBody = req.postDataJSON()
      await route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify([
          { id: 'doc-a', collection: 'entities', score: 0.9, reranked: true, metadata: { text: 'alpha — reranked' } },
          { id: 'doc-b', collection: 'entities', score: 0.5, reranked: false, metadata: { text: 'beta — vector only' } },
          { id: 'doc-c', collection: 'entities', score: 0.4, metadata: { text: 'gamma — reranked absent' } },
        ]),
      })
    })

    await page.goto('/search')
    await page.getByPlaceholder('Enter your query...').fill('alpha')
    await page.getByRole('button', { name: /Search/i }).click()

    // Wait for results to render.
    await expect(page.getByText('doc-a')).toBeVisible()
    await expect(page.getByText('doc-b')).toBeVisible()
    await expect(page.getByText('doc-c')).toBeVisible()

    // Phase 2: WebUI must not send a `rerank` field — server applies its
    // own default (on when sidecar configured, off otherwise).
    expect(lastBody, 'WebUI sent /search/text but never captured').not.toBeNull()
    const body = lastBody as Record<string, unknown>
    expect(body).not.toHaveProperty('rerank')

    // The reranked Badge text "reranked" must appear exactly once — only
    // doc-a in the fixture sets reranked: true. doc-b explicit false and
    // doc-c omitted both must render without the badge.
    const reranked = page.getByText('reranked', { exact: true })
    await expect(reranked).toHaveCount(1)

    // Scope-check: the one badge sits in the doc-a row.
    const rerankedRow = page.locator('div', { has: page.getByText('doc-a') }).first()
    await expect(rerankedRow.getByText('reranked', { exact: true })).toBeVisible()
  })
})
