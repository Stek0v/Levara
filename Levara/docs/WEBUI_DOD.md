# Levara WebUI — Definition of Done (T1-T87)

## Validated: T1-T20 (ручная валидация с пользователем)

---

## Блок 5: Design System (T21-T27)

### T21. Design tokens
**DoD:**
- [ ] Color palette: primary (blue), success (green), warning (amber), error (red), neutral (gray). 10 оттенков каждого (50-950)
- [ ] Light theme + Dark theme: CSS custom properties (`--color-primary-500`, `--color-bg-surface`)
- [ ] Spacing scale: 0/1/2/3/4/5/6/8/10/12/16/20/24/32/40/48/64 (в units of 4px)
- [ ] Typography: Inter (UI), JetBrains Mono (code). Sizes: xs(12)/sm(14)/base(16)/lg(18)/xl(20)/2xl(24)/3xl(30). Weights: 400/500/600/700
- [ ] Border radius: none(0)/sm(4)/md(6)/lg(8)/xl(12)/full(9999)
- [ ] Shadows: sm/md/lg/xl для elevation
- [ ] Tokens экспортированы как CSS variables + JS constants
- [ ] Figma/design tool: tokens синхронизированы (или Figma не используется — задокументировать)

### T22. Component library
**DoD:**
- [ ] Primitives: Button (primary/secondary/ghost/danger, sizes sm/md/lg, loading state, disabled), Input (text/password/search, validation, prefix/suffix icons), Select (single/multi, searchable, async options), Checkbox, Radio, Toggle, Textarea
- [ ] Layout: Stack (vertical/horizontal), Grid, Divider, Spacer, Card, Container
- [ ] Data display: Table (sort/select/pagination integrated), Badge, Avatar, Tag/Chip, Tooltip, Progress (bar/circular), Skeleton
- [ ] Feedback: Toast (success/error/warning/info, auto-dismiss, action button), Modal (sizes, confirm variant), Alert/Banner (inline, dismissable), EmptyState (illustration + CTA), ErrorState (message + retry)
- [ ] Navigation: Sidebar, Breadcrumbs, Tabs, Dropdown menu, Command palette
- [ ] Overlay: Dialog, Popover, Sheet (side panel)
- [ ] Все компоненты: TypeScript typed props, forwardRef, a11y (ARIA), keyboard support
- [ ] Все компоненты: поддерживают dark/light theme через tokens

### T23. Паттерны страниц
**DoD:**
- [ ] **List pattern**: header (title + actions) → filter bar → table → pagination. Используется: Datasets, Memories, Feedback, Users, Collections
- [ ] **Detail pattern**: breadcrumb → header (title + status + actions) → tabs → content. Используется: Dataset detail, Collection detail, Notebook
- [ ] **Form pattern**: header → sections → fields (with validation) → footer (cancel + submit). Используется: Create dataset, Settings, Create collection
- [ ] **Dashboard pattern**: grid of widgets (responsive: 1col mobile, 2col tablet, 3-4col desktop). Используется: Dashboard, Analytics
- [ ] **Chat pattern**: messages list (scroll-to-bottom) + input (multiline + send button + attachments). Используется: Chat/RAG
- [ ] **Canvas pattern**: full-screen canvas + overlay controls (zoom/filter/search). Используется: Graph viewer
- [ ] Каждый паттерн: responsive (mobile/tablet/desktop), loading/empty/error states включены

### T24. Storybook
**DoD:**
- [ ] Storybook настроен: все компоненты из T22 имеют stories
- [ ] Каждый story: default + все варианты (size, variant, state) + interactive controls (args)
- [ ] a11y addon включён: accessibility checks на каждом story
- [ ] Dark mode addon: preview в обеих темах
- [ ] Viewport addon: mobile/tablet/desktop preview
- [ ] Документация: каждый компонент имеет MDX page с usage guidelines
- [ ] CI: Storybook build на каждый PR (проверка что stories не сломаны)
- [ ] Deployed: Storybook доступен по URL для дизайнеров/PM (Chromatic или static hosting)

### T25. Dark/Light theme
**DoD:**
- [ ] Все цвета через CSS variables (нет hardcoded hex в компонентах)
- [ ] Переключатель в header: иконка sun/moon
- [ ] 3 режима: Light / Dark / System (follow OS preference)
- [ ] Выбор сохраняется: `PUT /settings` (theme) + localStorage fallback
- [ ] При переключении: нет FOUC (flash of unstyled content), transition 150ms
- [ ] Графы/charts: цвета адаптируются (не белый текст на белом фоне)
- [ ] Code blocks: отдельная syntax theme для light/dark

