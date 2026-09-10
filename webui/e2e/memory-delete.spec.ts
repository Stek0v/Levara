import { test, expect } from '@playwright/test'

test.describe('Memory deletion identity', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', route => route.fulfill({
		status: 200,
		contentType: 'application/json',
		body: JSON.stringify({ id: 'alice', email: 'alice@test.local' }),
	}))
  })

  test('sends the selected ID once and keeps the same-key sibling', async ({ page }) => {
	const selectedID = 'id one/+%'
	let memories = [
	  { id: selectedID, key: 'duplicate', value: 'selected', type: 'fact' },
	  { id: 'id-two', key: 'duplicate', value: 'sibling', type: 'fact' },
	]
	let deleteRequests = 0
	let requestedPath = ''
	await page.route('**/api/v1/memories/by-id/**', async route => {
	  deleteRequests++
	  requestedPath = new URL(route.request().url()).pathname
	  await new Promise(resolve => setTimeout(resolve, 100))
	  memories = memories.filter(memory => memory.id !== selectedID)
	  await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ deleted: true, id: selectedID, key: 'duplicate' }) })
	})
	await page.route('**/api/v1/memories', route => route.fulfill({
	  status: 200,
	  contentType: 'application/json',
	  body: JSON.stringify(memories),
	}))

	await page.goto('/memories')
	const selectedDelete = page.getByText('selected').locator('..').getByRole('button')
	await selectedDelete.evaluate((button: HTMLButtonElement) => {
	  button.click()
	  button.click()
	})
	await expect(page.getByText('selected')).toHaveCount(0)
	await expect(page.getByText('sibling')).toBeVisible()
	expect(deleteRequests).toBe(1)
	expect(requestedPath).toContain('/api/v1/memories/by-id/id%20one%2F%2B%25')
  })

  test('disables legacy rows without IDs and keeps a rejected card visible', async ({ page }) => {
	await page.route('**/api/v1/memories/by-id/shared-id', route => route.fulfill({
	  status: 403,
	  contentType: 'application/json',
	  body: JSON.stringify({ detail: 'memory delete forbidden' }),
	}))
	await page.route('**/api/v1/memories', route => route.fulfill({
	  status: 200,
	  contentType: 'application/json',
	  body: JSON.stringify([
		{ key: 'legacy', value: 'missing durable ID', type: 'fact' },
		{ id: 'shared-id', key: 'shared', value: 'read only', type: 'fact' },
	  ]),
	}))

	await page.goto('/memories')
	await expect(page.getByText('missing durable ID').locator('..').getByRole('button')).toBeDisabled()
	await page.getByText('read only').locator('..').getByRole('button').click()
	await expect(page.getByText('memory delete forbidden', { exact: true })).toBeVisible()
	await expect(page.getByText('read only')).toBeVisible()
  })
})
