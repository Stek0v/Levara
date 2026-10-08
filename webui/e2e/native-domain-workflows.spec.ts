import { test, expect, request, type APIRequestContext, type Page } from '@playwright/test'
import { readFile } from 'node:fs/promises'

const origin = process.env.LEVARA_T30_NATIVE_API
const enabled = process.env.LEVARA_T30_DISPOSABLE === '1'
const password = 'Disposable-t30-only-Password1!'
test.skip(!enabled || !origin, 'Requires explicitly disposable T30 backend')
test.setTimeout(90000)
test.beforeAll(async () => {
  const target = new URL(origin!)
  expect(target.hostname).toBe('127.0.0.1')
  expect(target.port).not.toBe('8081')
  const health = await request.newContext({ baseURL: origin })
  try { expect((await health.get('/health')).ok()).toBeTruthy() } finally { await health.dispose() }
})

async function okJSON(response: Awaited<ReturnType<APIRequestContext['get']>>) {
  expect(response.ok(), await response.text()).toBeTruthy()
  return response.json()
}

async function account(email: string) {
  const signup = await request.newContext({ baseURL: origin })
  try {
    const user = await okJSON(await signup.post('/api/v1/auth/register', { data: { email, password } }))
    const token = user.access_token || user.token
    expect(typeof token).toBe('string')
    return request.newContext({ baseURL: origin, extraHTTPHeaders: { Authorization: 'Bearer ' + token } })
  } finally { await signup.dispose() }
}

async function login(page: Page, email: string) {
  await page.goto('/login?next=%2Fchat')
  await page.getByLabel(/email|почта/i).fill(email)
  await page.getByLabel(/password|пароль/i).fill(password)
  await page.locator('form').getByRole('button').click()
  await expect(page).toHaveURL(/\/chat$/)
  await expect(page.getByRole('region', { name: 'Imported chats' })).toBeVisible()
}

