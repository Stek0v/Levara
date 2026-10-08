# Tasks

## 1. Browser authority and error behavior

- [x] 1.1 Implement imported-chat list/detail and owner-consent share/admin audience/revoke UI with canonical encoded selectors, stale response suppression and account reset. DoD: successful grant/revoke, forbidden mutation, failed reads/retry, empty/null, rapid selection and revoked open detail are truthful. Verify `LEVARA_API_URL=http://127.0.0.1:1 PLAYWRIGHT_PORT=3022 npx playwright test imported-chats.spec.ts`; preflight installed Node/Chromium, free Next port and no live backend target. Document current authority policy.
- [x] 1.2 Fix workspace exact-scope read digest/CAS and empty-text editing, task observation errors/lease expiry and sync failed/partial/running presentation. DoD: stale conflict preserves draft, scope change cannot reuse digest; denied/unavailable is not empty/ready; body200 domain errors remain failures. Verify focused `domain-states.spec.ts` on the same mocked preflight; document supported read-only Task surface.
- [x] 1.3 Include existing memory-delete/project context/git/share/i18n browser specs and new focused checks in curated command; update stale WebUI README. DoD: installed dependency versions and current backend authority documented, no live catalog invoked. Verify frozen `npm run lint`, `LEVARA_API_URL=http://127.0.0.1:1 npm run build` and curated `npm run test:e2e`.

## 2. Native browser acceptance

- [x] 2.1 Start a dedicated authenticated standalone SQLite backend on a free loopback port with private data; create isolated owner/admin/member fixtures. DoD: health, selected profile, identity and private SQL paths preflight verified; no existing localhost service touched. Verify local config-check, health and authenticated browser login.
- [x] 2.2 Prove real imported-chat owner share, administrator audience grant/revoke and colleague read denial after revocation, with reload/account change and private owner retention. DoD: actual backend responses/native state agree with browser; no route mocks replace the protected workflow. Verify dedicated native browser spec and log, private backend preflight as2.1.
- [x] 2.3 Complete the design domain acceptance matrix with observed supported browser success/empty/denied/stale/failed/reload/profile evidence; fix confirmed gaps with focused cases. DoD: every browser row has current proof or explicitly supported backend/operator-only surface, not a fabricated browser completion. Verify dedicated native specs and current existing native domain gates as needed; model-service readiness preflight before provider-dependent checks.

## 3. Release and integration

- [x] 3.1 Run `GOFLAGS='-p=1 -ldflags=-w' make release-artifact` after all Go mutations stop; inspect exact six binaries/profiles/LICENSE allowlist and binary metadata. DoD: local archive contains no source/dev tools, observed buildexit0, no publish/deploy. Preflight disk/free build environment; preserve any existing archive.
- [x] 3.2 Freeze combined diff, verify lint/build/curated/native browser checks, contract drift, OpenSpec strict and appropriate backend integration excluding user-forbidden TestMemoryREST. DoD: observed current-revision evidence and independent review accept originalT30; external prerequisites and historical failures remain explicit. Update roadmap only after all required proof.