### T26. Responsive breakpoints
**DoD:**
- [ ] Breakpoints: mobile (< 768px), tablet (768-1024px), desktop (> 1024px), wide (> 1440px)
- [ ] Mobile: sidebar collapsed → hamburger menu, tables → card view, graph → simplified list
- [ ] Tablet: sidebar collapsed по умолчанию, toggle кнопка, tables полные
- [ ] Desktop: sidebar visible, full layout
- [ ] Touch targets: минимум 44×44px на mobile (WCAG 2.5.8)
- [ ] Тестирование: Playwright viewport tests для 375px (iPhone), 768px (iPad), 1280px (laptop)
- [ ] No horizontal scroll на всех breakpoints

### T27. Visual regression tests
**DoD:**
- [ ] Chromatic или Percy подключен к CI
- [ ] Baseline screenshots для всех Storybook stories
- [ ] PR: автоматическое сравнение → visual diff → approve/reject
- [ ] Покрытие: все компоненты из T22 + все паттерны страниц из T23
- [ ] 3 viewport'а: mobile/tablet/desktop
- [ ] 2 темы: light/dark
- [ ] Threshold: pixel diff < 0.1% — auto-approve, > 0.1% — manual review

---

## Блок 6: Accessibility (T28-T32)

### T28. WCAG 2.2 AA baseline
**DoD:**
- [ ] Semantic HTML: `<nav>`, `<main>`, `<article>`, `<aside>`, `<header>`, `<footer>`, `<section>` — не `<div>` для всего
- [ ] Headings hierarchy: h1 → h2 → h3, без пропусков, один h1 на страницу
- [ ] Color contrast: text ≥ 4.5:1, large text ≥ 3:1, UI components ≥ 3:1
- [ ] Images: все `<img>` имеют `alt`. Decorative: `alt=""`
- [ ] Forms: все inputs имеют связанный `<label>`. Error messages связаны через `aria-describedby`
- [ ] ARIA: кастомные компоненты (Dropdown, Modal, Tabs, Toast) используют ARIA patterns из APG
- [ ] Focus visible: outline стиль для всех интерактивных элементов (не `outline: none`)
- [ ] Motion: `prefers-reduced-motion` уважается (отключить animations)
- [ ] Target size: минимум 24×24px (AA), рекомендация 44×44px для touch

### T29. Keyboard navigation
**DoD:**
- [ ] Tab order логичный: left → right, top → bottom, sidebar → main content
- [ ] Skip link: "Перейти к содержимому" — первый focusable element, visible on focus
- [ ] Все actions доступны с клавиатуры: Enter (activate), Space (toggle), Escape (close/cancel)
- [ ] Модальные окна: focus trap внутри, Tab/Shift+Tab циклический
- [ ] Dropdown/Select: Arrow keys для навигации, Enter для выбора, Escape для закрытия
- [ ] Table: сортировка через Enter на header, row selection через Space
- [ ] Search: Enter submit, Escape clear
- [ ] Тестирование: все P0 user journeys (T7) проходимы без мыши

### T30. Focus management
**DoD:**
- [ ] Modal open → focus на первый focusable element внутри (или на заголовок)
- [ ] Modal close → focus возвращается на trigger element
- [ ] Toast appear → не крадёт focus. Screen reader объявляет через `aria-live="polite"`
- [ ] Page navigation (SPA) → focus на `<main>` или h1 новой страницы
- [ ] Delete + undo → focus на следующий элемент в списке
- [ ] Error on form submit → focus на первое поле с ошибкой
- [ ] Infinite scroll / load more → focus остаётся, не сбрасывается на начало
- [ ] `focus-visible` polyfill: outline только при keyboard navigation, не при клике мышью

### T31. Screen reader testing
**DoD:**
- [ ] VoiceOver (macOS): все P0 journeys озвучиваются корректно
- [ ] NVDA (Windows): все P0 journeys — если есть Windows dev
- [ ] Landmarks: `<nav>`, `<main>`, `<aside>` с `aria-label` — screen reader показывает landmark list
- [ ] Tables: `<th scope="col/row">` — screen reader объявляет заголовки при навигации по ячейкам
- [ ] Dynamic content: `aria-live` regions для toast, search results count, progress updates
- [ ] Чеклист: 10 критических потоков протестированы вручную, задокументированы

### T32. Automated a11y в CI
**DoD:**
- [ ] axe-core интегрирован в unit/component tests (jest-axe или vitest-axe)
- [ ] Каждый компонент из T22: axe проверка в test suite
- [ ] Playwright + @axe-core/playwright: E2E a11y на 5 ключевых страницах
- [ ] CI: PR blocked если axe находит critical/serious violations
- [ ] Storybook: a11y addon показывает violations в каждом story
- [ ] Reporting: a11y violations трекаются как bugs (critical = P0, serious = P1)

---

## Блок 7: i18n (T33-T37)

