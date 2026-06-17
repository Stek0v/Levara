import type { Page } from '@playwright/test'

let cachedToken: string | null = null

export async function authenticate(page: Page) {
  if (!cachedToken) {
    const email = `e2e-${Date.now()}-${Math.random().toString(16).slice(2)}@example.com`
    const res = await page.request.post('/api/v1/auth/register', {
      data: { email, password: 'password123' },
    })
    if (!res.ok()) {
      throw new Error(`auth setup failed: ${res.status()} ${await res.text()}`)
    }
    const body = await res.json()
    cachedToken = body.access_token || body.token
    if (!cachedToken) throw new Error('auth setup failed: token missing')
  }

  await page.goto('/login')
  await page.evaluate((t) => localStorage.setItem('levara_token', t), cachedToken)
}
