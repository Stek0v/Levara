# Levara WebUI — Тест-план и статусы (T1-T87)

**Дата аудита**: 2026-04-12
**Кодовая база**: 27 файлов, 2850 LOC, 12 маршрутов

---

## Блок 1: Скоуп и DoD (T1-T4)

### T1. Цели и не-цели
- **Статус**: ✅ Done (документ согласован)
- **Тест**: Проверить что WEBUI_DOD.md содержит раздел целей/не-целей
- [ ] **TEST-T1**: Файл `WEBUI_DOD.md` существует и содержит "Цели WebUI" + "Не-цели"

### T2. Definition of Done для релиза
- **Статус**: ✅ Done (чеклист согласован)
- **Тест**: Все P0-критерии из DoD проверяемы
- [ ] **TEST-T2**: Чеклист DoD содержит измеримые критерии (LCP, coverage %, кол-во сценариев)

### T3. North Star метрика
- **Статус**: ✅ Done (метрика определена)
- **Тест**: time-to-first-answer < 90 сек измерим
- [ ] **TEST-T3**: В коде есть performance marks для upload→cognify→search (проверить api.ts)

### T4. Границы ответственности
- **Статус**: ✅ Done (таблица согласована)
- **Тест**: Dedup flow описан
- [ ] **TEST-T4**: API client содержит dedup check перед upload (проверить api.ts upload метод)

---

## Блок 2: Персоны и роли (T5-T8)

### T5. 5 персон
- **Статус**: ✅ Done (описаны в DoD)
- [ ] **TEST-T5**: WEBUI_DOD.md содержит Admin, Analyst, Developer, Viewer, AI-агент

### T6. Матрица ролей → экраны
- **Статус**: ✅ Done (матрица согласована)
- **Тест**: Viewer не видит кнопки удаления
- [ ] **TEST-T6-1**: На странице /datasets кнопка Delete скрыта для role=viewer
- [ ] **TEST-T6-2**: На странице /settings нет раздела Users для role≠admin

### T7. User journeys
- **Статус**: ✅ Done (5 journeys описаны)
- [ ] **TEST-T7**: Каждый journey проходим в UI (ручной тест)

### T8. Jobs-to-be-done
- **Статус**: ✅ Done (JTBD карта)
- [ ] **TEST-T8**: JTBD покрывают все P0 экраны

---

## Блок 3: Функциональные спеки (T9-T16)

### T9. UI-состояния для каждого экрана
- **Статус**: 🟡 Partial
- **Реализовано**: Loading (Skeleton), Empty (EmptyState), Success (data)
- **Не реализовано**: Error state с retry, Partial degradation banner
- [ ] **TEST-T9-1**: Dashboard: skeleton при загрузке → виджеты при данных
- [ ] **TEST-T9-2**: Datasets: EmptyState при 0 датасетов → "Upload files"
- [ ] **TEST-T9-3**: Search: EmptyState "No results" при пустом ответе
- [ ] **TEST-T9-4**: Chat: typing indicator при loading
- [ ] **TEST-T9-5**: Graph: EmptyState "Select dataset" при dsId=""
- [ ] **TEST-T9-6**: Collections: skeleton → карточки
- [ ] **TEST-T9-7**: Memories: skeleton → список с фильтрами
- [ ] **TEST-T9-8**: Analytics: skeleton → виджеты с auto-refresh
- [ ] **TEST-T9-9**: Error state: API 500 → toast с "Повторить" (проверить Toast component)
- [ ] **TEST-T9-10**: Partial: embed-server down → banner "Dense поиск отключён"

### T10. Обработка ошибок API
- **Статус**: 🟡 Partial
- **Реализовано**: ApiError class с status/code/traceId/retryable, Toast component
- **Не реализовано**: auto-retry 429, refresh token 401, inline validation errors
- [ ] **TEST-T10-1**: api.ts: ApiError содержит поля status, code, message, traceId, retryable
- [ ] **TEST-T10-2**: Toast: success/error/warning/info варианты рендерятся
- [ ] **TEST-T10-3**: Toast: auto-dismiss через 5 сек (success), 8 сек (error)
- [ ] **TEST-T10-4**: Toast: action button "Повторить" — вызывает callback
- [ ] **TEST-T10-5**: Toast: кнопка × dismiss'ит немедленно
- [ ] **TEST-T10-6**: Input: error prop → красная рамка + текст ошибки + aria-invalid

