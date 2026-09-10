import { test, expect, type Page } from '@playwright/test'

const methods = { password: true, registration: true, directory: true, oidc: true, saml: true }
const user = { id: 'enterprise-user', email: 'user@example.test' }

async function mockApp(page: Page) {
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    const body = path.endsWith('/settings') ? { locale: 'en', theme: 'light' }
      : path.endsWith('/auth/methods') ? methods : []
    await route.fulfill({ json: body })
  })
  await page.route('**/health**', route => route.fulfill({ json: { status: 'ok' } }))
}

test('valid HttpOnly cookie takes priority over stale stored bearer', async ({ page, context, baseURL }) => {
  await mockApp(page)
  await context.addCookies([{ name: 'auth_token', value: 'valid-cookie', url: baseURL!, httpOnly: true, sameSite: 'Lax' }])
  await page.addInitScript(() => localStorage.setItem('levara_token', 'stale-token'))
  let cookieProbes = 0
  await page.route('**/api/v1/auth/me', route => {
    const headers = route.request().headers()
    const ok = !headers.authorization && headers.cookie?.includes('auth_token=valid-cookie')
    if (ok) cookieProbes++
    return route.fulfill({ status: ok ? 200 : 401, json: ok ? user : { error: 'invalid token' } })
  })
  await page.goto('/datasets')
  await expect(page).toHaveURL(/\/datasets$/)
  await expect.poll(() => cookieProbes).toBeGreaterThan(0)
  await expect(page.getByRole('button', { name: 'Log out' })).toBeVisible()
  expect(await page.evaluate(() => localStorage.getItem('levara_token'))).toBeNull()
})

test('configured directory login submits username and reports denied credentials', async ({ page }) => {
  await mockApp(page)
  await page.route('**/api/v1/auth/directory/login', route => {
    expect(route.request().postDataJSON()).toEqual({ username: 'DOMAIN\\alice', password: 'incorrect' })
    expect(route.request().headers().authorization).toBeUndefined()
    return route.fulfill({ status: 401, json: { error: 'Directory sign-in denied' } })
  })
  await page.goto('/login')
  await page.getByRole('button', { name: 'Directory account' }).click()
  await page.getByLabel('Username').fill('DOMAIN\\alice')
  await page.getByLabel('Password').fill('incorrect')
  await page.locator('form').getByRole('button', { name: 'Sign in' }).click()
  await expect(page.getByText('Directory sign-in denied')).toBeVisible()
  await expect(page).toHaveURL(/\/login$/)
})

test('disabled providers are not offered, and discovery failures can be retried', async ({ page }) => {
  await mockApp(page)
  let offline = true
  await page.route('**/api/v1/auth/methods', route => offline
    ? route.abort('failed')
    : route.fulfill({ json: { ...methods, directory: false, oidc: false, saml: false } }))
  await page.goto('/login')
  await expect(page.getByText('Could not verify the server connection. Please try again.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Continue with OIDC' })).toHaveCount(0)
  offline = false
  await page.getByRole('button', { name: 'Retry' }).click()
  await expect(page.getByText('Could not verify the server connection. Please try again.')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Directory account' })).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Continue with SAML' })).toHaveCount(0)
  await expect(page.getByLabel('Email')).toBeVisible()
})

test('directory success verifies the HttpOnly session and returns to the requested page', async ({ page }) => {
  await mockApp(page)
  await page.route('**/api/v1/auth/me', route => {
    const ok = route.request().headers().cookie?.includes('auth_token=directory-session')
    return route.fulfill({ status: ok ? 200 : 401, json: ok ? user : { error: 'unauthenticated' } })
  })
  await page.route('**/api/v1/auth/directory/login', route => route.fulfill({
    json: { access_token: 'directory-session' },
    headers: { 'set-cookie': 'auth_token=directory-session; Path=/; HttpOnly; SameSite=Lax' },
  }))
  await page.goto('/login?next=%2Fdatasets%3Fview%3Dshared')
  await page.getByRole('button', { name: 'Directory account' }).click()
  await page.getByLabel('Username').fill('alice')
  await page.getByLabel('Password').fill('correct')
  await page.locator('form').getByRole('button', { name: 'Sign in' }).click()
  await expect(page).toHaveURL(/\/datasets\?view=shared$/)
  await expect(page.getByRole('button', { name: 'Log out' })).toBeVisible()
  expect(await page.evaluate(() => localStorage.getItem('levara_token'))).toBeNull()
  expect(await page.evaluate(() => document.cookie)).not.toContain('auth_token')
})

for (const method of ['OIDC', 'SAML'] as const) {
  test(`${method} navigation adopts a cookie and consumes only a local next path`, async ({ page }) => {
    await mockApp(page)
    const path = method === 'OIDC' ? '/api/v1/auth/oidc/login' : '/api/v1/saml/login'
    await page.route('**/api/v1/auth/me', route => {
      const ok = route.request().headers().cookie?.includes('auth_token=sso-session')
      if (ok) expect(route.request().headers().authorization).toBeUndefined()
      return route.fulfill({ status: ok ? 200 : 401, json: ok ? user : { error: 'unauthenticated' } })
    })
    await page.route('**' + path, route => {
      return route.fulfill({ status: 303, headers: { location: '/', 'set-cookie': 'auth_token=sso-session; Path=/; HttpOnly; SameSite=Lax' } })
    })
    await page.goto('/login?next=%2Fdatasets%3Fview%3Dshared')
    await page.evaluate(() => localStorage.setItem('levara_token', 'stale-before-sso'))
    await page.getByRole('button', { name: `Continue with ${method}` }).click()
    await expect(page).toHaveURL(/\/datasets\?view=shared$/)
    await expect(page.getByRole('button', { name: 'Log out' })).toBeVisible()
    expect(await page.evaluate(() => sessionStorage.getItem('levara_sso_next'))).toBeNull()
    expect(await page.evaluate(() => localStorage.getItem('levara_token'))).toBeNull()
  })
}

