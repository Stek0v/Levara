import { test, expect, type Page } from '@playwright/test'

async function mockAPI(page: Page) {
  const state = { userId: 'alice', searches: [] as { userId: string; session_id: string }[] }
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    let body: unknown = []
    if (path === '/api/v1/auth/login') {
      state.userId = route.request().postDataJSON().email.split('@')[0]
      body = { token: `test-token-${state.userId}` }
    } else if (path === '/api/v1/auth/me') {
      body = { id: state.userId, email: `${state.userId}@test.local`, username: state.userId }
    } else if (path === '/api/v1/settings') {
      body = { locale: 'en', theme: 'light' }
    } else if (path === '/api/v1/search/text') {
      state.searches.push({ ...route.request().postDataJSON(), userId: state.userId })
      body = { answer: `Private answer for ${state.userId}`, chunks: [] }
    }
    await route.fulfill({ status: 200, json: body })
  })
  await page.route('**/health**', (route) => route.fulfill({ status: 200, json: { status: 'ok' } }))
  return state
}

async function login(page: Page, user: string) {
  await page.getByLabel(/email|почта/i).fill(`${user}@test.local`)
  await page.getByLabel(/password|пароль/i).fill('isolated-test-password')
  await page.locator('form').getByRole('button').click()
}

for (const next of ['/\\attacker.invalid', '/\t/attacker.invalid', '//attacker.invalid', '/chat/../login', '/.//attacker.invalid/path', '/safe/..//attacker.invalid/path']) {
  test(`login rejects unsafe redirect ${JSON.stringify(next)}`, async ({ page }) => {
    await mockAPI(page)
    // Fulfil the attack target locally: the regression must never contact it.
    await page.route('**://attacker.invalid/**', (route) => route.fulfill({ status: 200, body: 'external redirect' }))
    await page.goto(`/login?next=${encodeURIComponent(next)}`)
    await login(page, 'alice')
    await expect(page).toHaveURL('/', { timeout: 5000 })
  })
}

test('login preserves a safe local query and fragment', async ({ page }) => {
  await mockAPI(page)
  await page.goto(`/login?next=${encodeURIComponent('/chat?source=a%2Fb#answer')}`)
  await login(page, 'alice')
  await expect(page).toHaveURL('/chat?source=a%2Fb#answer')
})

test('switching accounts isolates persisted chat and session IDs', async ({ page }) => {
  const state = await mockAPI(page)
  await page.goto('/login?next=%2Fchat')
  await login(page, 'alice')
  await page.getByPlaceholder('Ask a question... (Enter to send, Shift+Enter for newline)').fill('Alice confidential question')
  await page.getByPlaceholder('Ask a question... (Enter to send, Shift+Enter for newline)').press('Enter')
  await expect(page.getByText('Private answer for alice', { exact: true })).toBeVisible()
  await page.reload()
  await expect(page.getByText('Alice confidential question', { exact: true })).toBeVisible()

  await page.goto('/login?next=%2Fchat')
  await login(page, 'bob')
  await expect(page.getByRole('heading', { name: 'Chat', exact: true })).toBeVisible()
  await expect.soft(page.getByText('Alice confidential question', { exact: true })).toBeHidden()
  await expect.soft(page.getByText('Private answer for alice', { exact: true })).toBeHidden()
  await page.getByPlaceholder('Ask a question... (Enter to send, Shift+Enter for newline)').fill('Bob question')
  await page.getByPlaceholder('Ask a question... (Enter to send, Shift+Enter for newline)').press('Enter')
  await expect(page.getByText('Private answer for bob', { exact: true })).toBeVisible()
  expect(state.searches).toHaveLength(2)
  expect(state.searches[1].session_id).not.toBe(state.searches[0].session_id)

  await page.goto('/login?next=%2Fchat')
  await login(page, 'alice')
  await expect(page.getByText('Alice confidential question', { exact: true })).toBeVisible()
  await expect(page.getByText('Bob question', { exact: true })).toBeHidden()
})

test('chat never adopts an ownerless legacy transcript', async ({ page }) => {
  await mockAPI(page)
  await page.addInitScript(() => {
    localStorage.setItem('levara.chat.messages', JSON.stringify([{ role: 'user', content: 'Legacy confidential question', timestamp: 1 }]))
    localStorage.setItem('levara.chat.sessionId', 'legacy-session')
  })
  await page.goto('/chat')
  await expect(page.getByRole('heading', { name: 'Chat', exact: true })).toBeVisible()
  await expect(page.getByText('Legacy confidential question', { exact: true })).toBeHidden()
  await expect(page.getByText('Ask a question', { exact: true })).toBeVisible()
})

test('graph path highlighting preserves existing SVG nodes and zoom', async ({ page }) => {
  await mockAPI(page)
  await page.route('**/api/v1/datasets?*', (route) => route.fulfill({ json: [{ id: 'graph-dataset', name: 'Graph fixture' }] }))
  await page.route('**/api/v1/datasets/graph-dataset/graph', (route) => route.fulfill({ json: {
    nodes: [
      { id: 'alice', name: 'Alice', type: 'Person' },
      { id: 'bob', name: 'Bob', type: 'Person' },
      { id: 'org', name: 'Organization', type: 'Organization' },
    ],
    edges: [
      { source: 'alice', target: 'bob', label: 'knows' },
      { source: 'bob', target: 'org', label: 'works_at' },
    ],
  } }))
  await page.route('**/api/v1/graph/path?*', (route) => route.fulfill({ json: {
    edges: [{ source_id: 'alice', target_id: 'bob', type: 'knows' }], as_of: 0,
  } }))
  await page.goto('/graph')
  await page.getByLabel('Dataset', { exact: true }).selectOption('graph-dataset')
  const svg = page.locator('svg.w-full.h-full')
  const circles = svg.locator('circle')
  await expect(circles).toHaveCount(3)
  const originalNode = await circles.first().elementHandle()
  const graphGroup = svg.locator(':scope > g')
  await graphGroup.evaluate((element) => element.setAttribute('transform', 'translate(10,20) scale(1.2)'))
  await page.getByLabel('Path from node id').fill('alice')
  await page.getByLabel('Path to node id').fill('bob')
  await page.getByRole('button', { name: 'Path', exact: true }).click()
  await expect(svg.locator('line[stroke="#0891b2"]')).toHaveCount(1)
  await expect(svg.locator('line[stroke-opacity="0.25"]')).toHaveCount(1)
  expect(await originalNode!.evaluate((element) => element.isConnected)).toBe(true)
  await expect(graphGroup).toHaveAttribute('transform', 'translate(10,20) scale(1.2)')

  // A topology change still rebuilds and applies the current path styling.
  await page.getByRole('button', { name: 'Person', exact: true }).click()
  await expect(circles).toHaveCount(2)
  await expect(svg.locator('line[stroke="#0891b2"]')).toHaveCount(1)
  await page.getByPlaceholder('Search nodes…').fill('no matching node')
  await expect(circles).toHaveCount(0)
})