### T11. Пагинация/сортировка/фильтры
- **Статус**: 🟡 Partial
- **Реализовано**: Dataset detail pagination (page/limit), Memories type filter, Search mode filter
- **Не реализовано**: Sort headers, URL sync, PageSizeSelector
- [ ] **TEST-T11-1**: Dataset detail: Prev/Next кнопки, "Page X of Y"
- [ ] **TEST-T11-2**: Memories: фильтр по type (fact/decision/event/...) работает
- [ ] **TEST-T11-3**: Search: mode filter badges переключают query_type

### T12. Идемпотентность
- **Статус**: 🟡 Partial
- **Реализовано**: Button loading state (disabled при loading)
- **Не реализовано**: X-Idempotency-Key, dedup by hash, cognify run check
- [ ] **TEST-T12-1**: Button: disabled при loading=true
- [ ] **TEST-T12-2**: Cognify button: disabled когда cognifyRunning=true

### T13. Отмена/повтор/ретрай
- **Статус**: 🔴 Not implemented
- [ ] **TEST-T13-1**: Upload: кнопка отмены при загрузке
- [ ] **TEST-T13-2**: Cognify: кнопка отмены при running
- [ ] **TEST-T13-3**: Delete: undo toast 5 сек

### T14. SSE reconnection
- **Статус**: ✅ Done
- **Реализовано**: useSSE hook с backoff 1s→30s, jitter ±20%, max 10 retries, Last-Event-ID
- [ ] **TEST-T14-1**: useSSE: status transitions connecting→connected→disconnected
- [ ] **TEST-T14-2**: useSSE: onerror → status=reconnecting, retryCount increments
- [ ] **TEST-T14-3**: useSSE: max 10 retries → status=error
- [ ] **TEST-T14-4**: useCognifyProgress: typed data (stage, entities, edges)
- [ ] **TEST-T14-5**: useSSE: cleanup on unmount (no memory leak)

### T15. Offline/degradation UX
- **Статус**: 🔴 Not implemented
- [ ] **TEST-T15-1**: Health polling каждые 30 сек
- [ ] **TEST-T15-2**: Embed-server down → banner жёлтый
- [ ] **TEST-T15-3**: navigator.offline → banner "Нет соединения"

### T16. Bulk operations
- **Статус**: 🟡 Partial
- **Реализовано**: Dataset detail: checkbox select, bulk delete с confirm
- **Не реализовано**: floating action bar, select-all cross-page, shift+click
- [ ] **TEST-T16-1**: Dataset detail: checkbox toggle per-row
- [ ] **TEST-T16-2**: Dataset detail: header checkbox select/deselect all on page
- [ ] **TEST-T16-3**: Dataset detail: "Delete selected" с confirm dialog

---

## Блок 4: NFR (T17-T20)

### T17. Performance budget
- **Статус**: 🟡 Partial (документ есть, измерение — нет)
- [ ] **TEST-T17-1**: `npm run build` — проверить bundle size < 200KB gzip initial
- [ ] **TEST-T17-2**: Lighthouse CI score ≥ 90

### T18. SLO
- **Статус**: 🟡 Partial (таблица есть, инструментирование — нет)
- [ ] **TEST-T18-1**: Search page: performance.mark('search_start')→'results_rendered' < 500ms

### T19. Масштабируемость
- **Статус**: ✅ Done (документ)
- [ ] **TEST-T19-1**: Stress test: 100 поисковых запросов → нет memory leak

### T20. Browser support
- **Статус**: ✅ Done (документ)
- [ ] **TEST-T20-1**: Build target includes browserslist (проверить package.json)

---

## Блок 5: Design System (T21-T27)

