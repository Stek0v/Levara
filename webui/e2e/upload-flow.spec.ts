import { test, expect, type Page, type Request } from '@playwright/test'
import { readFile } from 'node:fs/promises'

const dataset = { id: 'shared-dataset-id', name: 'Shared handbook', owner_id: 'other-owner', role: 'editor' }
const file = { name: 'handbook.txt', mimeType: 'text/plain', buffer: Buffer.from('Original handbook bytes — UTF-8\n') }

async function mockAPI(page: Page) {
  const state = {
    uploads: [] as Request[], cognify: [] as Request[], downloads: [] as Request[], statusRequests: [] as Request[],
    uploadStatus: 200, uploadError: 'Text extraction failed: no readable content',
    start: { status: 'PipelineRunStarted', pipeline_run_id: 'run-1' } as Record<string, unknown>,
    progress: { pipeline_run_id: 'run-1', status: 'COMPLETED', stage: 'done' } as Record<string, unknown>,
    downloadStatus: 200, statusCode: 200, datasetsReady: Promise.resolve(),
    shares: [] as { id: string; user_email: string; role: string }[],
  }
  await page.addInitScript(() => localStorage.setItem('levara_token', 'upload-test-token'))
  await page.route('**/api/v1/**', async (route) => {
    const request = route.request()
    const url = new URL(request.url())
    const path = url.pathname
    let body: unknown = []
    if (path === '/api/v1/auth/me') {
      if (request.headers().authorization !== 'Bearer upload-test-token') {
        return route.fulfill({ status: 401, json: { error: 'missing bearer' } })
      }
      body = { id: 'editor', email: 'editor@test.local', username: 'editor' }
    }
    else if (path === '/api/v1/settings') body = { locale: 'en', theme: 'light' }
    else if (path === '/api/v1/datasets') {
      await state.datasetsReady
      body = [dataset]
    }
    else if (path === '/api/v1/add') {
      state.uploads.push(request)
      return route.fulfill({ status: state.uploadStatus, json: state.uploadStatus === 200
        ? { status: 'ok', items: 1, dataset_id: dataset.id, dataset_name: dataset.name }
        : { detail: state.uploadError } })
    } else if (path === '/api/v1/cognify') {
      state.cognify.push(request)
      body = state.start
    } else if (/\/cognify\/[^/]+\/status$/.test(path)) {
      state.statusRequests.push(request)
      return route.fulfill({ status: state.statusCode, json: state.progress })
    }
    else if (/\/cognify\/[^/]+\/stream$/.test(path)) {
      return route.fulfill({ contentType: 'text/event-stream', body: `event: done\ndata: ${JSON.stringify(state.progress)}\n\n` })
    } else if (path === `/api/v1/datasets/${dataset.id}/data`) body = [
      { id: 'doc-1', name: file.name, extension: '.txt', data_size: file.buffer.length,
        pipeline_status: JSON.stringify({ [dataset.name]: { status: 'COMPLETED' } }) },
      { id: 'doc-2', name: 'other-collection.txt', pipeline_status: JSON.stringify({ unrelated: { status: 'COMPLETED' } }) },
    ]
    else if (path.endsWith('/raw')) {
      state.downloads.push(request)
      return state.downloadStatus === 200
        ? route.fulfill({ contentType: 'text/plain', body: file.buffer, headers: { 'content-disposition': 'attachment; filename="handbook.txt"' } })
        : route.fulfill({ status: 403, json: { detail: 'Download access denied' } })
    } else if (path === `/api/v1/datasets/${dataset.id}/shares`) {
      if (request.method() === 'POST') {
        const data = request.postDataJSON()
        state.shares = [{ id: 'share-1', user_email: data.email, role: data.role }]
        body = state.shares[0]
      } else body = state.shares
    } else if (path.endsWith('/shares/share-1') && request.method() === 'DELETE') state.shares = []
    await route.fulfill({ status: 200, json: body })
  })
  await page.route('**/health**', (route) => route.fulfill({ json: { status: 'ok' } }))
  return state
}

async function upload(page: Page) {
  await page.getByLabel('Target dataset').selectOption({ label: dataset.name })
  await page.locator('input[type=file]').setInputFiles(file)
}

test('existing shared dataset sends its ID, name, exact bytes and bearer', async ({ page }) => {
  const state = await mockAPI(page)
  await page.goto('/datasets')
  await upload(page)
  await expect.poll(() => state.uploads.length).toBe(1)
  const request = state.uploads[0]
  const form = await new Response(new Uint8Array(request.postDataBuffer()!), { headers: { 'Content-Type': request.headers()['content-type'] } }).formData()
  expect(form.get('datasetId')).toBe(dataset.id)
  expect(form.get('datasetName')).toBe(dataset.name)
  expect(Buffer.from(await (form.get('data') as File).arrayBuffer())).toEqual(file.buffer)
  expect(request.headers().authorization).toBe('Bearer upload-test-token')
  await expect(page.getByText('Ready to search', { exact: true })).toBeVisible()
  expect(state.cognify[0].postDataJSON()).toEqual({ datasets: [dataset.id], collection: dataset.name, skip_graph: true })
})

