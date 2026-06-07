# Orders Database Schema

## Migration Safety

Postgres migrations for the orders database must add nullable columns before
backfilling data. Schema changes that lock the checkout table need a maintenance
window.

## Ownership

The platform database team owns indexes, migrations, and rollback scripts for
orders storage.
