import { test, expect, type Page } from '@playwright/test'

const chat = { chat_id: 'canonical /?&', platform: 'codex', session_id: 'source /?&', project_id: '', title: 'Imported A', message_count: 1 }
const second = { ...chat, chat_id: 'second', session_id: 'second', title: 'Imported B' }
const detail = (item = chat, project = item.project_id) => ({ ...item, project_id: project, messages: [{ external_id: 'm1', ordinal: 1, role: 'user', content: item.chat_id === second.chat_id ? 'Content B' : 'Private content A', created_at: '' }] })
const path = (item = chat) => '/api/v1/chats/import/sessions/' + encodeURIComponent(item.platform) + '/' + encodeURIComponent(item.session_id) + '?chat_id=' + encodeURIComponent(item.chat_id)

async function setup(page: Page, sessions: unknown = [chat]) {
  await page.route('**/api/v1/**', route => route.fulfill({ json: [] }))
  await page.route('**/api/v1/auth/me', route => route.fulfill({ json: { id: 'user-a', email: 'a@test.local' } }))
  await page.route('**/api/v1/settings', route => route.fulfill({ json: { theme: 'light' } }))
  await page.route('**/api/v1/chats/import/sessions', route => route.fulfill({ json: { sessions } }))
  await page.route(url => url.pathname.startsWith('/api/v1/chats/import/sessions/'), route => {
    const item = new URL(route.request().url()).searchParams.get('chat_id') === second.chat_id ? second : chat
    return route.fulfill({ json: detail(item) })
  })
}