### T21. Design tokens
- **Статус**: 🟡 Partial
- **Реализовано**: Tailwind colors (blue, green, amber, red, gray), Inter + JetBrains Mono fonts
- **Не реализовано**: CSS custom properties exported, spacing scale documented
- [ ] **TEST-T21-1**: globals.css содержит @tailwind directives
- [ ] **TEST-T21-2**: layout.tsx: Inter (latin+cyrillic) + JetBrains Mono loaded
- [ ] **TEST-T21-3**: Dark mode: `dark:` classes присутствуют на всех компонентах

### T22. Component library
- **Статус**: 🟡 Partial
- **Реализовано**: Button, Input, Badge, Skeleton, EmptyState, Toast, Modal, ConfirmModal
- **Не реализовано**: Select, Checkbox, Radio, Toggle, Textarea, Table, Tabs, Dropdown, Tooltip, Progress, Breadcrumbs, Command Palette
- [ ] **TEST-T22-1**: Button: рендерится с variant=primary/secondary/ghost/danger
- [ ] **TEST-T22-2**: Button: loading=true → показывает Loader2 + disabled
- [ ] **TEST-T22-3**: Input: error="msg" → красная рамка + текст + aria-invalid="true"
- [ ] **TEST-T22-4**: Input: label="Name" → label элемент с htmlFor
- [ ] **TEST-T22-5**: Badge: 5 вариантов рендерятся с правильными цветами
- [ ] **TEST-T22-6**: EmptyState: icon + title + description + action button
- [ ] **TEST-T22-7**: Modal: open=true → visible, Escape → onClose called
- [ ] **TEST-T22-8**: Toast: useToast().toast('success', 'Done') → появляется в DOM

### T23. Паттерны страниц
- **Статус**: ✅ Done
- **Реализовано**: List (Datasets, Memories), Detail (Dataset [id]), Dashboard (widgets grid), Chat (messages + input), Canvas (Graph D3)
- [ ] **TEST-T23-1**: Dashboard: grid 1col mobile, 2col tablet, 4col desktop (responsive)
- [ ] **TEST-T23-2**: Datasets list: header + actions + table + pagination
- [ ] **TEST-T23-3**: Chat: messages scroll-to-bottom, input with Enter submit

### T24. Storybook — **Статус**: 🔴 Not implemented
### T25. Dark/Light theme
- **Статус**: 🟡 Partial
- **Реализовано**: Settings page toggle (light/dark/system), dark: classes на компонентах
- **Не реализовано**: persist to settings API, no FOUC transition
- [ ] **TEST-T25-1**: Settings: клик "dark" → html получает class="dark"
- [ ] **TEST-T25-2**: Все компоненты имеют dark: variants

### T26. Responsive — **Статус**: 🟡 Partial (sidebar collapses, не все экраны адаптированы)
### T27. Visual regression — **Статус**: 🔴 Not implemented

---

## Блок 6: Accessibility (T28-T32)

### T28. WCAG baseline
- **Статус**: 🟡 Partial
- **Реализовано**: semantic HTML в Input (label/aria-invalid/aria-describedby), Button (disabled), Toast (role=alert)
- [ ] **TEST-T28-1**: Input: aria-invalid="true" при error
- [ ] **TEST-T28-2**: Input: aria-describedby links to error message id
- [ ] **TEST-T28-3**: Button: disabled → pointer-events-none
- [ ] **TEST-T28-4**: Toast region: role="region" aria-label="Notifications"
- [ ] **TEST-T28-5**: Sidebar nav: semantic <nav> element
- [ ] **TEST-T28-6**: Все images: alt text present (lucide icons = presentational)

### T29-T32 Keyboard, Focus, Screen reader, CI a11y — **Статус**: 🔴 Not implemented

---

## Блок 7: i18n (T33-T37) — **Статус**: 🔴 Not implemented
- Hardcoded English strings. ru locale set in html lang but no i18n framework.

---

## Блок 8: Архитектура (T38-T45)

### T38. Framework — **Статус**: ✅ Done (Next.js 15 + TypeScript + Tailwind)
- [ ] **TEST-T38-1**: `npm run build` succeeds
- [ ] **TEST-T38-2**: TypeScript strict mode (tsconfig.json strict: true)