test('real owner consent, admin audience grant/revoke and detach preserve private source', async ({ browser }) => {
  const suffix = Date.now() + '-' + Math.random().toString(16).slice(2)
  const ownerEmail = 'owner-' + suffix + '@test.invalid'
  const adminEmail = 'admin-' + suffix + '@test.invalid'
  const memberEmail = 'member-' + suffix + '@test.invalid'
  const owner = await account(ownerEmail)
  const admin = await account(adminEmail)
  const member = await account(memberEmail)
  const ownerContext = await browser.newContext()
  const adminContext = await browser.newContext()
  const memberContext = await browser.newContext()
  try {
    const project = await okJSON(await owner.post('/api/v1/datasets', { data: { name: 't30-' + suffix } }))
    expect(typeof project.id).toBe('string')
    await okJSON(await owner.post('/api/v1/datasets/' + project.id + '/shares', { data: { email: adminEmail, role: 'admin' } }))
    const title = 'Native imported ' + suffix
    const content = 'Private transcript ' + suffix
    const imported = await okJSON(await owner.post('/api/v1/chats/import', { data: { finish: true, conversation: {
      platform: 'codex', session_id: 'native-' + suffix, title,
      messages: [{ external_id: 'm1', ordinal: 1, role: 'user', kind: 'text', content }],
    } } }))
    const sessionPath = '/api/v1/chats/import/sessions/codex/' + encodeURIComponent(imported.session_id) + '?chat_id=' + encodeURIComponent(imported.chat_id)
    expect((await admin.get(sessionPath)).status()).toBe(403)
    expect((await member.get(sessionPath)).status()).toBe(403)

    const ownerPage = await ownerContext.newPage()
    const adminPage = await adminContext.newPage()
    const memberPage = await memberContext.newPage()
    await login(ownerPage, ownerEmail)
    const ownerChat = ownerPage.getByRole('region', { name: 'Imported chats' })
    await ownerChat.getByRole('button', { name: title, exact: true }).click()
    await expect(ownerChat.getByText(content, { exact: true })).toBeVisible()
    await ownerChat.getByLabel('Imported chat project ID').fill(project.id)
    await ownerChat.getByRole('button', { name: 'Share with project', exact: true }).click()
    await expect(ownerChat.getByText('Project: ' + project.id, { exact: true })).toBeVisible()

    await login(adminPage, adminEmail)
    await adminPage.goto('/datasets/' + project.id)
    await adminPage.getByPlaceholder(/email пользователя|user email/i).fill(memberEmail)
    await adminPage.getByRole('button', { name: /выдать доступ|grant access/i }).click()
    await expect(adminPage.getByText(memberEmail, { exact: true })).toBeVisible()
    const shared = await okJSON(await member.get(sessionPath))
    expect(shared.messages[0].content).toBe(content)

    await login(memberPage, memberEmail)
    const memberChat = memberPage.getByRole('region', { name: 'Imported chats' })
    await memberChat.getByRole('button', { name: title, exact: true }).click()
    await expect(memberChat.getByText(content, { exact: true })).toBeVisible()
    await memberPage.reload()
    await memberChat.getByRole('button', { name: title, exact: true }).click()
    await expect(memberChat.getByText(content, { exact: true })).toBeVisible()
    await memberChat.getByLabel('Imported chat project ID').fill(project.id)
    await memberChat.getByRole('button', { name: 'Share with project', exact: true }).click()
    await expect(memberChat.getByRole('alert')).toBeVisible()
    await expect(memberChat.getByText(content, { exact: true })).toHaveCount(0)
    await memberChat.getByRole('button', { name: 'Refresh imported chats' }).click()
    await expect(memberChat.getByText(content, { exact: true })).toBeVisible()

    await adminPage.getByText(memberEmail, { exact: true }).locator('..').getByTitle(/отозвать|revoke/i).click()
    await expect(adminPage.getByText(memberEmail, { exact: true })).toHaveCount(0)
    expect((await member.get(sessionPath)).status()).toBe(403)
    await memberPage.evaluate(() => window.dispatchEvent(new Event('focus')))
    await expect(memberChat.getByText(content, { exact: true })).toHaveCount(0)
    await expect(memberChat.getByRole('button', { name: title, exact: true })).toHaveCount(0)

    await adminPage.goto('/chat')
    const adminChat = adminPage.getByRole('region', { name: 'Imported chats' })
    await adminChat.getByRole('button', { name: title, exact: true }).click()
    await adminChat.getByRole('button', { name: 'Revoke project sharing' }).click()
    await expect(adminChat.getByText(content, { exact: true })).toHaveCount(0)
    expect((await admin.get(sessionPath)).status()).toBe(403)
    const retained = await okJSON(await owner.get(sessionPath))
    expect(retained.project_id).toBe('')
    expect(retained.messages[0].content).toBe(content)
    await ownerChat.getByRole('button', { name: 'Refresh imported chats' }).click()
    await expect(ownerChat.getByText('Project: Private', { exact: true })).toBeVisible()
    await expect(ownerChat.getByText(content, { exact: true })).toBeVisible()

    const logout = ownerPage.getByRole('button', { name: /log out|выйти/i })
    await logout.focus()
    await logout.press('Enter')
    await expect(ownerPage).toHaveURL(/\/login/)
    await login(ownerPage, memberEmail)
    await expect(ownerPage.getByRole('region', { name: 'Imported chats' }).getByText(content, { exact: true })).toHaveCount(0)
  } finally {
    await Promise.all([owner.dispose(), admin.dispose(), member.dispose(), ownerContext.close(), adminContext.close(), memberContext.close()])
  }
})

