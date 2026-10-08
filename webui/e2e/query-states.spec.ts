import { test, expect, type Page } from '@playwright/test'

async function session(page: Page) {
  await page.route('**/api/v1/**', route => route.fulfill({ json: {} }))
  await page.route('**/api/v1/auth/me', route => route.fulfill({ json: { id: 'query-user', email: 'query@test.local' } }))
  await page.route('**/api/v1/settings', route => route.fulfill({ json: { theme: 'light', locale: 'en' } }))
  await page.route('**/health/details', route => route.fulfill({ json: { services: { neo4j: { status: 'disconnected' } } } }))
}

const memory = { id: 'memory-one', key: 'private-key', value: 'private memory text', type: 'fact', room: 'memory', hall: 'fact' }

for (const status of [403, 503, 404]) {
  test(`memories initial ${status} is unavailable rather than empty`, async ({ page }) => {
    await session(page)
    await page.route('**/api/v1/memories*', route => route.fulfill({ status, json: { error: `memory endpoint ${status}` } }))
    await page.goto('/memories')
    await expect(page.getByRole('alert').filter({ hasText: `Memories unavailable: memory endpoint ${status}` })).toBeVisible({ timeout: 15000 })
    await expect(page.getByText('No memories yet', { exact: true })).toHaveCount(0)
  })
}

test('memories accepted empty is visible and retry can recover an unavailable query', async ({ page }) => {
  await session(page)
  let unavailable = true
  await page.route('**/api/v1/memories*', route => route.fulfill(unavailable ? { status: 403, json: { error: 'temporary denial' } } : { json: [] }))
  await page.goto('/memories')
  await expect(page.getByRole('alert').filter({ hasText: 'temporary denial' })).toBeVisible({ timeout: 15000 })
  unavailable = false
  await page.getByRole('button', { name: 'Refresh memories', exact: true }).click()
  await expect(page.getByText('No memories yet', { exact: true })).toBeVisible()
  await expect(page.getByRole('alert').filter({ hasText: 'temporary denial' })).toHaveCount(0)
})

for (const status of [403, 503]) {
  test(`memory refresh ${status} hides private rows or labels them stale`, async ({ page }) => {
    await session(page)
    let failed = false
    await page.route('**/api/v1/memories*', route => route.fulfill(failed ? { status, json: { error: 'memory refresh failed' } } : { json: [memory] }))
    await page.goto('/memories')
    await expect(page.getByText(memory.value, { exact: true })).toBeVisible()
    failed = true
    await page.getByRole('button', { name: 'Refresh memories', exact: true }).click()
    await expect(page.getByRole('alert').filter({ hasText: 'Memories unavailable: memory refresh failed' })).toBeVisible({ timeout: 15000 })
    if (status === 403) {
      await expect(page.getByText(memory.value, { exact: true })).toHaveCount(0)
      await expect(page.getByText(memory.key, { exact: true })).toHaveCount(0)
    } else {
      await expect(page.getByText(memory.value, { exact: true })).toBeVisible()
      await expect(page.getByRole('alert').filter({ hasText: 'showing stale data' })).toBeVisible()
    }
    await expect(page.getByText('No memories yet', { exact: true })).toHaveCount(0)
  })
}

for (const status of [403, 503]) {
  test(`search failure ${status} is visible and does not masquerade as an empty search`, async ({ page }) => {
    await session(page)
    await page.route('**/api/v1/collections', route => route.fulfill({ json: [] }))
    await page.route('**/api/v1/search/text', route => route.fulfill({ status, json: { error: `search backend ${status}` } }))
    await page.goto('/search')
    await page.getByPlaceholder('Enter your query...').fill('query')
    await page.getByRole('button', { name: 'Search', exact: true }).click()
    await expect(page.getByRole('alert').filter({ hasText: `Search failed: search backend ${status}` })).toBeVisible()
    await expect(page.getByText('No results found', { exact: true })).toHaveCount(0)
  })
}