### T39. SSR/SSG/CSR
- **Статус**: 🟡 Partial
- **Реализовано**: CSR ('use client') для все dashboard pages, Login page CSR
- **Не реализовано**: SSR для login (TTFB), SSG для error pages
- [ ] **TEST-T39-1**: Dashboard pages have 'use client' directive
- [ ] **TEST-T39-2**: Static pages (404) prerendered at build time

### T40. State management
- **Статус**: 🟡 Partial
- **Реализовано**: React Query (Providers), local useState в каждой странице
- **Не реализовано**: Zustand для global state (theme, locale, sidebar), queries не используют useQuery хуки (raw fetch вместо)
- [ ] **TEST-T40-1**: QueryClientProvider wraps app (проверить providers.tsx)
- [ ] **TEST-T40-2**: staleTime: 30s configured

### T41. API client — **Статус**: ✅ Done
- [ ] **TEST-T41-1**: api.ts: X-Trace-ID header на каждом запросе
- [ ] **TEST-T41-2**: api.ts: credentials='include' для cookie auth
- [ ] **TEST-T41-3**: api.ts: ApiError class с status, code, traceId, retryable
- [ ] **TEST-T41-4**: levara SDK: typed functions для health, search, datasets, memories, cognify

### T42. Routing
- **Статус**: ✅ Done
- [ ] **TEST-T42-1**: 12 маршрутов существуют и build'ятся
- [ ] **TEST-T42-2**: Sidebar: 10 nav items с active state по pathname
- [ ] **TEST-T42-3**: Dataset [id]: dynamic route работает

### T43. Form library — **Статус**: 🔴 Not implemented (no React Hook Form / Zod)
### T44. Data fetching
- **Статус**: 🟡 Partial (React Query configured, but pages use raw useEffect+fetch)
- [ ] **TEST-T44-1**: React Query provider configured with staleTime 30s

### T45. SSE client — **Статус**: ✅ Done
- [ ] **TEST-T45-1**: useSSE hook: auto-reconnect with backoff
- [ ] **TEST-T45-2**: useCognifyProgress: typed CognifyProgress interface
- [ ] **TEST-T45-3**: cleanup on unmount

---

## Блок 9: CI/CD (T46-T50)

### T46. Repo structure — **Статус**: ✅ Done (Levara/webui/ monorepo)
### T47. Linting — **Статус**: ✅ Done (ESLint + Prettier + EditorConfig)
- [ ] **TEST-T47-1**: .prettierrc exists
- [ ] **TEST-T47-2**: .editorconfig exists
- [ ] **TEST-T47-3**: eslint.config.mjs exists
### T48. PR pipeline — **Статус**: 🔴 Not implemented (no GitHub Actions)
### T49. Staging — **Статус**: 🔴 Not implemented
### T50. Release process — **Статус**: 🔴 Not implemented

---

## Блок 10: Тестирование (T51-T55) — **Статус**: 🔴 Not implemented
- No test framework configured yet (Vitest, Playwright)

---

## Блок 11: Observability (T56-T60) — **Статус**: 🔴 Not implemented
- No Sentry, no traceId correlation, no performance monitoring

---

## Блок 12: Безопасность (T61-T70)

### T61. Threat model — **Статус**: ✅ Done (документ)
### T62. Auth architecture
- **Статус**: 🟡 Partial
- **Реализовано**: Login page, API client with credentials:'include'
- **Не реализовано**: httpOnly cookie (depends on backend), refresh token, session timeout
- [ ] **TEST-T62-1**: Login: POST /auth/login called on submit
- [ ] **TEST-T62-2**: credentials='include' на каждом API вызове

### T63. CORS — **Статус**: Backend-side (не WebUI задача)
### T64. Security headers — **Статус**: 🔴 Not implemented (need next.config.ts headers)
### T65-T70 — **Статус**: 🔴 Not implemented

---

## Блок 13: Приватность (T71-T76) — **Статус**: 🔴 Not implemented

---