test('reads encoded canonical identity; grants and revokes only acknowledged project state', async ({ page }) => {
  await setup(page)
  let project = ''
  let posts = 0
  let deletes = 0
  await page.route(url => url.pathname.startsWith('/api/v1/chats/import/sessions/'), async route => {
    const req = route.request()
    const url = new URL(req.url())
    expect(url.searchParams.get('chat_id')).toBe(chat.chat_id)
    if (req.method() === 'POST') {
      expect(req.postDataJSON()).toEqual({ project_id: 'project /x' })
      project = 'project /x'; posts++
    }
    if (req.method() === 'DELETE') { expect(req.postData()).toBeNull(); project = ''; deletes++ }
    await route.fulfill({ json: req.method() === 'GET' ? detail(chat, project) : { ...chat, project_id: project } })
  })
  await page.goto('/chat')
  await page.getByRole('button', { name: 'Imported A', exact: true }).click()
  await expect(page.getByText('Private content A')).toBeVisible()
  const response = page.waitForRequest(req => new URL(req.url()).pathname.startsWith('/api/v1/chats/import/sessions/') && req.method() === 'GET')
  await page.getByRole('button', { name: 'Refresh imported chats' }).click()
  expect(new URL((await response).url()).pathname + new URL((await response).url()).search).toBe(path())
  await expect(page.getByText('Private content A')).toBeVisible()
  await page.getByLabel('Imported chat project ID').fill('project /x')
  await page.getByRole('button', { name: 'Share with project', exact: true }).click()
  await expect(page.getByText('Project: project /x', { exact: true })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Manage project audience' })).toHaveAttribute('href', '/datasets/project%20%2Fx')
  await page.getByRole('button', { name: 'Revoke project sharing' }).click()
  await expect(page.getByText('Project: Private', { exact: true })).toBeVisible()
  expect(posts).toBe(1); expect(deletes).toBe(1)
})

for (const sessions of [null, []]) {
  test('empty/null session list ' + JSON.stringify(sessions), async ({ page }) => {
    await setup(page, sessions)
    await page.goto('/chat')
    await expect(page.getByText('No imported chats available.')).toBeVisible()
    await expect(page.getByPlaceholder('Ask a question... (Enter to send, Shift+Enter for newline)')).toBeVisible()
  })
}

test('failed list is visible and refresh retries', async ({ page }) => {
  await setup(page)
  let failed = true
  await page.route('**/api/v1/chats/import/sessions', route => route.fulfill(failed ? { status: 503, json: { error: 'Import unavailable' } } : { json: { sessions: [chat] } }))
  await page.goto('/chat')
  await expect(page.getByRole('region', { name: 'Imported chats' }).getByRole('alert')).toHaveText('Import unavailable')
  failed = false
  await page.getByRole('button', { name: 'Refresh imported chats' }).click()
  await expect(page.getByRole('button', { name: 'Imported A', exact: true })).toBeVisible()
})

test('focus rechecks revoked access and removes visible transcript', async ({ page }) => {
  await setup(page)
  let revoked = false
  await page.route(url => url.pathname.startsWith('/api/v1/chats/import/sessions/'), route => route.fulfill(revoked ? { status: 403, json: { error: 'Access revoked' } } : { json: detail() }))
  await page.goto('/chat')
  await page.getByRole('button', { name: 'Imported A', exact: true }).click()
  await expect(page.getByText('Private content A')).toBeVisible()
  revoked = true
  await page.route('**/api/v1/chats/import/sessions', route => route.fulfill({ json: { sessions: [] } }))
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(page.getByRole('button', { name: 'Imported A', exact: true })).toHaveCount(0)
  await expect(page.getByText('Private content A')).toHaveCount(0)
})

test('late selection response cannot replace current transcript', async ({ page }) => {
  await setup(page, [chat, second])
  let release!: () => void
  const hold = new Promise<void>(resolve => { release = resolve })
  let started = false
  let finished = false
  await page.route(url => url.pathname.startsWith('/api/v1/chats/import/sessions/'), async route => {
    const item = new URL(route.request().url()).searchParams.get('chat_id') === second.chat_id ? second : chat
    if (item === chat) { started = true; await hold }
    await route.fulfill({ json: detail(item) }).catch(() => {})
    if (item === chat) finished = true
  })
  await page.goto('/chat')
  await page.getByRole('button', { name: 'Imported A', exact: true }).click()
  await expect.poll(() => started).toBe(true)
  await page.getByRole('button', { name: 'Imported B', exact: true }).click()
  await expect(page.getByText('Content B')).toBeVisible()
  release()
  await expect.poll(() => finished).toBe(true)
  await expect(page.getByText('Private content A')).toHaveCount(0)
})

test('denied grant clears detail without publishing requested project', async ({ page }) => {
  await setup(page)
  await page.route(url => url.pathname.endsWith('/project'), route => route.fulfill({ status: 403, json: { error: 'Sharing denied' } }))
  await page.goto('/chat')
  await page.getByRole('button', { name: 'Imported A', exact: true }).click()
  await page.getByLabel('Imported chat project ID').fill('unauthorized')
  await page.getByRole('button', { name: 'Share with project', exact: true }).click()
  await expect(page.getByRole('region', { name: 'Imported chats' }).getByRole('alert')).toHaveText('Sharing denied')
  await expect(page.getByText('Private content A')).toHaveCount(0)
  await expect(page.getByText('Project: unauthorized', { exact: true })).toHaveCount(0)
})

test('failed revoke keeps no optimistic private acknowledgement', async ({ page }) => {
  await setup(page)
  await page.route(url => url.pathname.startsWith('/api/v1/chats/import/sessions/'), route => route.fulfill(route.request().method() === 'DELETE' ? { status: 503, json: { error: 'Revocation unavailable' } } : { json: detail(chat, 'project-a') }))
  await page.goto('/chat')
  await page.getByRole('button', { name: 'Imported A', exact: true }).click()
  await page.getByRole('button', { name: 'Revoke project sharing' }).click()
  await expect(page.getByRole('region', { name: 'Imported chats' }).getByRole('alert')).toHaveText('Revocation unavailable')
  await expect(page.getByText('Project: Private', { exact: true })).toHaveCount(0)
})

test('account change clears imported and local chats before reloading', async ({ page }) => {
  await setup(page)
  await page.addInitScript(() => {
    localStorage.setItem('levara.chat.user-a.messages', JSON.stringify([{ role: 'user', content: 'Old local private transcript', timestamp: 1 }]))
  })
  await page.goto('/chat')
  await page.getByRole('button', { name: 'Imported A', exact: true }).click()
  await expect(page.getByText('Private content A')).toBeVisible()
  await expect(page.getByText('Old local private transcript', { exact: true })).toBeVisible()
  await page.route('**/api/v1/chats/import/sessions', route => route.fulfill({ json: { sessions: [] } }))
  await page.route('**/api/v1/auth/me', route => route.fulfill({ json: { id: 'user-b', email: 'b@test.local' } }))
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(page.getByText('Old local private transcript', { exact: true })).toHaveCount(0)
  await expect(page.getByText('Private content A')).toHaveCount(0)
})

test('mismatched sharing acknowledgement cannot publish state', async ({ page }) => {
  await setup(page)
  await page.route(url => url.pathname.endsWith('/project'), route => route.fulfill({ json: { ...second, project_id: 'project-a' } }))
  await page.goto('/chat')
  await page.getByRole('button', { name: 'Imported A', exact: true }).click()
  await page.getByLabel('Imported chat project ID').fill('project-a')
  await page.getByRole('button', { name: 'Share with project', exact: true }).click()
  await expect(page.getByRole('region', { name: 'Imported chats' }).getByRole('alert')).toHaveText('Invalid sharing acknowledgement. Refresh before retrying.')
  await expect(page.getByText('Project: project-a', { exact: true })).toHaveCount(0)
  await expect(page.getByText('Private content A')).toHaveCount(0)
})

test('late sharing acknowledgement cannot restore previous selection', async ({ page }) => {
  await setup(page, [chat, second])
  let release!: () => void
  const hold = new Promise<void>(resolve => { release = resolve })
  let started = false
  await page.route(url => url.pathname.endsWith('/project'), async route => {
    started = true
    await hold
    await route.fulfill({ json: { ...chat, project_id: 'project-a' } }).catch(() => {})
  })
  await page.goto('/chat')
  await page.getByRole('button', { name: 'Imported A', exact: true }).click()
  await page.getByLabel('Imported chat project ID').fill('project-a')
  await page.getByRole('button', { name: 'Share with project', exact: true }).click()
  await expect.poll(() => started).toBe(true)
  await page.getByRole('button', { name: 'Imported B', exact: true }).click()
  await expect(page.getByText('Content B')).toBeVisible()
  release()
  await expect(page.getByText('Private content A')).toHaveCount(0)
  await expect(page.getByText('Project: project-a', { exact: true })).toHaveCount(0)
})

test('account switch aborts old local answer and preserves its old storage namespace', async ({ page }) => {
  await setup(page, [])
  let release!: () => void
  let started!: () => void
  const arrived = new Promise<void>(resolve => { started = resolve })
  const held = new Promise<void>(resolve => { release = resolve })
  await page.route('**/api/v1/search/text', async route => {
    started()
    await held
    await route.fulfill({ json: { answer: 'Old delayed answer', chunks: [] } }).catch(() => {})
  })
  await page.goto('/chat')
  await page.getByPlaceholder('Ask a question... (Enter to send, Shift+Enter for newline)').fill('Old private question')
  await page.getByRole('button', { name: 'Send', exact: true }).click()
  await arrived
  await expect(page.getByText('Old private question', { exact: true })).toBeVisible()
  await page.route('**/api/v1/auth/me', route => route.fulfill({ json: { id: 'user-b', email: 'b@test.local' } }))
  await page.evaluate(() => window.dispatchEvent(new Event('focus')))
  await expect(page.getByText('Old private question', { exact: true })).toHaveCount(0)
  await expect(page.getByPlaceholder('Ask a question... (Enter to send, Shift+Enter for newline)')).toBeVisible()
  release()
  await expect(page.getByText('Old delayed answer', { exact: true })).toHaveCount(0)
  const local = await page.evaluate(() => ({ old: localStorage.getItem('levara.chat.user-a.messages'), fresh: localStorage.getItem('levara.chat.user-b.messages') }))
  expect(local.old).toContain('Old private question')
  expect(local.fresh).not.toContain('Old private question')
  expect(local.old).not.toContain('Old delayed answer')
})

test('cross-tab bearer change reloads identity with the current token', async ({ page }) => {
  await setup(page, [])
  await page.addInitScript(() => {
    localStorage.setItem('levara_token', 'old-token')
    localStorage.setItem('levara.chat.user-a.messages', JSON.stringify([{ role: 'user', content: 'Old bearer transcript', timestamp: 1 }]))
  })
  await page.route('**/api/v1/auth/me', route => {
    const token = route.request().headers().authorization
    if (!token) return route.fulfill({ status: 401, json: {} })
    return route.fulfill({ json: token === 'Bearer new-token' ? { id: 'user-b', email: 'b@test.local' } : { id: 'user-a', email: 'a@test.local' } })
  })
  await page.goto('/chat')
  await expect(page.getByText('Old bearer transcript', { exact: true })).toBeVisible()
  await page.evaluate(() => {
    localStorage.setItem('levara_token', 'new-token')
    window.dispatchEvent(new StorageEvent('storage', { key: 'levara_token', newValue: 'new-token' }))
  })
  await expect(page.getByText('Old bearer transcript', { exact: true })).toHaveCount(0)
  await expect(page.getByPlaceholder('Ask a question... (Enter to send, Shift+Enter for newline)')).toBeVisible()
  await expect.poll(() => page.evaluate(() => localStorage.getItem('levara.chat.user-b.messages'))).toBe('[]')
})
