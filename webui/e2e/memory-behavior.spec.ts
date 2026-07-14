import { test, expect } from '@playwright/test'

test.describe('Memory Behavior dashboard', () => {
  test.beforeEach(async ({ page }) => {
    await page.route('**/api/v1/auth/me', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ id: 'u1', email: 'u@test.local', username: 'u' }),
      }),
    )
  })

  test('renders metrics and trajectory rows from API', async ({ page }) => {
    await page.route('**/api/v1/memory-behavior*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          window_hours: 24,
          collection: 'levara',
          summary: {
            total_trajectories: 2,
            total_events: 5,
            memory_ops: 4,
            recall_before_save_rate: 0.75,
            repeat_save_rate: 0.25,
            zero_result_rate: 0.2,
            empty_recall_rate: 0.5,
            memory_ops_per_trajectory: 2,
            context_bytes_per_trajectory: 2048,
            save_without_room_or_hall_count: 1,
            unknown_hall_error_count: 1,
            tool_errors_by_tool: { save_memory: 1 },
            problem_trajectories: [{
              id: 'trace:bad',
              collection: 'levara',
              client_name: 'codex',
              repeat_saves: 1,
              blind_saves: 1,
              zero_results: 1,
              errors: 0,
              context_bytes: 4096,
              memory_ops: 3,
            }],
          },
        }),
      }),
    )
    await page.route('**/api/v1/agent-trajectories*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          window_hours: 24,
          limit: 20,
          offset: 0,
          total: 1,
          trajectories: [{
            id: 'trace:good',
            started_at: '2026-07-14T00:00:00Z',
            ended_at: '2026-07-14T00:00:01Z',
            duration_ms: 1000,
            client_name: 'codex',
            toolset: 'memory',
            collection: 'levara',
            event_count: 2,
            counters: {
              search_count: 0,
              recall_count: 1,
              save_count: 1,
              zero_result_count: 0,
              error_count: 0,
              request_bytes: 100,
              response_bytes: 300,
            },
          }],
        }),
      }),
    )

    await page.goto('/memory-behavior?hours=24&collection=levara&client=codex')

    await expect(page.getByRole('heading', { name: 'Memory Behavior' })).toBeVisible()
    await expect(page.getByText('75.0%')).toBeVisible()
    await expect(page.getByText('25.0%')).toBeVisible()
    await expect(page.getByText('trace:good')).toBeVisible()
    await expect(page.getByText('trace:bad')).toBeVisible()
  })

  test('preserves filters in URL query params', async ({ page }) => {
    await page.route('**/api/v1/memory-behavior*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ window_hours: 1, summary: { total_trajectories: 0, total_events: 0, memory_ops: 0, recall_before_save_rate: 0, repeat_save_rate: 0, zero_result_rate: 0, empty_recall_rate: 0, memory_ops_per_trajectory: 0, context_bytes_per_trajectory: 0, save_without_room_or_hall_count: 0, unknown_hall_error_count: 0, tool_errors_by_tool: {}, problem_trajectories: [] } }),
      }),
    )
    await page.route('**/api/v1/agent-trajectories*', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ window_hours: 1, limit: 20, offset: 0, total: 0, trajectories: [] }),
      }),
    )

    await page.goto('/memory-behavior')
    await page.getByPlaceholder('all collections').fill('wb')
    await page.getByPlaceholder('all clients').fill('codex')
    await page.getByRole('button', { name: 'Apply' }).click()

    await expect(page).toHaveURL(/collection=wb/)
    await expect(page).toHaveURL(/client=codex/)
  })
})