test('native workspace CAS refuses overwrite and preserves the browser draft', async ({ browser }) => {
  const suffix = Date.now() + '-workspace'
  const email = suffix + '@test.invalid'
  const api = await account(email)
  const context = await browser.newContext()
  try {
    const project = await okJSON(await api.post('/api/v1/datasets', { data: { name: suffix } }))
    const file = { project_id: project.id, branch: 'main', path: 'docs/note.md' }
    await okJSON(await api.post('/api/v1/workspace/write', { data: { ...file, text: 'original native text', expected_file_digest: '', index: false } }))
    const page = await context.newPage()
    await login(page, email)
    await page.goto('/workspace')
    await page.getByLabel('Project ID', { exact: true }).fill(project.id)
    await page.getByRole('button', { name: 'Read', exact: true }).click()
    await expect(page.getByText('original native text', { exact: true })).toBeVisible()
    const read = await okJSON(await api.get('/api/v1/workspace/read', { params: file }))
    expect(read.file_digest).toMatch(/^[0-9a-f]{64}$/)
    await okJSON(await api.post('/api/v1/workspace/write', { data: { ...file, text: 'concurrent native text', expected_file_digest: read.file_digest, index: false } }))
    await page.getByLabel('File text').fill('unsaved browser draft')
    const write = page.waitForResponse(r => r.url().endsWith('/api/v1/workspace/write'))
    await page.getByRole('button', { name: 'Write + Index', exact: true }).click()
    const rejected = await write
    expect(rejected.status()).toBe(400)
    expect((await rejected.json()).error).toContain('workspace write conflict')
    await expect(page.getByRole('alert').filter({ hasText: 'workspace write conflict' })).toBeVisible()
    await expect(page.getByLabel('File text')).toHaveValue('unsaved browser draft')
    await expect(page.getByRole('button', { name: 'Write + Index', exact: true })).toBeDisabled()
    expect((await okJSON(await api.get('/api/v1/workspace/read', { params: file }))).text).toBe('concurrent native text')
    await page.getByRole('button', { name: 'Read', exact: true }).click()
    await expect(page.getByText('concurrent native text', { exact: true })).toBeVisible()
    await expect(page.getByLabel('File text')).toHaveValue('unsaved browser draft')
    await expect(page.getByRole('button', { name: 'Write + Index', exact: true })).toBeEnabled()
  } finally { await Promise.all([api.dispose(), context.close()]) }
})

test('native memory room and hall survive reload and ID deletion', async ({ browser }) => {
  const suffix = Date.now() + '-memory'
  const email = suffix + '@test.invalid'
  const api = await account(email)
  const context = await browser.newContext()
  try {
    const page = await context.newPage()
    await login(page, email)
    await page.goto('/memories')
    await page.getByRole('button', { name: /add memory|добавить/i }).first().click()
    await page.getByPlaceholder(/^(key|ключ)$/i).fill(suffix)
    await page.getByPlaceholder(/^(value|значение)$/i).fill('Native room hall value')
    await page.getByPlaceholder(/^(room|комната)/i).fill('memory')
    await page.locator('select').last().selectOption('discovery')
    await page.getByRole('button', { name: /^(save|сохранить)$/i }).click()
    await expect(page.getByText(suffix, { exact: true })).toBeVisible()
    await page.reload()
    const row = page.getByText(suffix, { exact: true }).locator('../..')
    await expect(row.getByText('Native room hall value', { exact: true })).toBeVisible()
    await expect(row.getByText('· memory', { exact: true })).toBeVisible()
    await expect(row.getByText('· discovery', { exact: true })).toBeVisible()
    const memories = await okJSON(await api.get('/api/v1/memories'))
    const saved = memories.find((m: { key: string }) => m.key === suffix)
    expect(saved).toMatchObject({ room: 'memory', hall: 'discovery' })
    await row.getByRole('button', { name: new RegExp(suffix) }).click()
    await expect(page.getByText(suffix, { exact: true })).toHaveCount(0)
    await page.reload()
    await expect(page.getByText(suffix, { exact: true })).toHaveCount(0)
    expect((await okJSON(await api.get('/api/v1/memories'))).some((m: { id: string }) => m.id === saved.id)).toBe(false)
  } finally { await Promise.all([api.dispose(), context.close()]) }
})

