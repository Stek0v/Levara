# Coding workflow

1. Define behavior and regression boundaries before planning steps.
2. Locate callers and tests before changing a public contract.
3. Prefer a failing test or other reproducible baseline receipt.
4. Record every code mutation under a new workspace revision.
5. Run focused tests first, then the repository validation required by DoD.
6. Do not pass a step by weakening assertions, deleting tests, or hiding failures.
7. Record build, lint, type-check, and test receipts separately when they satisfy different criteria.
8. Require a reviewer for migrations, public API changes, security-sensitive work, or production behavior.
