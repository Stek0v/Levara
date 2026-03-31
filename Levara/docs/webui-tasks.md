# Levara WebUI — Task List

## Результат анализа Cognee Frontend

**Cognee frontend:** Next.js 16 + React 19 + Tailwind CSS 4 + Three.js
**79 UI компонентов, 12 страниц/routes, 48 API endpoints**
**Совместимость с Levara backend: 94% (45/48 endpoints)**

---

## Phase 0: Backend совместимость (перед WebUI)

| # | Задача | Приоритет | LOC |
|---|--------|----------|-----|
| W0.1 | Алиас `POST /search/` → `/search/text` | 🔴 | 5 |
| W0.2 | Алиас `POST /notebooks/:id/:cellId/run` → `/:id/cells/:cellId/run` | 🔴 | 5 |
| W0.3 | Endpoint `GET /users?email=` для dataset sharing | 🟠 | 30 |
| W0.4 | CORS: добавить `http://localhost:3000` (уже есть) | ✅ Done | 0 |

## Phase 1: Проект + Layout (1-2 дня)

| # | Задача | Что делать |
|---|--------|-----------|
| W1.1 | Init проект `~/src/levara-webui` | Next.js 16 + Tailwind 4 + TypeScript |
| W1.2 | Layout: Header + Sidebar + Main | `src/app/layout.tsx` + `src/ui/Layout/Header.tsx` |
| W1.3 | Routing: 8 pages (dashboard, search, visualize, collections, ontologies, mcp-status, account) | App Router |
| W1.4 | API client: fetch wrapper | `src/utils/fetch.ts` — JWT + API Key auth |
| W1.5 | Icons: 10 базовых SVG | Search, Plus, Minus, Close, Caret, etc. |
| W1.6 | UI elements: Button, Input, Select, Modal, Accordion | `src/ui/elements/` |

## Phase 2: Dashboard + Datasets (2-3 дня)

| # | Задача | Что делать |
|---|--------|-----------|
| W2.1 | Dashboard layout: left sidebar + right content | `src/app/dashboard/Dashboard.tsx` |
| W2.2 | Datasets accordion: list, create, delete | `DatasetsAccordion.tsx` + API calls |
| W2.3 | File upload: drag-drop + button | `AddDataToCognee.tsx` + `POST /add` |
| W2.4 | Cognify trigger + status polling | `cognifyDataset.ts` + progress bar |
| W2.5 | Dataset data viewer | `GET /datasets/:id/data` → table |

## Phase 3: Search + Chat (2-3 дня)

| # | Задача | Что делать |
|---|--------|-----------|
| W3.1 | Search page: message list + input | Chat-style UI с auto-scroll |
| W3.2 | Search type selector | Dropdown: AUTO, CHUNKS, HYBRID, RAG_COMPLETION, GRAPH_COMPLETION, etc. |
| W3.3 | Top-K parameter | Slider 1-100 |
| W3.4 | Search results rendering | Markdown + JSON + source highlighting |
| W3.5 | Search history | `GET /search/` → previous queries |

## Phase 4: Graph Visualization (3-5 дней)

| # | Задача | Что делать |
|---|--------|-----------|
| W4.1 | 2D force graph | `react-force-graph-2d` basic visualization |
| W4.2 | Node coloring by type | Entity, Temporal, Document, etc. |
| W4.3 | Node search/filter | Text input → highlight matching nodes |
| W4.4 | Dataset-scoped graph | `GET /datasets/:id/graph` → visualize |
| W4.5 | 3D graph (optional) | Three.js rendering с metaballs (advanced) |

## Phase 5: Notebooks (2-3 дня)

| # | Задача | Что делать |
|---|--------|-----------|
| W5.1 | Notebook list + create/delete | Accordion в sidebar |
| W5.2 | Cell editor: code + markdown | Auto-expanding textarea |
| W5.3 | Cell execution | `POST /notebooks/:id/cells/:cellId/run` → output |
| W5.4 | Cell controls: collapse, move, delete | Header с кнопками |
| W5.5 | Markdown preview | `react-markdown` для markdown cells |

## Phase 6: Collections + Ontologies (1-2 дня)

| # | Задача | Что делать |
|---|--------|-----------|
| W6.1 | Collections page: list, create, delete | Table с metadata (dim, model, records) |
| W6.2 | Re-embed trigger + status | `POST /reembed` + progress |
| W6.3 | Ontologies page: upload, list, delete | File upload для OWL/RDF/TTL |

## Phase 7: System Status + Account (1 день)

| # | Задача | Что делать |
|---|--------|-----------|
| W7.1 | MCP Status page | `GET /health/details` → service cards с green/red |
| W7.2 | MCP tools list | `POST /mcp` initialize → tools/list |
| W7.3 | Account page | User info + API key management |
| W7.4 | Settings | LLM/embed provider config display |

## Phase 8: Auth + Polish (1-2 дня)

| # | Задача | Что делать |
|---|--------|-----------|
| W8.1 | Login page | Email/password → JWT + cookie |
| W8.2 | API Key auth | X-API-Key header support |
| W8.3 | Protected routes | Redirect to login if no token |
| W8.4 | Loading states | Skeleton + spinners |
| W8.5 | Error handling | Toast notifications |
| W8.6 | Responsive design | Mobile-friendly layout |

---

## Оценки

| Phase | Дни | Компоненты |
|-------|-----|-----------|
| Phase 0: Backend compat | 0.5 | 3 endpoint fixes |
| Phase 1: Project + Layout | 1-2 | 15 files |
| Phase 2: Dashboard | 2-3 | 10 files |
| Phase 3: Search | 2-3 | 8 files |
| Phase 4: Graph | 3-5 | 10 files |
| Phase 5: Notebooks | 2-3 | 8 files |
| Phase 6: Collections | 1-2 | 5 files |
| Phase 7: Status | 1 | 4 files |
| Phase 8: Auth + Polish | 1-2 | 6 files |
| **Итого** | **12-21 дней** | **~66 файлов** |

## Технологический стек

```
Next.js 16 (App Router)
React 19
TypeScript 5
Tailwind CSS 4
react-force-graph-2d (graph)
react-markdown (notebooks)
httpx / fetch API
```

## API Base URL

```
NEXT_PUBLIC_LEVARA_API_URL=http://localhost:8081/api/v1
```

Levara backend proxy через `next.config.mjs`:
```javascript
async rewrites() {
  return [
    { source: '/api/:path*', destination: 'http://localhost:8081/api/:path*' },
    { source: '/health/:path*', destination: 'http://localhost:8081/health/:path*' },
    { source: '/mcp', destination: 'http://localhost:8081/mcp' },
  ];
}
```
