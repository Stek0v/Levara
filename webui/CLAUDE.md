@AGENTS.md

# Levara WebUI

Next.js 15 (App Router) + TypeScript + Tailwind CSS.
Backend API proxied via `next.config.ts` rewrites to `localhost:8080`.

## Dev

```bash
npm run dev -- -p 3001    # start dev server
npm run build             # production build
npx playwright test       # run 61 E2E tests
```

## Structure

- `src/app/(dashboard)/` — all authenticated pages (sidebar layout)
- `src/app/(auth)/login/` — login/register page (no sidebar)
- `src/components/ui/` — reusable components (Button, Input, Badge, Toast, Modal, etc.)
- `src/components/layout/` — Sidebar navigation
- `src/hooks/use-levara.ts` — React Query hooks (shared cache, auto-invalidation)
- `src/hooks/use-sse.ts` — SSE client with auto-reconnect
- `src/lib/api.ts` — Levara API client (typed SDK, error handling, traceId)
- `e2e/` — Playwright E2E tests

## Key patterns

- ALL data fetching through React Query hooks (useDatasets, useCollections, etc.)
- Mutations invalidate related queries automatically
- Upload → auto-cognify → BM25 + vector indexed
- Collection selector in Chat/Search (filter out internal Triplet_text, _community_summaries)
- Theme/locale persist in localStorage