test('native Task leases, failed steps and private owner scope agree with observation', async ({ browser }) => {
  const suffix = Date.now() + '-task'
  const email = suffix + '@test.invalid'
  const owner = await account(email)
  const other = await account('other-' + email)
  const context = await browser.newContext()
  try {
    const call = async (name: string, args: Record<string, unknown>) => {
      const rpc = await okJSON(await owner.post('/mcp/2026-07-28', {
        headers: { Accept: 'application/json, text/event-stream', 'MCP-Protocol-Version': '2026-07-28', 'Mcp-Method': 'tools/call', 'Mcp-Name': name },
        data: { jsonrpc: '2.0', id: name, method: 'tools/call', params: { name, arguments: args, _meta: {
          'io.modelcontextprotocol/protocolVersion': '2026-07-28',
          'io.modelcontextprotocol/clientInfo': { name: 't30-native-browser', version: '1' },
          'io.modelcontextprotocol/clientCapabilities': {},
        } } },
      }))
      expect(rpc.error).toBeUndefined()
      expect(rpc.result.isError, JSON.stringify(rpc.result)).not.toBe(true)
      const payload = JSON.parse(rpc.result.content[0].text)
      if (name !== 'task_open') expect(payload.ok, JSON.stringify(payload)).toBe(true)
      expect(typeof payload.task_id).toBe('string')
      expect(Number.isInteger(payload.version)).toBe(true)
      return payload
    }
    const opened = await call('task_open', { collection: 't30-native', room: 'task-runtime', objective: suffix, risk_level: 'low', idempotency_key: suffix, definition_of_done: [{ criterion_id: 'visible', description: 'Native lifecycle is observable' }] })
    let version = (await call('task_plan', { task_id: opened.task_id, base_version: opened.version, steps: [
      { step_id: 'failed-native', description: 'Native failed step', criterion_ids: ['visible'] },
      { step_id: 'leased-native', description: 'Native leased step', criterion_ids: ['visible'] },
    ] })).version
    version = (await call('task_step', { task_id: opened.task_id, base_version: version, step_id: 'failed-native', actor_id: 'native-browser', action: 'claim', lease_seconds: 300 })).version
    version = (await call('task_step', { task_id: opened.task_id, base_version: version, step_id: 'failed-native', actor_id: 'native-browser', action: 'fail' })).version
    version = (await call('task_step', { task_id: opened.task_id, base_version: version, step_id: 'leased-native', actor_id: 'native-browser', action: 'claim', lease_seconds: 300 })).version
    await call('task_receipt', { task_id: opened.task_id, base_version: version, step_id: 'leased-native', actor_id: 'native-browser', criterion_ids: ['visible'], receipt_type: 'observation', status: 'pass', observation: 'Native receipt observation', workspace_revision: 'native-disposable-fixture', idempotency_key: suffix + '-receipt' })
    const listed = await okJSON(await owner.get('/api/v1/tasks', { params: { status: 'running' } }))
    expect(listed.tasks.find((x: { id: string }) => x.id === opened.task_id).step_counts).toMatchObject({ claimed: 1, failed: 1 })
    expect((await other.get('/api/v1/tasks/' + opened.task_id)).status()).toBe(404)
    expect((await okJSON(await other.get('/api/v1/tasks'))).tasks.some((x: { id: string }) => x.id === opened.task_id)).toBe(false)
    const page = await context.newPage()
    await login(page, email)
    await page.goto('/tasks')
    await page.getByRole('button', { name: 'running', exact: true }).click()
    await page.getByRole('button', { name: new RegExp(suffix) }).click()
    await expect(page.getByText('Native lifecycle is observable', { exact: true })).toBeVisible()
    await expect(page.getByText('Native receipt observation', { exact: true })).toBeVisible()
    await expect(page.getByRole('listitem').filter({ hasText: 'Native failed step' })).toBeVisible()
    await expect(page.getByRole('listitem').filter({ hasText: 'Native leased step' })).toBeVisible()
    await expect(page.getByText('lease: native-browser', { exact: true })).toBeVisible()
    await expect(page.getByText(/^lease expires:/)).toBeVisible()
    await page.reload()
    await page.getByRole('button', { name: new RegExp(suffix) }).click()
    await expect(page.getByText('lease: native-browser', { exact: true })).toBeVisible()
  } finally { await Promise.all([owner.dispose(), other.dispose(), context.close()]) }
})