### T33. Определить локали
**DoD:**
- [ ] MVP: `ru` (Russian) + `en` (English)
- [ ] Default locale: `ru` (основные пользователи)
- [ ] Fallback: `en` (если перевод отсутствует)
- [ ] BCP 47 tags: `ru-RU`, `en-US`
- [ ] Определение locale: (1) user setting, (2) browser `navigator.language`, (3) fallback
- [ ] URL strategy: нет locale в URL (single-domain, setting-based)

### T34. Система переводов
**DoD:**
- [ ] i18next (или аналог по стеку) интегрирован
- [ ] Все видимые строки через ключи: `t('datasets.empty.title')`, нет hardcoded text
- [ ] Namespace'ы: `common`, `auth`, `datasets`, `search`, `chat`, `graph`, `settings`, `errors`
- [ ] Plural rules: `t('items', {count: 5})` → "5 записей" / "5 records"
- [ ] Interpolation: `t('welcome', {name: 'Alex'})` → "Привет, Alex"
- [ ] Нет конкатенации строк: `t('deleted', {count})` а не `count + ' удалено'`
- [ ] Файлы переводов: `locales/ru/datasets.json`, `locales/en/datasets.json`
- [ ] Missing key → fallback locale → если нет → показать key (dev warning в console)

### T35. Форматирование дат/чисел
**DoD:**
- [ ] Даты: `Intl.DateTimeFormat` по текущей locale. Не `moment.js`
- [ ] Форматы дат: relative ("5 мин назад"), short ("10 апр"), full ("10 апреля 2026, 15:30")
- [ ] Числа: `Intl.NumberFormat` — разделители (1,234.56 en / 1 234,56 ru)
- [ ] File sizes: "14.2 МБ" / "14.2 MB"
- [ ] Durations: "2 мин 30 сек" / "2 min 30 sec"
- [ ] Timezone: все даты в UTC от backend, отображение в local timezone пользователя

### T36. Псевдолокализация
**DoD:**
- [ ] Pseudo-locale `en-XA`: all strings wrapped [Ṱĥïş ïş ţëšţ] — проверка что все строки проходят через i18n
- [ ] Long string test: pseudo adds +30% length — проверка что UI не ломается
- [ ] CI: псевдолокализация запускается в Storybook, визуальные дефекты = bug
- [ ] RTL: не требуется для MVP (ru+en — LTR). Задокументировать как "not supported"

### T37. Extraction workflow
**DoD:**
- [ ] CLI команда `npm run i18n:extract` — сканирует код, собирает все `t('key')` вызовы
- [ ] Новые ключи: добавляются в `en` файл как `"key": "KEY"` (заглушка)
- [ ] Неиспользуемые ключи: CLI warning, можно удалить
- [ ] PR check: если есть hardcoded strings (не через `t()`) — warning в review
- [ ] Translation update flow: (1) dev добавляет ключ в en, (2) translator заполняет ru, (3) PR merge

---

## Блок 8: Архитектура фронтенда (T38-T45)

### T38. Выбор фреймворка
**DoD:**
- [ ] Решение зафиксировано с обоснованием (опыт команды, экосистема, SSR support)
- [ ] Рекомендация: **React + Next.js** (App Router) — наибольшая экосистема, SSR/SSG, TypeScript first
- [ ] Альтернатива если команда Vue: **Vue + Nuxt 3**
- [ ] Задокументировано: почему выбран, какие tradeoffs приняты
- [ ] Прототип: hello world с auth + API call работает

### T39. SSR/SSG/CSR стратегия
**DoD:**
- [ ] **CSR (client-side)**: Dashboard, Search, Chat, Graph, Notebooks, Settings — динамические, требуют auth
- [ ] **SSR (server-side)**: Login/Register — SEO не нужно, но быстрый TTFB для первого впечатления
- [ ] **SSG (static)**: Docs pages, API reference, 404/500 error pages
- [ ] Auth pages: redirect если уже залогинен (SSR check cookie)
- [ ] Protected pages: middleware redirect на login если нет token
- [ ] Задокументировано: таблица "страница → render strategy → почему"

### T40. State management
**DoD:**
- [ ] **Server state**: TanStack Query (React Query) — fetching, caching, stale-while-revalidate, optimistic updates
- [ ] **UI state**: React useState/useReducer (local component state) — modals, form inputs, toggles
- [ ] **Global app state**: Zustand (минимальный) — theme, locale, sidebar collapsed, current tenant
- [ ] **Persisted state**: localStorage — theme, locale, sidebar state. Нет sensitive data
- [ ] Правило: "если данные приходят с сервера — TanStack Query, не Zustand/Redux"
- [ ] Правило: "если state нужен только в компоненте — useState, не global"
- [ ] Devtools: TanStack Query Devtools + Zustand devtools в dev mode