test('search profile404 collection failure is visible, retry restores choices, accepted empty remains empty', async ({ page }) => {
  await session(page)
  let unavailable = true
  await page.route('**/api/v1/collections', route => route.fulfill(unavailable ? { status: 404, json: { error: 'collections not exposed by profile' } } : { json: [{ name: 'levara' }] }))
  await page.route('**/api/v1/search/text', route => route.fulfill({ json: [] }))
  await page.goto('/search')
  await expect(page.getByRole('alert').filter({ hasText: 'Collections unavailable: collections not exposed by profile' })).toBeVisible({ timeout: 15000 })
  await expect(page.getByLabel('Collection', { exact: true })).toBeDisabled()
  unavailable = false
  await page.getByRole('button', { name: 'Retry collections', exact: true }).click()
  await expect(page.getByLabel('Collection', { exact: true })).toBeEnabled()
  await expect(page.getByLabel('Collection', { exact: true }).locator('option')).toContainText(['All collections', 'levara'])
  await page.getByPlaceholder('Enter your query...').fill('no match')
  await page.getByRole('button', { name: 'Search', exact: true }).click()
  await expect(page.getByText('No results found', { exact: true })).toBeVisible()
})


test('search accepts an explicit project collection when operator inventory is forbidden', async ({ page }) => {
  await session(page)
  let selected = ''
  await page.route('**/api/v1/collections', route => route.fulfill({ status: 403, json: { error: 'operator inventory requires administrator' } }))
  await page.route('**/api/v1/search/text', route => {
    selected = route.request().postDataJSON().collection
    return route.fulfill({ json: [{ id: 'own-result', score: 1, metadata: { text: 'Authorized project result' } }] })
  })
  await page.goto('/search')
  await expect(page.getByRole('alert').filter({ hasText: 'Collections unavailable:' })).toBeVisible({ timeout: 15000 })
  await page.getByLabel('Collection', { exact: true }).fill('known-project')
  await page.getByPlaceholder('Enter your query...').fill('project evidence')
  await page.getByRole('button', { name: 'Search', exact: true }).click()
  await expect(page.getByText('Authorized project result', { exact: true })).toBeVisible()
  expect(selected).toBe('known-project')
})

const graph = {
  nodes: [
    { id: 'node-a', label: 'Protected node', type: 'Person', properties: { secret: 'private graph property' } },
    { id: 'node-b', label: 'Other node', type: 'Person', properties: {} },
  ],
  edges: [{ source: 'node-a', target: 'node-b', label: 'related_to' }],
}

async function graphSession(page: Page) {
  await session(page)
  await page.route('**/api/v1/datasets?**', route => route.fulfill({ json: { data: [{ id: 'ds-one', name: 'Domain project' }] } }))
  await page.route('**/api/v1/vsa/status', route => route.fulfill({ json: { available: false } }))
}

test('graph dataset error is visible without claiming an empty graph or absent datasets', async ({ page }) => {
  await graphSession(page)
  await page.route('**/api/v1/datasets?**', route => route.fulfill({ status: 503, json: { error: 'dataset catalog unavailable' } }))
  await page.goto('/graph')
  await expect(page.getByRole('alert').filter({ hasText: 'Datasets unavailable: dataset catalog unavailable' })).toBeVisible({ timeout: 15000 })
  await expect(page.getByText('No datasets available.', { exact: true })).toHaveCount(0)
  await expect(page.getByText('Graph is empty', { exact: true })).toHaveCount(0)
})

test('graph and observation profile404 failures are unavailable rather than empty or optional-off', async ({ page }) => {
  await graphSession(page)
  await page.route('**/api/v1/datasets/ds-one/graph', route => route.fulfill({ status: 404, json: { error: 'graph not exposed by profile' } }))
  await page.route('**/health/details', route => route.fulfill({ status: 404, json: { error: 'health details not exposed by profile' } }))
  await page.route('**/api/v1/vsa/status', route => route.fulfill({ status: 404, json: { error: 'VSA not exposed by profile' } }))
  await page.goto('/graph')
  await page.getByLabel('Dataset', { exact: true }).selectOption('ds-one')
  await expect(page.getByRole('alert').filter({ hasText: 'Graph unavailable: graph not exposed by profile' })).toBeVisible({ timeout: 15000 })
  await expect(page.getByText('Graph is empty', { exact: true })).toHaveCount(0)
  await expect(page.getByText('Neo4j unavailable', { exact: true })).toBeVisible()
  await expect(page.getByText('VSA unavailable', { exact: true })).toBeVisible()
  await expect(page.getByText('VSA off', { exact: true })).toHaveCount(0)
})