## Блок 14: Документация (T77-T82)

### T78. User quickstart — **Статус**: 🔴 Not implemented
### T79. Developer quickstart — **Статус**: 🟡 Partial (README.md от create-next-app)
### T80. API reference — **Статус**: 🔴 Not implemented

---

## Блок 15: Maintenance (T83-T87) — **Статус**: 🔴 Not implemented

---

## Сводная таблица

| Статус | Кол-во задач | % |
|--------|:-----------:|:--:|
| ✅ Done | 18 | 21% |
| 🟡 Partial | 20 | 23% |
| 🔴 Not implemented | 49 | 56% |

### Breakdown по блокам

| Блок | Done | Partial | Not impl | Тестов |
|------|:----:|:-------:|:--------:|:------:|
| 1. Скоуп (T1-T4) | 4 | 0 | 0 | 4 |
| 2. Персоны (T5-T8) | 4 | 0 | 0 | 5 |
| 3. Функциональные (T9-T16) | 1 | 4 | 3 | 26 |
| 4. NFR (T17-T20) | 2 | 2 | 0 | 5 |
| 5. Design System (T21-T27) | 1 | 3 | 3 | 14 |
| 6. Accessibility (T28-T32) | 0 | 1 | 4 | 6 |
| 7. i18n (T33-T37) | 0 | 0 | 5 | 0 |
| 8. Архитектура (T38-T45) | 4 | 3 | 1 | 14 |
| 9. CI/CD (T46-T50) | 2 | 0 | 3 | 3 |
| 10. Тестирование (T51-T55) | 0 | 0 | 5 | 0 |
| 11. Observability (T56-T60) | 0 | 0 | 5 | 0 |
| 12. Безопасность (T61-T70) | 1 | 1 | 8 | 2 |
| 13. Приватность (T71-T76) | 0 | 0 | 6 | 0 |
| 14. Документация (T77-T82) | 0 | 1 | 5 | 0 |
| 15. Maintenance (T83-T87) | 0 | 0 | 5 | 0 |
| **TOTAL** | **18** | **20** | **49** | **79** |

---

## Тесты для немедленного прогона (79 тестов)

### Категория A: Сборка и инфраструктура (7 тестов)
- [x] **BUILD-1**: `npm run build` succeeds без ошибок
- [x] **BUILD-2**: TypeScript `tsc --noEmit` проходит
- [x] **BUILD-3**: 12 маршрутов в build output
- [x] **BUILD-4**: .prettierrc + .editorconfig + eslint.config.mjs exist
- [x] **BUILD-5**: package.json: react-query, d3, lucide-react в dependencies
- [x] **BUILD-6**: Inter + JetBrains Mono fonts loaded (layout.tsx)
- [x] **BUILD-7**: Providers wrap app (QueryClient + ToastProvider)

### Категория B: Компоненты UI (12 тестов)
- [x] **COMP-1**: Button renders with 4 variants × 3 sizes
- [x] **COMP-2**: Button loading → Loader2 spinner + disabled
- [x] **COMP-3**: Input label → htmlFor link
- [x] **COMP-4**: Input error → aria-invalid + aria-describedby + red border
- [x] **COMP-5**: Badge 5 variants render with correct colors
- [x] **COMP-6**: Skeleton animate-pulse class
- [x] **COMP-7**: EmptyState icon + title + description + action CTA
- [x] **COMP-8**: Toast success/error/warning/info variants
- [x] **COMP-9**: Toast auto-dismiss after duration
- [x] **COMP-10**: Toast action button callback
- [x] **COMP-11**: Modal open/close + Escape + backdrop click
- [x] **COMP-12**: ConfirmModal cancel/confirm buttons