### T41. API client генерация
**DoD:**
- [ ] OpenAPI spec: backend генерирует `/api/v1/openapi.json` (или файл в репозитории)
- [ ] Codegen: `openapi-typescript-codegen` или `orval` → TypeScript types + fetch functions
- [ ] Автоматизация: `npm run api:generate` → обновляет клиент из spec
- [ ] CI: если spec изменился и клиент не перегенерирован → fail
- [ ] Все API calls через сгенерированный клиент, нет ручных fetch с hardcoded URLs
- [ ] Error types: сгенерированы из OpenAPI error schemas

### T42. Routing и навигация
**DoD:**
- [ ] File-based routing (Next.js App Router / Nuxt pages)
- [ ] Route structure: `/login`, `/dashboard`, `/datasets`, `/datasets/[id]`, `/search`, `/chat`, `/graph`, `/collections`, `/memories`, `/notebooks`, `/notebooks/[id]`, `/settings`, `/admin/users`, `/admin/tenants`
- [ ] Breadcrumbs: автоматические по route hierarchy
- [ ] Sidebar: persistent navigation, active state по текущему route
- [ ] Deep linking: все состояния в URL (search query, filters, pagination, active tab)
- [ ] Protected routes: middleware redirect если нет auth
- [ ] 404 page: custom, с поиском и ссылками на main sections
- [ ] Loading: route-level loading.tsx / suspense boundary

### T43. Form library
**DoD:**
- [ ] React Hook Form (или Formik для Vue: VeeValidate)
- [ ] Validation: zod schemas (shared between frontend и OpenAPI types)
- [ ] Error display: inline under field, red border, `aria-describedby`
- [ ] Submit: disabled до valid, loading spinner, prevent double submit
- [ ] Dirty check: unsaved changes → confirm before navigate away ("Вы уверены? Несохранённые изменения будут потеряны")
- [ ] Complex forms: multi-step (wizard) для Cognify settings, Collection creation
- [ ] File inputs: drag-drop zone, preview, progress, dedup check (T4)

### T44. Data fetching + caching
**DoD:**
- [ ] TanStack Query: все GET → `useQuery`, все POST/PUT/DELETE → `useMutation`
- [ ] Stale time: 30s для списков (datasets, collections), 5 min для metadata (info, health)
- [ ] Cache invalidation: после mutation → invalidate related queries
- [ ] Optimistic updates: feedback submit, memory save, theme change
- [ ] Prefetch: hover over sidebar link → prefetch page data
- [ ] Error retry: 3 retries с backoff для 5xx, no retry для 4xx
- [ ] Pagination: `useInfiniteQuery` для cursor-based (search results)
- [ ] Background refetch: focus window → refetch stale queries

### T45. SSE client
**DoD:**
- [ ] Custom hook: `useSSE(url, options)` → `{data, status, error, reconnect}`
- [ ] Auto-reconnect: exponential backoff 1s→30s (T14 policy)
- [ ] Last-Event-ID: resume from last received event
- [ ] Heartbeat detection: timeout 30s → reconnect
- [ ] Event parsing: typed events (progress, complete, error) с TypeScript discriminated unions
- [ ] Cleanup: close connection on component unmount / page navigation
- [ ] Status: `connecting` → `connected` → `reconnecting` → `disconnected`
- [ ] React integration: `useCognifyProgress(runId)` → typed progress state

---

## Блок 9: CI/CD (T46-T50)

### T46. Monorepo или отдельный репо
**DoD:**
- [ ] Решение: **отдельный репо** `levara-webui` (Go backend и JS frontend имеют разные CI/CD, dependencies, release cycles)
- [ ] Альтернатива: `Levara/webui/` monorepo — если команда маленькая и хочет atomic commits
- [ ] Задокументировано: решение + обоснование
- [ ] Связь: WebUI repo ссылается на OpenAPI spec из Levara repo (git submodule или API artifact)

### T47. Linting + formatting
**DoD:**
- [ ] ESLint: strict TypeScript rules, React hooks rules, import order
- [ ] Prettier: opinionated formatting, tab width 2, single quotes, trailing commas
- [ ] EditorConfig: consistent across editors (indent, newlines, encoding)
- [ ] Stylelint: CSS/SCSS rules (если не Tailwind — тогда Tailwind plugin)
- [ ] Husky + lint-staged: pre-commit hook — lint + format only staged files
- [ ] CI: lint check на каждый PR, fail на errors (warnings allowed с threshold)
- [ ] Config files committed: `.eslintrc`, `.prettierrc`, `.editorconfig`, `.stylelintrc`

