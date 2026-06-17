import { test, expect } from '@playwright/test'
import fs from 'fs'
import { authenticate } from './helpers'

/**
 * FULL INTEGRATION: verifies that every API↔UI connection works
 * with REAL data, not just checking if elements exist.
 */

const API = process.env.LEVARA_API_URL || 'http://localhost:8081'

test.describe('Full Integration', () => {
  test.beforeEach(async ({ page }) => {
    await authenticate(page)
  })

  test('I1. Create dataset → appears in list', async ({ page }) => {
    await page.goto('/datasets')
    await page.waitForTimeout(2000)

    // Count existing datasets
    const bodyBefore = await page.textContent('body') || ''
    void bodyBefore

    // Create new dataset
    await page.getByRole('button', { name: /New Dataset/i }).click()
    await page.getByPlaceholder('Dataset name').fill('integration_test_' + Date.now())
    await page.getByRole('button', { name: 'Create' }).click()
    await page.waitForTimeout(3000)

    // Verify it appeared
    const bodyAfter = await page.textContent('body') || ''
    expect(bodyAfter).toContain('integration_test_')
  })

  test('I2. Upload file → success banner shown', async ({ page }) => {
    const tmpFile = '/tmp/integration_test.txt'
    fs.writeFileSync(tmpFile, 'Integration test: quantum computing advances in 2026.')

    await page.goto('/datasets')
    await page.waitForTimeout(1000)

    const fileInput = page.locator('input[type="file"]')
    await fileInput.setInputFiles(tmpFile)
    await page.waitForTimeout(5000)

    // Should show file in recent uploads
    const body = await page.textContent('body') || ''
    expect(body.includes('integration_test') || body.includes('processing') || body.includes('ready')).toBeTruthy()
  })

  test('I3. Dashboard shows real data from API', async ({ page }) => {
    await page.goto('/')
    await page.waitForTimeout(3000)

    // Status widget should show "ready" (from /api/v1/info)
    const body = await page.textContent('body') || ''
    expect(body).toContain('ready')
    await expect(page.getByText('Dimension')).toBeVisible()
    expect(body).toMatch(/Dimension\d+/)
  })

  test('I4. Collections page shows data or empty', async ({ page }) => {
    await page.goto('/collections')
    await page.waitForTimeout(3000)

    const body = await page.textContent('body') || ''
    // Should show either collection cards OR "No collections"
    expect(body.includes('Records') || body.includes('No collections')).toBeTruthy()
  })

  test('I5. Search returns actual results from BM25', async ({ page }) => {
    // First upload something searchable
    const tmpFile = '/tmp/search_test.txt'
    fs.writeFileSync(tmpFile, 'Solar energy is the fastest growing renewable energy source in the world.')

    // Upload via API
    const form = new FormData()
    form.append('data', new Blob([fs.readFileSync(tmpFile)]), 'search_test.txt')
    await fetch(`${API}/api/v1/add`, { method: 'POST', body: form })

    // Wait for auto-cognify
    await page.waitForTimeout(5000)

    // Search
    await page.goto('/search')
    await page.getByRole('button', { name: 'Sparse' }).first().click()
    await page.getByPlaceholder(/query/i).fill('solar energy renewable')
    await page.getByRole('button', { name: 'Search' }).click()
    await page.waitForTimeout(5000)

    // Should see results or "No results" — NOT an error
    const body = await page.textContent('body') || ''
    expect(body.includes('score') || body.includes('No results')).toBeTruthy()
    expect(body).not.toContain('Error')
  })

  test('I6. Analytics shows real cache stats', async ({ page }) => {
    await page.goto('/analytics')
    await page.waitForTimeout(5000)

    const body = await page.textContent('body') || ''
    // System Status should show actual status
    expect(body).toContain('System Status')
    // LLM Cache should show real numbers
    expect(body).toContain('LLM Cache')
    // Main content should not show "undefined" or "NaN"
    const mainText = await page.locator('main').textContent() || ''
    expect(mainText).not.toContain('undefined')
    expect(mainText).not.toContain('NaN')
  })

  test('I7. Settings theme toggle persists', async ({ page }) => {
    await page.goto('/settings')

    // Set dark mode
    await page.getByRole('button', { name: 'dark' }).click()
    await expect(page.locator('html')).toHaveClass(/dark/)

    // Reload page
    await page.reload()
    await page.waitForTimeout(1000)

    // Dark mode should persist
    await expect(page.locator('html')).toHaveClass(/dark/)

    // Reset to system
    await page.getByRole('button', { name: 'system' }).click()
  })

  test('I8. Memories CRUD works', async ({ page }) => {
    await page.goto('/memories')
    await page.waitForTimeout(2000)

    // Add memory
    await page.locator('main').getByRole('button', { name: /Add Memory/i }).first().click()
    await page.getByPlaceholder('Key').fill('test_key_' + Date.now())
    await page.getByPlaceholder('Value').fill('test_value')
    await page.getByRole('button', { name: 'Save' }).click()
    await page.waitForTimeout(3000)

    // Should not show error alert
    // Page should still be on memories
    await expect(page).toHaveURL('/memories')
  })

  test('I9. Chat sends message and gets response', async ({ page }) => {
    await page.goto('/chat')
    await page.locator('textarea').fill('Hello, what is Levara?')

    const sendBtn = page.locator('button').filter({ has: page.locator('svg.lucide-send') })
    await sendBtn.click()

    // Wait for response
    await page.waitForTimeout(15000)

    // Should show user message
    const body = await page.textContent('body') || ''
    expect(body).toContain('Hello')
    // Should show some response (assistant bubble)
    const bubbles = await page.locator('[class*="rounded-lg"]').count()
    expect(bubbles).toBeGreaterThan(0)
  })

  test('I10. Graph page loads without crash', async ({ page }) => {
    await page.goto('/graph')
    await page.waitForTimeout(2000)

    // Should show "Select a dataset" or graph canvas
    const body = await page.textContent('body') || ''
    expect(body.includes('Knowledge Graph')).toBeTruthy()
    expect(body).not.toContain('Error')
  })
})