test('native document upload, search, exact download and administrator CAS grants/revocation', async ({ browser }) => {
  const suffix = Date.now() + '-document'
  const email = suffix + '@test.invalid'
  const owner = await account(email)
  const context = await browser.newContext()
  try {
    const project = await okJSON(await owner.post('/api/v1/datasets', { data: { name: suffix } }))
    await okJSON(await owner.put('/api/v1/settings', { data: { locale: 'en' } }))
    const marker = 'nativehandbook' + Date.now()
    const file = { name: suffix + '.txt', mimeType: 'text/plain', buffer: Buffer.from(marker + ' searchable handbook original bytes — UTF-8\n') }
    const page = await context.newPage()
    await login(page, email)
    await page.goto('/datasets')
    await page.getByLabel('Target dataset').selectOption(project.id)
    await page.locator('input[type=file]').setInputFiles(file)
    await expect(page.getByText('Ready to search', { exact: true })).toBeVisible({ timeout: 45000 })
    await page.goto('/datasets/' + project.id)
    const row = page.getByRole('row').filter({ hasText: file.name })
    await expect(row.getByText('Processed', { exact: true })).toBeVisible()
    await page.reload()
    await expect(row.getByText('Processed', { exact: true })).toBeVisible()
    const downloadEvent = page.waitForEvent('download')
    await row.getByRole('button', { name: 'Download original' }).click()
    const download = await downloadEvent
    expect(download.suggestedFilename()).toBe(file.name)
    expect(await readFile((await download.path())!)).toEqual(file.buffer)
    await page.goto('/search')
    await page.getByLabel('Collection', { exact: true }).fill(suffix)
    await page.getByLabel('Search mode', { exact: true }).selectOption('CHUNKS_LEXICAL')
    await page.getByPlaceholder('Enter your query...').fill(marker)
    await page.getByRole('button', { name: 'Search', exact: true }).click()
    await expect(page.getByText(new RegExp(marker))).toBeVisible({ timeout: 15000 })
    const peerEmail = suffix + '-peer@test.invalid'
    const peer = await account(peerEmail)
    try {
      const ownerID = (await okJSON(await owner.get('/api/v1/auth/me'))).id
      const peerID = (await okJSON(await peer.get('/api/v1/auth/me'))).id
      const tenant = await okJSON(await owner.post('/api/v1/tenants', { data: { name: suffix + '-team' } }))
      await okJSON(await owner.post('/api/v1/tenants/' + tenant.id + '/users', { data: { user_id: peerID } }))
      const records = await okJSON(await owner.get('/api/v1/datasets/' + project.id + '/data'))
      const record = (Array.isArray(records) ? records : records.data).find((item: { name: string }) => item.name === file.name)
      expect(typeof record.id).toBe('string')
      const documentPath = '/api/v1/datasets/' + project.id + '/data/' + record.id
      await page.goto('/datasets/' + project.id)
      await page.getByRole('button', { name: 'Document access: ' + file.name }).click()
      const dialog = page.getByRole('dialog')
      await dialog.getByRole('button', { name: 'Enable document access' }).click()
      await expect(dialog.getByText('restricted', { exact: true })).toBeVisible()
      await dialog.getByLabel('Recipient', { exact: true }).selectOption(peerID)
      const policy = await okJSON(await owner.get(documentPath + '/policy'))
      expect((await peer.get(documentPath + '/raw?original=true')).status()).toBe(403)
      await okJSON(await owner.post(documentPath + '/grants', { data: { acl_revision: policy.acl_revision, principal_kind: 'user', principal_id: ownerID, role: 'viewer' } }))
      await dialog.getByRole('button', { name: 'Grant access', exact: true }).click()
      await expect(dialog.getByText('The policy changed. Data was refreshed; try again.')).toBeVisible()
      await dialog.getByRole('button', { name: 'Grant access', exact: true }).click()
      await expect(dialog.getByText(peerEmail + ' · Viewer', { exact: true })).toBeVisible()
      const original = await peer.get(documentPath + '/raw?original=true')
      expect(original.status()).toBe(200)
      expect(await original.body()).toEqual(file.buffer)
      await dialog.getByText(peerEmail + ' · Viewer', { exact: true }).locator('..').getByRole('button', { name: 'Revoke', exact: true }).click()
      await expect(dialog.getByText(peerEmail + ' · Viewer', { exact: true })).toHaveCount(0)
      expect((await peer.get(documentPath + '/raw?original=true')).status()).toBe(403)
      await page.reload()
      await page.getByRole('button', { name: 'Document access: ' + file.name }).click()
      await expect(page.getByRole('dialog').getByText(peerEmail + ' · Viewer', { exact: true })).toHaveCount(0)
    } finally { await peer.dispose() }
  } finally { await Promise.all([owner.dispose(), context.close()]) }
})