test('extraction failure is visible and the same file can be retried', async ({ page }) => {
  const state = await mockAPI(page)
  state.uploadStatus = 422
  await page.goto('/datasets')
  await upload(page)
  await expect(page.getByText(state.uploadError, { exact: true })).toBeVisible()
  expect(state.cognify).toHaveLength(0)
  await expect(page.locator('input[type=file]')).toHaveValue('')
  state.uploadStatus = 200
  await page.locator('input[type=file]').setInputFiles(file)
  await expect(page.getByText('Ready to search', { exact: true })).toBeVisible()
  expect(state.uploads).toHaveLength(2)
  await expect(page.getByText(state.uploadError, { exact: true })).toBeVisible()
})

test('missing cognify run ID is a visible failure, not readiness', async ({ page }) => {
  const state = await mockAPI(page)
  state.start = { status: 'PipelineRunStarted' }
  await page.goto('/datasets')
  await upload(page)
  await expect(page.getByText('Processing did not return a run ID.', { exact: true })).toBeVisible()
  await expect(page.getByText('Ready to search', { exact: true })).toHaveCount(0)
  await expect(page.getByLabel('Target dataset')).toBeEnabled()
})

for (const status of ['COMPLETED', 'SKIPPED', 'already_processed']) {
  test(`explicit ${status} without a run ID is ready`, async ({ page }) => {
    const state = await mockAPI(page)
    state.start = { status }
    await page.goto('/datasets')
    await upload(page)
    await expect(page.getByText('Ready to search', { exact: true })).toBeVisible()
    await expect(page.locator('input[type=file]')).toBeEnabled()
  })
}

test('failed processing reports the reason and preserves earlier completed uploads', async ({ page }) => {
  const state = await mockAPI(page)
  await page.goto('/datasets')
  await upload(page)
  await expect(page.getByText('Ready to search', { exact: true })).toBeVisible()
  state.start = { status: 'PipelineRunStarted', pipeline_run_id: 'run-2' }
  state.progress = { pipeline_run_id: 'run-2', status: 'FAILED', message: 'Embedding provider unavailable' }
  await page.locator('input[type=file]').setInputFiles({ ...file, name: 'second.txt' })
  await expect(page.getByText('Embedding provider unavailable', { exact: true })).toBeVisible()
  await expect(page.getByText('Ready to search', { exact: true })).toHaveCount(1)
})

test('active batch disables uploads and ignores drops until matching run completes', async ({ page }) => {
  const state = await mockAPI(page)
  state.progress = { pipeline_run_id: 'run-1', status: 'RUNNING', stage: 'embedding' }
  await page.goto('/datasets')
  await upload(page)
  await expect(page.getByLabel('Target dataset')).toBeDisabled()
  await expect(page.locator('input[type=file]')).toBeDisabled()
  const drop = await page.evaluateHandle(() => {
    const dt = new DataTransfer()
    dt.items.add(new File(['must not upload'], 'overlap.txt', { type: 'text/plain' }))
    return dt
  })
  await page.locator('div.border-dashed').dispatchEvent('drop', { dataTransfer: drop })
  await drop.dispose()
  state.progress = { pipeline_run_id: 'run-1', status: 'COMPLETED' }
  await expect(page.getByText('Ready to search', { exact: true })).toBeVisible()
  expect(state.uploads).toHaveLength(1)
  expect(state.statusRequests[0].headers().authorization).toBe('Bearer upload-test-token')
  await expect(page.getByLabel('Target dataset')).toBeEnabled()
})

test('detail shows completion only for its collection and downloads authenticated original bytes', async ({ page, context }) => {
  const state = await mockAPI(page)
  await page.goto(`/datasets/${dataset.id}`)
  await context.addCookies([{ name: 'download-cookie', value: 'test', domain: '127.0.0.1', path: '/' }])
  const row = page.getByRole('row').filter({ hasText: file.name })
  await expect(row.getByText('Processed', { exact: true })).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: 'other-collection.txt' }).getByText('Processed', { exact: true })).toHaveCount(0)
  const downloadEvent = page.waitForEvent('download')
  await row.getByRole('button', { name: 'Download original' }).click()
  const download = await downloadEvent
  expect(download.suggestedFilename()).toBe(file.name)
  expect(await readFile((await download.path())!)).toEqual(file.buffer)
  expect(state.downloads).toHaveLength(1)
  expect(new URL(state.downloads[0].url()).searchParams.get('original')).toBe('true')
  expect(state.downloads[0].headers().authorization).toBe('Bearer upload-test-token')
  expect((await state.downloads[0].allHeaders()).cookie).toContain('download-cookie=test')
  state.downloadStatus = 403
  await row.getByRole('button', { name: 'Download original' }).click()
  await expect(page.getByText('Download access denied', { exact: true })).toBeVisible()
  expect(state.downloads).toHaveLength(2)
})