### T48. PR pipeline
**DoD:**
- [ ] GitHub Actions workflow on `pull_request`:
- [ ] Step 1: Install dependencies (cached, < 30s)
- [ ] Step 2: Lint + typecheck (`tsc --noEmit`) — fail fast
- [ ] Step 3: Unit + component tests — coverage report as PR comment
- [ ] Step 4: Build production bundle — verify no build errors, check bundle size
- [ ] Step 5: Lighthouse CI — CWV check against budgets (T17)
- [ ] Step 6: Preview deploy (Vercel/Netlify/Cloudflare) — comment with preview URL
- [ ] Total time: < 5 min. If > 5 min → optimize
- [ ] Required checks: lint + typecheck + tests + build must pass for merge

### T49. Staging environment
**DoD:**
- [ ] PR → auto-deploy preview (unique URL per PR, destroyed on merge)
- [ ] `main` branch → auto-deploy staging (persistent URL, latest code)
- [ ] Git tag `v*` → deploy production (manual trigger or auto)
- [ ] Staging points to staging backend (separate Levara instance or shared dev)
- [ ] E2E smoke suite runs on staging after deploy
- [ ] Rollback: deploy previous tag to production in < 5 min
- [ ] Env vars: managed through CI secrets, not committed

### T50. Release process
**DoD:**
- [ ] SemVer: MAJOR.MINOR.PATCH
- [ ] Conventional Commits enforced: `feat:`, `fix:`, `chore:`, `docs:`, `BREAKING CHANGE:`
- [ ] commitlint in pre-commit hook
- [ ] semantic-release: auto-determine version from commits, generate changelog, create GitHub release
- [ ] CHANGELOG.md auto-generated, grouped by type (Features, Bug Fixes, Breaking Changes)
- [ ] npm/container artifact published on release
- [ ] Release notes: auto-posted to Slack/team channel

---

## Блок 10: Тестирование (T51-T55)

### T51. Тест-пирамида
**DoD:**
- [ ] Unit (base): компонент рендерится, props работают, events fire — Vitest + Testing Library
- [ ] Integration (middle): страница загружает данные, форма сабмитит, navigation работает — Vitest + MSW (mock API)
- [ ] E2E (top): login → upload → cognify → search → feedback — Playwright + real API (staging)
- [ ] Ratio target: 70% unit, 20% integration, 10% E2E
- [ ] Coverage: > 70% statements для бизнес-логики (hooks, utils, state). Не гнаться за 100% на UI

### T52. E2E framework
**DoD:**
- [ ] Playwright configured: chromium + firefox (T20 Tier 1)
- [ ] 5 Critical User Journeys automated:
  1. Login → Dashboard visible
  2. Upload file → appears in datasets
  3. Cognify → progress → complete
  4. Search (3 modes) → results displayed
  5. Chat → RAG answer with citations
- [ ] Page Object Model: reusable selectors + actions
- [ ] Test data: seeded before suite, cleaned after
- [ ] CI: E2E runs on staging after deploy (T49)
- [ ] Artifacts: screenshots + traces on failure

### T53. Unit/component tests
**DoD:**
- [ ] Vitest + Testing Library configured
- [ ] All T22 components: render test + variant tests + a11y test (jest-axe)
- [ ] Hooks: custom hooks tested with renderHook
- [ ] Utils: pure functions 100% covered (formatDate, normalize, tokenF1)
- [ ] State: Zustand stores tested (initial state, actions, selectors)
- [ ] API: TanStack Query hooks tested with MSW (mock responses, error states, loading)
- [ ] Coverage report: generated on each PR, commented as summary

### T54. API contract tests
**DoD:**
- [ ] OpenAPI spec is source of truth
- [ ] Prism (Stoplight) or MSW: mock server from OpenAPI spec for frontend tests
- [ ] Contract check: if frontend calls endpoint not in spec → test fails
- [ ] Type check: generated TypeScript types always match spec (T41 pipeline)
- [ ] CI: if spec changes but frontend not updated → warning (breaking = fail)

### T55. Smoke suite
**DoD:**
- [ ] 10 tests, < 3 min total runtime
- [ ] Runs: before every production deploy
- [ ] Covers: auth, navigation, search, upload, cognify start, settings
- [ ] Failure: blocks deploy, alerts team
- [ ] Data: uses minimal test fixtures, doesn't depend on large datasets
- [ ] Independent: each test can run standalone, no order dependency

---

## Блок 11: Observability (T56-T60)

### T56. Frontend error reporting
**DoD:**
- [ ] Sentry SDK integrated: automatic capture of unhandled exceptions + promise rejections
- [ ] Context: user_id, page, action, browser, OS attached to every error
- [ ] Breadcrumbs: last 20 user actions (clicks, navigation, API calls) before error
- [ ] Source maps: uploaded to Sentry on build (not served to client)
- [ ] API errors (5xx): captured with request URL, status, traceId
- [ ] Filtering: ignore 401 (expected), ignore ResizeObserver loop (browser noise)
- [ ] Alert: Sentry → Slack on new error spike (> 10 in 5 min)
- [ ] PII scrub: no tokens, passwords, emails in error payloads

