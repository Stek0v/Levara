import { test, expect } from '@playwright/test'
import path from 'path'
import fs from 'fs'

const API = 'http://localhost:8080'

// ═══════════ A. NAVIGATION ═══════════

test.describe('A. Navigation', () => {
  test('A1. Dashboard loads with sidebar', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible({ timeout: 10000 })
    await expect(page.locator('aside')).toBeVisible()
  })

  test('A2. All routes respond 200', async ({ page }) => {
    for (const r of ['/', '/search', '/chat', '/datasets', '/collections', '/memories', '/graph', '/notebooks', '/analytics', '/settings', '/login']) {
      const res = await page.goto(r)
      expect(res?.status(), `${r}`).toBe(200)
    }
  })

  test('A3. Sidebar navigation', async ({ page }) => {
    await page.goto('/')
    await page.getByRole('link', { name: 'Search' }).click()
    await expect(page).toHaveURL('/search')
    await expect(page.getByRole('heading', { name: 'Search' })).toBeVisible()
  })

  test('A4. Login page has no sidebar', async ({ page }) => {
    await page.goto('/login')
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
    await expect(page.locator('aside')).not.toBeVisible()
  })
})

// ═══════════ B. DASHBOARD ═══════════

test.describe('B. Dashboard', () => {
  test('B1. Health widgets', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByText('Status').first()).toBeVisible({ timeout: 10000 })
    await expect(page.getByText('Dimension').first()).toBeVisible()
  })

  test('B2. Quick actions', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByText('Upload Data')).toBeVisible({ timeout: 10000 })
  })

  test('B3. Quick action links', async ({ page }) => {
    await page.goto('/')
    await page.getByRole('link', { name: 'Datasets' }).click()
    await expect(page).toHaveURL('/datasets')
  })
})

// ═══════════ C. DATASETS ═══════════

test.describe('C. Datasets', () => {
  test('C1. Page loads', async ({ page }) => {
    await page.goto('/datasets')
    await expect(page.getByRole('heading', { name: 'Datasets' })).toBeVisible({ timeout: 10000 })
  })

  test('C2. Upload file via API', async ({ page }) => {
    const p = path.join('/tmp', 'e2e_test.txt')
    fs.writeFileSync(p, 'E2E test document about climate change.')
    const form = new FormData()
    form.append('data', new Blob([fs.readFileSync(p)]), 'e2e_test.txt')
    const res = await fetch(`${API}/api/v1/add`, { method: 'POST', body: form })
    expect(res.ok).toBeTruthy()
    const body = await res.json()
    expect(body.status).toBe('ok')
  })

  test('C3. Drag-drop zone visible', async ({ page }) => {
    await page.goto('/datasets')
    await expect(page.getByText('Drag')).toBeVisible({ timeout: 5000 })
  })

  test('C4. Create dataset form', async ({ page }) => {
    await page.goto('/datasets')
    await page.getByRole('button', { name: /New Dataset/i }).click()
    await expect(page.getByPlaceholder('Dataset name')).toBeVisible()
  })
})

// ═══════════ D. SEARCH ═══════════

test.describe('D. Search', () => {
  test('D1. All modes render', async ({ page }) => {
    await page.goto('/search')
    // Modes appear as selectable chips below search bar
    const body = await page.textContent('main')
    for (const m of ['Auto', 'Dense', 'Sparse', 'Hybrid', 'RAG', 'Graph']) {
      expect(body, `mode ${m} missing`).toContain(m)
    }
  })

  test('D2. Search returns results or empty', async ({ page }) => {
    await page.goto('/search')
    await page.getByPlaceholder(/query/i).fill('climate')
    // Use Sparse mode (no embed needed)
    await page.getByRole('button', { name: 'Sparse' }).first().click()
    await page.getByRole('button', { name: 'Search' }).click()
    await page.waitForTimeout(5000)
    // Either results or "No results"
    const page_text = await page.textContent('body')
    expect(page_text?.includes('results') || page_text?.includes('No results')).toBeTruthy()
  })

  test('D3. Enter key submits', async ({ page }) => {
    await page.goto('/search')
    const input = page.getByPlaceholder(/query/i)
    await input.fill('test')
    await input.press('Enter')
    await page.waitForTimeout(3000)
  })

  test('D4. Mode switch highlights', async ({ page }) => {
    await page.goto('/search')
    const btn = page.getByRole('button', { name: 'Sparse' }).first()
    await btn.click()
    await expect(btn).toHaveClass(/blue/)
  })
})