for (const status of [403, 503]) {
  test(`graph loaded refresh ${status} hides selected properties and paths or retains labeled stale data`, async ({ page }) => {
    await graphSession(page)
    let failed = false
    let refreshRequests = 0
    await page.route('**/api/v1/datasets/ds-one/graph', route => {
      if (failed) {
        refreshRequests++
        if (status !== 403 || refreshRequests === 1) return route.fulfill({ status, json: { error: 'graph refresh failed' } })
      }
      return route.fulfill({ json: graph })
    })
    await page.route('**/api/v1/graph/path?**', route => route.fulfill({ json: { as_of: 0, edges: [{ source_id: 'node-a', target_id: 'node-b', type: 'related_to' }] } }))
    await page.goto('/graph')
    await page.getByLabel('Dataset', { exact: true }).selectOption('ds-one')
    await page.getByRole('button', { name: /Protected node/ }).click()
    await expect(page.getByText('private graph property', { exact: true })).toBeVisible()
    await page.getByLabel('Path from node id').fill('node-a')
    await page.getByLabel('Path to node id').fill('node-b')
    await page.getByRole('button', { name: 'Path', exact: true }).click()
    await expect(page.getByText('1 path edges', { exact: true })).toBeVisible()
    failed = true
    await page.getByRole('button', { name: 'Refresh graph', exact: true }).click()
    await expect(page.getByRole('alert').filter({ hasText: 'Graph unavailable: graph refresh failed' })).toBeVisible({ timeout: 15000 })
    if (status === 403) {
      expect(refreshRequests).toBe(1)
      await expect(page.getByText('private graph property', { exact: true })).toHaveCount(0)
      await expect(page.getByRole('button', { name: /Protected node/ })).toHaveCount(0)
      await expect(page.getByText('1 path edges', { exact: true })).toHaveCount(0)
      await expect(page.getByLabel('Path from node id')).toHaveValue('')
      failed = false
      await page.getByRole('button', { name: 'Refresh graph', exact: true }).click()
      await expect(page.getByRole('button', { name: /Protected node/ })).toBeVisible()
      await expect(page.getByText('private graph property', { exact: true })).toHaveCount(0)
      await expect(page.getByText('1 path edges', { exact: true })).toHaveCount(0)
    } else {
      await expect(page.getByText('private graph property', { exact: true })).toBeVisible()
      await expect(page.getByText('1 path edges', { exact: true })).toBeVisible()
      await expect(page.getByRole('alert').filter({ hasText: 'showing stale data' })).toBeVisible()
    }
    await expect(page.getByText('Graph is empty', { exact: true })).toHaveCount(0)
  })
}

test('graph accepted empty is visible', async ({ page }) => {
  await graphSession(page)
  await page.route('**/api/v1/datasets/ds-one/graph', route => route.fulfill({ json: { nodes: [], edges: [] } }))
  await page.goto('/graph')
  await page.getByLabel('Dataset', { exact: true }).selectOption('ds-one')
  await expect(page.getByText('Graph is empty', { exact: true })).toBeVisible()
})

test('search serializes repeated Enter and clears results after subsequent denial', async ({ page }) => {
  await session(page)
  await page.route('**/api/v1/collections', route => route.fulfill({ json: [] }))
  let calls = 0
  let started!: () => void
  let release!: () => void
  const requestStarted = new Promise<void>(resolve => { started = resolve })
  const responseReady = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/v1/search/text', async route => {
    calls++
    if (calls === 1) {
      started()
      await responseReady
      await route.fulfill({ json: [{ id: 'held', metadata: { text: 'protected held result' }, score: 1 }] })
    } else {
      await route.fulfill({ status: 403, json: { error: 'search access revoked' } })
    }
  })
  await page.goto('/search')
  const input = page.getByPlaceholder('Enter your query...')
  await input.fill('first')
  await input.press('Enter')
  await requestStarted
  await input.fill('second')
  await input.press('Enter')
  expect(calls).toBe(1)
  release()
  await expect(page.getByText('protected held result', { exact: true })).toBeVisible()
  await input.press('Enter')
  await expect(page.getByRole('alert').filter({ hasText: 'search access revoked' })).toBeVisible()
  expect(calls).toBe(2)
  await expect(page.getByText('protected held result', { exact: true })).toHaveCount(0)
  await expect(page.getByText('No results found', { exact: true })).toHaveCount(0)
})