test('SSO next cannot navigate to a foreign origin', async ({ page }) => {
  await mockApp(page)
  await page.route('**/api/v1/auth/oidc/login', route => {
    return route.fulfill({ status: 401, body: 'Unauthorized' })
  })
  await page.goto('/login?next=' + encodeURIComponent('/\\attacker.example'))
  await page.getByRole('button', { name: 'Continue with OIDC' }).click()
  await expect(page).toHaveURL(/\/api\/v1\/auth\/oidc\/login$/)
  await expect(page.getByText('Unauthorized')).toBeVisible()
  expect(await page.evaluate(() => sessionStorage.getItem('levara_sso_next'))).toBe('/')
  // Callback failures remain opaque HTTP 401; browser back returns to retry.
  await page.goBack()
  await expect(page.getByRole('button', { name: 'Continue with OIDC' })).toBeVisible()
  expect(await page.evaluate(() => localStorage.getItem('levara_token'))).toBeNull()
})

test('logout confirms server revocation, exposes offline failure and then retries', async ({ page, context, baseURL }) => {
  await mockApp(page)
  await context.addCookies([{ name: 'auth_token', value: 'session', url: baseURL!, httpOnly: true, sameSite: 'Lax' }])
  await page.route('**/api/v1/auth/me', route => route.fulfill({ json: user }))
  let offline = true
  let revoked = false
  await page.route('**/api/v1/auth/logout', route => {
    expect(route.request().method()).toBe('POST')
    expect(route.request().headers().authorization).toBeUndefined()
    expect(route.request().headers().origin).toBe(baseURL)
    expect(route.request().headers().cookie).toContain('auth_token=session')
    if (offline) return route.abort('failed')
    revoked = true
    return route.fulfill({ status: 204, headers: { 'set-cookie': 'auth_token=; Path=/; Max-Age=0; HttpOnly; SameSite=Lax' } })
  })
  await page.goto('/datasets')
  await page.getByRole('button', { name: 'Expand sidebar' }).click()
  await page.getByRole('button', { name: 'Log out' }).click()
  await expect(page.getByText('Could not end the session. Please retry logout.')).toBeVisible()
  await expect(page).toHaveURL(/\/datasets$/)
  offline = false
  await page.getByRole('button', { name: 'Log out' }).click()
  await expect(page).toHaveURL(/\/login$/)
  expect(revoked).toBe(true)
  expect((await context.cookies()).some(cookie => cookie.name === 'auth_token')).toBe(false)
})

test('logout of an expired session ends the browser state on 401', async ({ page }) => {
  await mockApp(page)
  await page.route('**/api/v1/auth/me', route => route.fulfill({ json: user }))
  await page.route('**/api/v1/auth/logout', route => route.fulfill({ status: 401, body: 'Unauthorized' }))
  await page.goto('/datasets')
  await page.getByRole('button', { name: 'Expand sidebar' }).click()
  await page.getByRole('button', { name: 'Log out' }).click()
  await expect(page).toHaveURL(/\/login$/)
})

test('a stored bearer remains compatible only when there is no valid cookie', async ({ page }) => {
  await mockApp(page)
  await page.addInitScript(() => localStorage.setItem('levara_token', 'legacy-bearer'))
  let bearerProbes = 0
  await page.route('**/api/v1/auth/me', route => {
    const ok = route.request().headers().authorization === 'Bearer legacy-bearer'
    if (ok) bearerProbes++
    return route.fulfill({ status: ok ? 200 : 401, json: ok ? user : { error: 'missing cookie' } })
  })
  await page.route('**/api/v1/datasets*', route => {
    expect(route.request().headers().authorization).toBe('Bearer legacy-bearer')
    return route.fulfill({ json: [] })
  })
  await page.goto('/datasets')
  await expect(page.getByRole('button', { name: 'Log out' })).toBeVisible()
  expect(bearerProbes).toBeGreaterThan(0)
})

test('successful login does not reuse a still-pending anonymous session probe', async ({ page }) => {
  await mockApp(page)
  let releaseOld!: () => void
  const oldResponse = new Promise<void>(resolve => { releaseOld = resolve })
  let firstProbe = true
  await page.route('**/api/v1/auth/me', async route => {
    if (firstProbe) {
      firstProbe = false
      await oldResponse
      // A completed login performs a full navigation, aborting this old request.
      await route.fulfill({ status: 401, json: { error: 'old anonymous response' } }).catch(() => {})
      return
    }
    expect(route.request().headers().authorization).toBeUndefined()
    expect(route.request().headers().cookie).toContain('auth_token=new-session')
    await route.fulfill({ json: user })
  })
  await page.route('**/api/v1/auth/login', route => route.fulfill({
    json: { access_token: 'new-session' },
    headers: { 'set-cookie': 'auth_token=new-session; Path=/; HttpOnly; SameSite=Lax' },
  }))
  try {
    await page.goto('/login?next=%2Fdatasets')
    await expect.poll(() => firstProbe).toBe(false)
    await page.getByLabel('Email').fill('user@example.test')
    await page.getByLabel('Password').fill('password')
    await page.locator('form').getByRole('button', { name: 'Sign in' }).click()
    await expect(page).toHaveURL(/\/datasets$/)
    await expect(page.getByRole('button', { name: 'Log out' })).toBeVisible()
    expect(await page.evaluate(() => localStorage.getItem('levara_token'))).toBeNull()
  } finally {
    releaseOld()
    await page.unrouteAll({ behavior: 'wait' })
  }
})