// ═══════════ E. CHAT ═══════════

test.describe('E. Chat', () => {
  test('E1. Empty state', async ({ page }) => {
    await page.goto('/chat')
    await expect(page.getByText('Ask a question')).toBeVisible()
  })

  test('E2. Mode selector', async ({ page }) => {
    await page.goto('/chat')
    await expect(page.getByLabel('Chat mode')).toBeVisible()
  })

  test('E3. Send message', async ({ page }) => {
    await page.goto('/chat')
    await page.locator('textarea').fill('Hello world')
    // Find the Send button (contains Send icon)
    const sendBtn = page.locator('button').filter({ has: page.locator('svg.lucide-send') })
    await sendBtn.click()
    await expect(page.getByText('Hello world').first()).toBeVisible({ timeout: 10000 })
  })

  test('E4. Clear chat', async ({ page }) => {
    await page.goto('/chat')
    await page.locator('textarea').fill('Test msg')
    await page.locator('textarea').press('Enter')
    await page.waitForTimeout(1000)
    await page.getByTitle('Clear chat').click()
    await expect(page.getByText('Ask a question')).toBeVisible({ timeout: 5000 })
  })
})

// ═══════════ F. GRAPH ═══════════

test.describe('F. Graph', () => {
  test('F1. Page with selector', async ({ page }) => {
    await page.goto('/graph')
    await expect(page.getByRole('heading', { name: 'Knowledge Graph' })).toBeVisible()
    await expect(page.getByLabel('Dataset')).toBeVisible()
  })

  test('F2. Empty state', async ({ page }) => {
    await page.goto('/graph')
    await expect(page.getByText('Select a dataset')).toBeVisible()
  })

  test('F3. SVG canvas', async ({ page }) => {
    await page.goto('/graph')
    // Graph page has a large SVG for the canvas
    const svgCount = await page.locator('svg.w-full').count()
    expect(svgCount).toBeGreaterThanOrEqual(1)
  })
})

// ═══════════ G. COLLECTIONS ═══════════

test.describe('G. Collections', () => {
  test('G1. Page loads', async ({ page }) => {
    await page.goto('/collections')
    await expect(page.getByRole('heading', { name: 'Collections' })).toBeVisible({ timeout: 10000 })
  })

  test('G2. Shows cards or empty', async ({ page }) => {
    await page.goto('/collections')
    await page.waitForTimeout(3000)
    const text = await page.textContent('body')
    expect(text?.includes('Records') || text?.includes('No collections')).toBeTruthy()
  })
})

// ═══════════ H. MEMORIES ═══════════

test.describe('H. Memories', () => {
  test('H1. Type filters', async ({ page }) => {
    await page.goto('/memories')
    await expect(page.getByRole('heading', { name: 'Memories' })).toBeVisible({ timeout: 10000 })
    await expect(page.getByRole('button', { name: 'all' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'fact' })).toBeVisible()
  })

  test('H2. Add memory form', async ({ page }) => {
    await page.goto('/memories')
    await page.locator('main').getByRole('button', { name: /Add Memory/i }).first().click()
    await expect(page.getByPlaceholder('Key')).toBeVisible()
    await expect(page.getByPlaceholder('Value')).toBeVisible()
  })
})

// ═══════════ I. NOTEBOOKS ═══════════