### T57. TraceId сквозной
**DoD:**
- [ ] Frontend generates `X-Trace-ID` (UUID) for each API request
- [ ] Header passed to backend in every fetch call
- [ ] Backend logs include traceId, returns in response header
- [ ] Error display: traceId shown to user in error toast/modal ("Код ошибки: abc123")
- [ ] Sentry: traceId attached to frontend error → searchable in backend logs
- [ ] Correlation: one trace spans: UI click → API request → embed call → search → response → render

### T58. Performance monitoring
**DoD:**
- [ ] web-vitals library: LCP, INP, CLS collected in production
- [ ] Custom metrics: search_render_time, upload_duration, cognify_start_time
- [ ] Data sent to: analytics endpoint (self-hosted) or Sentry Performance
- [ ] Grafana dashboard: CWV percentiles over time, per-page breakdown
- [ ] Lighthouse CI: runs on every PR, results stored, trend visible
- [ ] Regression alert: if LCP p75 increases > 500ms week-over-week → warning

### T59. User analytics
**DoD:**
- [ ] Anonymous: no PII, no cookies, no third-party trackers
- [ ] Events: page_view, search_performed, file_uploaded, cognify_started, feedback_submitted
- [ ] Properties: page, search_type, file_type, duration — no user identity
- [ ] Self-hosted: Plausible or custom endpoint → own database
- [ ] Opt-out: setting to disable analytics entirely
- [ ] Dashboard: most used features, search vs chat ratio, popular collections
- [ ] GDPR compliant: no consent banner needed (no PII, no cookies)

### T60. Dashboards + алерты
**DoD:**
- [ ] Grafana dashboard "WebUI Health": JS errors/hour, API latency p50/p95, active sessions, CWV
- [ ] Grafana dashboard "Usage": searches/hour, uploads/day, cognify runs, feedback distribution
- [ ] Alerts: JS errors > 50/hour → Slack warning. API p95 > 3s → Slack critical
- [ ] Alert escalation: warning → Slack. Critical → Slack + PagerDuty (if configured)
- [ ] Dashboard accessible to: Admin + Developer personas

---

## Блок 12: Безопасность (T61-T70)

### T61. Threat model
**DoD:**
- [ ] Assets identified: user data, API keys, documents, search history, memories, graph data
- [ ] Actors: authenticated user, admin, anonymous, AI agent (MCP), attacker
- [ ] Trust boundaries: browser ↔ API (untrusted), API ↔ database (trusted), API ↔ LLM provider (semi-trusted)
- [ ] Top threats: XSS (injected content in search/chat), CSRF (state-changing GETs), broken access control (RBAC bypass), token theft, dependency supply chain
- [ ] Mitigations mapped to each threat (reference T62-T70)
- [ ] Reviewed: annually or on major architecture change

### T62. Auth architecture
**DoD:**
- [ ] JWT stored in httpOnly, Secure, SameSite=Strict cookie — not localStorage
- [ ] Access token: 15 min TTL. Refresh token: 7 days TTL, rotated on use
- [ ] Login: POST /auth/login → Set-Cookie (access + refresh)
- [ ] API calls: cookie sent automatically (no Authorization header needed from frontend)
- [ ] Token refresh: interceptor detects 401 → POST /auth/refresh → retry original request
- [ ] Logout: POST /auth/logout → clear cookies + invalidate refresh token server-side
- [ ] Tab sync: logout in one tab → logout in all tabs (BroadcastChannel)
- [ ] Session timeout: 30 min idle → prompt "Сессия истекает через 5 мин. Продлить?" → logout

### T63. CORS policy
**DoD:**
- [ ] Production: explicit origin whitelist (no wildcards)
- [ ] Development: `localhost:3001` allowed
- [ ] Credentials: `Access-Control-Allow-Credentials: true` (for cookies)
- [ ] Methods: GET, POST, PUT, PATCH, DELETE, OPTIONS
- [ ] Headers: Content-Type, X-Trace-ID, X-Idempotency-Key
- [ ] Preflight cache: `Access-Control-Max-Age: 86400`

### T64. Security headers
**DoD:**
- [ ] `Content-Security-Policy`: `default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self' https://api.deepseek.com; frame-ancestors 'none'`
- [ ] `X-Frame-Options: DENY`
- [ ] `X-Content-Type-Options: nosniff`
- [ ] `Strict-Transport-Security: max-age=31536000; includeSubDomains` (production only)
- [ ] `Referrer-Policy: strict-origin-when-cross-origin`
- [ ] `Permissions-Policy: camera=(), microphone=(), geolocation=()`
- [ ] Headers set by: reverse proxy (nginx) or Next.js middleware