test('individual viewer share is granted and revoked in the selected dataset', async ({ page }) => {
  const state = await mockAPI(page)
  await page.goto(`/datasets/${dataset.id}`)
  await page.getByPlaceholder('User email').fill('reader@test.local')
  await page.getByRole('button', { name: 'Grant access' }).click()
  await expect(page.getByText('reader@test.local', { exact: true })).toBeVisible()
  expect(state.shares[0].role).toBe('viewer')
  await page.getByTitle('Revoke', { exact: true }).click()
  await expect(page.getByText('reader@test.local', { exact: true })).toHaveCount(0)
})

for (const payload of [
  { pipeline_run_id: 'wrong-run', status: 'COMPLETED' },
  { pipeline_run_id: 'run-1', stage: 'done' },
]) {
  test(`unconfirmed processing result never becomes ready: ${JSON.stringify(payload)}`, async ({ page }) => {
    const state = await mockAPI(page)
    state.progress = payload
    await page.goto('/datasets')
    await upload(page)
    await expect(page.getByText('Invalid processing status response.', { exact: true })).toBeVisible()
    await expect(page.getByText('Ready to search', { exact: true })).toHaveCount(0)
    await expect(page.locator('input[type=file]')).toBeEnabled()
  })
}

test('status access failure ends the batch with an error', async ({ page }) => {
  const state = await mockAPI(page)
  state.statusCode = 403
  state.progress = { detail: 'Processing access denied' }
  await page.goto('/datasets')
  await upload(page)
  await expect(page.getByText('Processing access denied', { exact: true })).toBeVisible()
  await expect(page.getByText('Ready to search', { exact: true })).toHaveCount(0)
  await expect(page.getByLabel('Target dataset')).toBeEnabled()
})

test('new upload creates a named dataset without claiming an existing ID', async ({ page }) => {
  const state = await mockAPI(page)
  await page.goto('/datasets')
  await expect(page.getByLabel('Target dataset')).toHaveValue('')
  await page.locator('input[type=file]').setInputFiles(file)
  await expect(page.getByText('Ready to search', { exact: true })).toBeVisible()
  const request = state.uploads[0]
  const form = await new Response(new Uint8Array(request.postDataBuffer()!), { headers: { 'Content-Type': request.headers()['content-type'] } }).formData()
  expect(form.get('datasetId')).toBeNull()
  expect(form.get('datasetName')).toMatch(/^upload-\d+$/)
})

test('detail cognify handles missing ID, FAILED, and explicit completion with retry', async ({ page }) => {
  const state = await mockAPI(page)
  state.start = { status: 'PipelineRunStarted' }
  await page.goto(`/datasets/${dataset.id}`)
  const button = page.getByRole('button', { name: 'Cognify', exact: true })
  await button.click()
  await expect(page.getByText('Processing did not return a run ID.', { exact: true })).toBeVisible()
  await expect(button).toBeEnabled()
  state.start = { status: 'PipelineRunStarted', pipeline_run_id: 'run-1' }
  state.progress = { pipeline_run_id: 'run-1', status: 'FAILED', message: 'Cannot embed this document' }
  await button.click()
  await expect(page.getByText('Cannot embed this document', { exact: true })).toBeVisible()
  await expect(button).toBeEnabled()
  state.start = { status: 'already_processed' }
  await button.click()
  await expect(button).toBeEnabled()
  await expect(page.getByText('Cannot embed this document', { exact: true })).toHaveCount(0)
  expect(state.cognify).toHaveLength(3)
  expect(state.cognify[0].postDataJSON()).toEqual({ datasets: [dataset.id], collection: dataset.name })
})

test('detail waits for the collection name before allowing Cognify', async ({ page }) => {
  const state = await mockAPI(page)
  let resolveDatasets!: () => void
  state.datasetsReady = new Promise<void>((resolve) => { resolveDatasets = resolve })
  try {
    await page.goto(`/datasets/${dataset.id}`)
    const button = page.getByRole('button', { name: 'Cognify', exact: true })
    await expect(button).toBeDisabled()
    await expect(page.getByRole('row').filter({ hasText: file.name }).getByText('Processed', { exact: true })).toHaveCount(0)
    expect(state.cognify).toHaveLength(0)
    resolveDatasets()
    await expect(button).toBeEnabled()
    await expect(page.getByRole('row').filter({ hasText: file.name }).getByText('Processed', { exact: true })).toBeVisible()
  } finally {
    resolveDatasets()
  }
})