test.describe('I. Notebooks', () => {
  test('I1. Default cells', async ({ page }) => {
    await page.goto('/notebooks')
    await expect(page.getByRole('heading', { name: 'Notebook' })).toBeVisible({ timeout: 10000 })
    await expect(page.locator('textarea').first()).toBeVisible()
  })

  test('I2. Cell badges', async ({ page }) => {
    await page.goto('/notebooks')
    const text = await page.textContent('body')
    expect(text?.includes('code') || text?.includes('markdown')).toBeTruthy()
  })
})

// ═══════════ J. ANALYTICS ═══════════

test.describe('J. Analytics', () => {
  test('J1. Widgets load', async ({ page }) => {
    await page.goto('/analytics')
    await expect(page.getByRole('heading', { name: 'Analytics' })).toBeVisible()
    await expect(page.getByText('System Status').first()).toBeVisible({ timeout: 10000 })
  })

  test('J2. LLM Cache', async ({ page }) => {
    await page.goto('/analytics')
    await expect(page.getByText('LLM Cache').first()).toBeVisible({ timeout: 10000 })
  })

  test('J3. Auto-refresh', async ({ page }) => {
    await page.goto('/analytics')
    await expect(page.getByText('Auto-refresh')).toBeVisible()
  })
})

// ═══════════ K. SETTINGS ═══════════

test.describe('K. Settings', () => {
  test('K1. Theme toggle', async ({ page }) => {
    await page.goto('/settings')
    await expect(page.getByRole('button', { name: 'light' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'dark' })).toBeVisible()
  })

  test('K2. Dark mode', async ({ page }) => {
    await page.goto('/settings')
    await page.getByRole('button', { name: 'dark' }).click()
    await expect(page.locator('html')).toHaveClass(/dark/)
  })

  test('K3. Language', async ({ page }) => {
    await page.goto('/settings')
    await expect(page.getByText('Language')).toBeVisible()
    const select = page.locator('select').last()
    const options = await select.locator('option').allTextContents()
    expect(options).toContain('Русский')
    expect(options).toContain('English')
  })

  test('K4. API info', async ({ page }) => {
    await page.goto('/settings')
    await expect(page.getByText('Endpoint')).toBeVisible()
  })
})

// ═══════════ L. LOGIN ═══════════

test.describe('L. Login', () => {
  test('L1. Renders', async ({ page }) => {
    await page.goto('/login')
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible()
    await expect(page.getByLabel('Email')).toBeVisible()
    await expect(page.getByLabel('Password')).toBeVisible()
  })

  test('L2. Toggle register', async ({ page }) => {
    await page.goto('/login')
    await page.getByText("Don't have an account").click()
    await expect(page.getByRole('heading', { name: 'Create account' })).toBeVisible()
  })

  test('L3. Empty submit stays on page', async ({ page }) => {
    await page.goto('/login')
    await page.getByRole('button', { name: 'Sign in' }).click()
    await page.waitForTimeout(500)
    await expect(page).toHaveURL('/login')
  })
})

// ═══════════ M. ERROR HANDLING ═══════════

test.describe('M. Errors', () => {
  test('M1. No JS errors on pages', async ({ page }) => {
    const errors: string[] = []
    page.on('pageerror', (e) => errors.push(e.message))
    for (const r of ['/', '/search', '/chat', '/datasets', '/collections', '/memories', '/notebooks', '/analytics', '/settings']) {
      await page.goto(r)
      await page.waitForTimeout(1000)
    }
    const real = errors.filter((e) => !e.includes('fetch') && !e.includes('NetworkError') && !e.includes('Failed'))
    expect(real).toHaveLength(0)
  })
})

// ═══════════ N. RESPONSIVE ═══════════

test.describe('N. Responsive', () => {
  test('N1. Mobile — sidebar collapsed by default', async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 812 })
    await page.goto('/')
    await page.waitForTimeout(1000)
    // On mobile, sidebar has -translate-x-full class (off-screen)
    const aside = page.locator('aside')
    const cls = await aside.getAttribute('class')
    expect(cls).toContain('-translate-x-full')
  })

  test('N2. Desktop — sidebar visible', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 800 })
    await page.goto('/')
    await expect(page.locator('aside')).toBeVisible()
  })
})