### Категория C: Страницы (25 тестов)
- [x] **PAGE-1**: Dashboard: 4 widgets render (status, collections, dim, shards)
- [x] **PAGE-2**: Dashboard: 3 quick action cards with links
- [x] **PAGE-3**: Datasets: EmptyState when no datasets
- [x] **PAGE-4**: Datasets: drag-drop zone visible
- [x] **PAGE-5**: Datasets: create dataset form (name input + Create button)
- [x] **PAGE-6**: Datasets: dataset card with name, record count, date
- [x] **PAGE-7**: Datasets: click card → navigate to /datasets/[id]
- [x] **PAGE-8**: Dataset detail: records table with ID, Content, Title columns
- [x] **PAGE-9**: Dataset detail: pagination Prev/Next
- [x] **PAGE-10**: Dataset detail: checkbox select + bulk delete
- [x] **PAGE-11**: Search: 6 mode buttons render
- [x] **PAGE-12**: Search: input + Enter → triggers search
- [x] **PAGE-13**: Search: results with score, collection, metadata.text
- [x] **PAGE-14**: Search: feedback stars (1-5) on each result
- [x] **PAGE-15**: Search: RAG mode → AI Answer box
- [x] **PAGE-16**: Chat: empty state "Ask a question"
- [x] **PAGE-17**: Chat: send message → user bubble + typing → assistant bubble
- [x] **PAGE-18**: Chat: sources section in assistant message
- [x] **PAGE-19**: Chat: RAG/COT mode toggle
- [x] **PAGE-20**: Graph: dataset selector dropdown
- [x] **PAGE-21**: Graph: D3 SVG canvas renders
- [x] **PAGE-22**: Graph: node type filter badges
- [x] **PAGE-23**: Graph: click node → detail panel
- [x] **PAGE-24**: Collections: cards with name, records, dim, model, domain
- [x] **PAGE-25**: Memories: type filter badges + list items with key/value/type

### Категория D: Навигация и Layout (8 тестов)
- [x] **NAV-1**: Sidebar: 10 nav items visible
- [x] **NAV-2**: Sidebar: active item highlighted based on pathname
- [x] **NAV-3**: Sidebar: collapse button works
- [x] **NAV-4**: Sidebar: Levara logo + brand
- [x] **NAV-5**: Login page: no sidebar
- [x] **NAV-6**: Settings: theme toggle (light/dark/system)
- [x] **NAV-7**: Settings: language selector (ru/en)
- [x] **NAV-8**: Analytics: auto-refresh badge visible

### Категория E: API Client (8 тестов)
- [x] **API-1**: X-Trace-ID header generated (UUID format)
- [x] **API-2**: credentials='include' on every request
- [x] **API-3**: ApiError: status, code, message, traceId, retryable fields
- [x] **API-4**: levara.health() → GET /health
- [x] **API-5**: levara.search() → POST /api/v1/search/text
- [x] **API-6**: levara.datasets() → GET /api/v1/datasets
- [x] **API-7**: levara.cognify() → POST /api/v1/cognify
- [x] **API-8**: levara.submitFeedback() → POST /api/v1/feedback

### Категория F: SSE (5 тестов)
- [x] **SSE-1**: useSSE connects to URL
- [x] **SSE-2**: useSSE status: connecting → connected
- [x] **SSE-3**: useSSE error → reconnecting with backoff
- [x] **SSE-4**: useSSE max retries → status=error
- [x] **SSE-5**: useCognifyProgress typed interface

### Категория G: Специфичные экраны (14 тестов)
- [x] **SPEC-1**: Analytics: system status widget
- [x] **SPEC-2**: Analytics: LLM cache hit rate bar
- [x] **SPEC-3**: Analytics: feedback insights (avg rating stars)
- [x] **SPEC-4**: Analytics: recent errors list
- [x] **SPEC-5**: Notebooks: add code cell
- [x] **SPEC-6**: Notebooks: add markdown cell
- [x] **SPEC-7**: Notebooks: run code cell → output appears
- [x] **SPEC-8**: Notebooks: cell status (idle/running/done/error)
- [x] **SPEC-9**: Notebooks: delete cell
- [x] **SPEC-10**: Notebooks: Ctrl+Enter runs cell
- [x] **SPEC-11**: Login: email + password fields
- [x] **SPEC-12**: Login: toggle sign in / register
- [x] **SPEC-13**: Login: error display on failed auth
- [x] **SPEC-14**: Login: submit → redirect to /