### T65. XSS prevention
**DoD:**
- [ ] No `dangerouslySetInnerHTML` anywhere (or explicit allowlist with DOMPurify sanitization)
- [ ] User input in search/chat: escaped by React/Vue by default, verified in code review
- [ ] Markdown rendering (notebooks, chat): sanitized with DOMPurify, no raw HTML
- [ ] CSP: no `unsafe-eval`, no inline scripts except nonce-based
- [ ] URL parameters: validated before use (no `javascript:` protocol in links)
- [ ] File names: displayed as text, not rendered as HTML

### T66. CSRF protection
**DoD:**
- [ ] SameSite=Strict on auth cookies — primary protection
- [ ] All mutations use POST/PUT/DELETE (no state changes via GET)
- [ ] CORS: only whitelisted origins
- [ ] For defense-in-depth: X-Requested-With header or double-submit cookie pattern on critical mutations (delete account, change password, transfer ownership)

### T67. Dependency scanning
**DoD:**
- [ ] Dependabot or Renovate: enabled on repo, weekly PR for patch/minor updates
- [ ] `npm audit`: runs in CI, fail on critical/high vulnerabilities
- [ ] Lock file: `package-lock.json` committed, reviewed on dependency changes
- [ ] Major updates: quarterly review, manual upgrade + test
- [ ] License check: no GPL in dependencies (if proprietary), documented acceptable licenses

### T68. Rate limiting UI
**DoD:**
- [ ] 429 response → toast: "Слишком много запросов. Повторите через N сек"
- [ ] Countdown timer visible
- [ ] Submit buttons disabled during cooldown
- [ ] `Retry-After` header parsed and respected
- [ ] Debounce on client: search 300ms, save 1s — reduce server load

### T69. Secrets management
**DoD:**
- [ ] No API keys, tokens, passwords in client-side JavaScript (ever)
- [ ] Environment variables: injected at build time via CI (NEXT_PUBLIC_ prefix only for public values)
- [ ] Backend API key (DeepSeek, etc.): server-side only, never exposed to browser
- [ ] `.env.example` in repo with placeholder values, `.env` in `.gitignore`
- [ ] CI secrets: GitHub Actions secrets, not in workflow YAML

### T70. Security audit checklist
**DoD:**
- [ ] OWASP ASVS Level 1 checklist completed for MVP
- [ ] Pre-release: manual security review of auth flow, RBAC, input handling
- [ ] Quarterly: automated DAST scan (OWASP ZAP) against staging
- [ ] Annually: third-party pentest (if budget allows) or internal deep review
- [ ] Findings tracked as security bugs (P0 = fix before release)

---

## Блок 13: Приватность (T71-T76)

### T71. Data inventory
**DoD:**
- [ ] Table: data category → purpose → storage → retention → legal basis
- [ ] Categories: user profile, documents, search queries, chat history, feedback, memories, graph entities
- [ ] Storage: PostgreSQL (metadata), WAL (vectors), Neo4j (graph), filesystem (uploads)
- [ ] Retention: user data — until deletion. Uploads — configurable (default 1 year). Logs — 90 days
- [ ] Reviewed: annually or on new feature that collects data

### T72. Privacy notice
**DoD:**
- [ ] Footer link: "Политика конфиденциальности"
- [ ] Content: what we collect, why, how long, who has access, user rights
- [ ] Language: Russian + English
- [ ] Updated: date visible, changelog for changes

### T73. Cookie consent
**DoD:**
- [ ] Auth cookies: strictly necessary, no consent needed
- [ ] Analytics (if T59 uses cookies): consent banner before activation
- [ ] Banner: "Мы используем cookies для аналитики. [Принять] [Отказаться] [Подробнее]"
- [ ] Choice saved: localStorage flag, respected on subsequent visits
- [ ] If T59 is cookieless (Plausible): no banner needed — document why

### T74. Right to deletion
**DoD:**
- [ ] Settings → "Удалить аккаунт" → confirm modal → password required → delete all user data
- [ ] Deleted: user record, memories, interactions, feedback, uploaded files, API keys, session data
- [ ] NOT deleted: anonymized aggregated analytics, system logs (PII scrubbed)
- [ ] Timeline: 30 days grace period ("Аккаунт будет удалён через 30 дней. [Отменить]")
- [ ] Email notification on deletion request and completion

### T75. Data export (GDPR)
**DoD:**
- [ ] Settings → "Скачать мои данные" → generates ZIP with JSON files
- [ ] Included: profile, memories, interactions, feedback, uploaded file list (not files themselves — too large)
- [ ] Format: JSON, human-readable keys, ISO dates
- [ ] Processing time: < 5 min for typical user, async with email notification
- [ ] Rate limit: 1 export per 24 hours