test('native SQL graph path and selected properties disappear after project revocation', async ({ browser }) => {
  const email = 'native-admin-t30@test.invalid'
  const loginAPI = await request.newContext({ baseURL: origin })
  const signed = await okJSON(await loginAPI.post('/api/v1/auth/login', { data: { email, password } }))
  await loginAPI.dispose()
  const admin = await request.newContext({ baseURL: origin, extraHTTPHeaders: { Authorization: 'Bearer ' + (signed.access_token || signed.token) } })
  const suffix = Date.now() + '-graph'
  const memberEmail = suffix + '@test.invalid'
  const member = await account(memberEmail)
  const context = await browser.newContext()
  try {
    expect((await okJSON(await admin.get('/api/v1/auth/me'))).is_superuser).toBe(true)
    const project = await okJSON(await admin.post('/api/v1/datasets', { data: { name: suffix } }))
    const a = suffix + '-a', b = suffix + '-b'
    const imported = await okJSON(await admin.post('/api/v1/sync/import/graph', { data: {
      nodes: [
        { id: a, name: suffix + ' protected', type: 'Person', properties: JSON.stringify({ secret: 'Native private graph property' }), dataset_id: project.id },
        { id: b, name: suffix + ' other', type: 'Person', properties: '{}', dataset_id: project.id },
      ],
      edges: [{ id: suffix + '-edge', source_id: a, target_id: b, relationship_name: 'related_to', properties: '{}', confidence: 1, dataset_id: project.id }],
    } }))
    expect(imported.status).not.toBe('error')
    const grant = await okJSON(await admin.post('/api/v1/datasets/' + project.id + '/shares', { data: { email: memberEmail, role: 'viewer' } }))
    const page = await context.newPage()
    await login(page, memberEmail)
    await page.goto('/graph')
    await page.getByLabel('Dataset', { exact: true }).selectOption(project.id)
    await page.getByRole('button', { name: new RegExp(suffix + ' protected') }).click()
    await expect(page.getByText('Native private graph property', { exact: true })).toBeVisible()
    await page.getByLabel('Path from node id').fill(a)
    await page.getByLabel('Path to node id').fill(b)
    await page.getByRole('button', { name: 'Path', exact: true }).click()
    await expect(page.getByText('1 path edges', { exact: true })).toBeVisible()
    await okJSON(await admin.delete('/api/v1/datasets/' + project.id + '/shares/' + grant.id))
    expect((await member.get('/api/v1/datasets/' + project.id + '/graph')).status()).toBe(403)
    await page.getByRole('button', { name: 'Refresh graph', exact: true }).click()
    await expect(page.getByRole('alert').filter({ hasText: 'Graph unavailable:' })).toBeVisible({ timeout: 15000 })
    await expect(page.getByText('Native private graph property', { exact: true })).toHaveCount(0)
    await expect(page.getByText('1 path edges', { exact: true })).toHaveCount(0)
    await expect(page.getByRole('button', { name: new RegExp(suffix + ' protected') })).toHaveCount(0)
  } finally { await Promise.all([admin.dispose(), member.dispose(), context.close()]) }
})