### T76. PII masking in logs
**DoD:**
- [ ] Frontend console.log: no emails, passwords, tokens, API keys in production
- [ ] Sentry: before-send hook strips PII fields (email, password, authorization headers)
- [ ] Backend logs: same — already handled, but verify no frontend pass-through
- [ ] Network tab: auth tokens visible only in cookies (httpOnly, not in request body/URL)
- [ ] Documented: list of PII fields and where they appear

---

## Блок 14: Документация (T77-T82)

### T77. Docs site
**DoD:**
- [ ] Docusaurus or VitePress deployed at `/docs` or `docs.levara.dev`
- [ ] Sections (Diátaxis): Tutorials, How-to Guides, Reference, Concepts
- [ ] Search: built-in full-text search
- [ ] Versioned: docs version matches WebUI release version
- [ ] Auto-deploy: on release tag push

### T78. User quickstart
**DoD:**
- [ ] Title: "5 минут до первого поиска"
- [ ] Steps: (1) Откройте WebUI → (2) Зарегистрируйтесь → (3) Перетащите PDF → (4) Нажмите Cognify → (5) Введите вопрос → (6) Получите ответ
- [ ] Screenshots: каждый шаг с аннотациями
- [ ] Video: 2-min screencast (optional, P2)
- [ ] Troubleshooting: 3 самых частых проблемы

### T79. Developer quickstart
**DoD:**
- [ ] Prerequisites: Node.js 20+, npm 10+, Git
- [ ] Steps: `git clone` → `cp .env.example .env` → `npm install` → `npm run dev` → open localhost:3001
- [ ] Backend: "запустите Levara сервер на :8080" (ссылка на CLAUDE.md)
- [ ] Time to first page: < 3 min from git clone
- [ ] Verified: CI runs quickstart steps, tests pass

### T80. API reference
**DoD:**
- [ ] Swagger UI at `/api/docs` or embedded in docs site
- [ ] Generated from OpenAPI spec (T41)
- [ ] Interactive: "Try it" button for each endpoint
- [ ] Auth: JWT token input field for authenticated endpoints
- [ ] Examples: request/response for each endpoint
- [ ] Updated: automatically on backend release

### T81. Troubleshooting FAQ
**DoD:**
- [ ] 20 entries, grouped by category
- [ ] Categories: Login, Upload, Cognify, Search, Performance, Errors
- [ ] Format: "Problem → Причина → Решение"
- [ ] Examples: "Cognify зависает" → LLM provider timeout → проверить LLM_ENDPOINT, увеличить timeout
- [ ] Searchable: docs site search indexes FAQ entries
- [ ] Updated: on each release, based on support requests

### T82. Contributing guide
**DoD:**
- [ ] CONTRIBUTING.md in repo root
- [ ] Sections: setup, code style, PR process, commit convention, test requirements
- [ ] PR template: description, screenshots, test coverage, checklist
- [ ] Issue templates: bug report, feature request
- [ ] Code of Conduct: link to standard (Contributor Covenant)

---

## Блок 15: Maintenance (T83-T87)

### T83. Browser support policy
**DoD:**
- [ ] Matrix from T20 documented in user-facing docs
- [ ] Update cadence: quarterly review, drop versions > 2 major behind
- [ ] Notification: 1 release warning before dropping a browser version
- [ ] Browserslist config in repo, CI checks it

### T84. Dependency update cadence
**DoD:**
- [ ] Patch/minor: Dependabot weekly, auto-merge if tests pass
- [ ] Major: quarterly review sprint, manual upgrade + full test suite
- [ ] Framework (Next.js/React): within 1 month of stable release
- [ ] Security: critical CVE → patch within 24 hours
- [ ] Log: dependency update decisions in ADR (Architecture Decision Records)

### T85. Feature flag system
**DoD:**
- [ ] Simple: environment variables (NEXT_PUBLIC_FF_*) for MVP
- [ ] Runtime: Unleash or LaunchDarkly if need user-level targeting (P2)
- [ ] UI: feature-flagged components use `<FeatureGate flag="new-graph-viewer">` wrapper
- [ ] Cleanup: flags removed within 2 releases after feature is GA
- [ ] Dashboard: list of active flags and their state (admin only)

### T86. Public roadmap
**DoD:**
- [ ] GitHub Projects board or Linear: 3 columns (Planned, In Progress, Done)
- [ ] Visible to: all authenticated users (read-only)
- [ ] Updated: monthly by product owner
- [ ] Items: feature title + 1-line description + target release
- [ ] Link from WebUI Settings page: "Roadmap →"

### T87. Deprecation policy
**DoD:**
- [ ] Rule: deprecated feature → warning in UI for 2 minor versions → removed in next major
- [ ] Visual: yellow banner on deprecated screens/features
- [ ] API: `Deprecation` header in HTTP response, documented in changelog
- [ ] Communication: release notes explicitly list deprecations
- [ ] Migration guide: for each deprecation, how to switch to replacement
