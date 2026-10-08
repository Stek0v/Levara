# Design

## Existing native path

CreateVerifiedBackup holds the offline writer lease, snapshots SQL and exact filesystem inventory, writes a staging archive, and invokes VerifyArchive before publishing archive/receipt. VerifyArchive restores into a fresh disposable filesystem root and private PostgreSQL cluster or SQLite copy, rebases references, and checks objects and native WAL/lexical recovery. Reuse this path.

## Workspace consistency

Known active Manifest.Files entries must agree with archive inventory bytes even when no active chunks exist. Validate project/branch/path/generation, safe canonical paths and digests using the committed inventory semantics. Historical generations do not require their files to equal current live bytes. Legacy absent/unknown inventory remains compatible and is checked through existing active chunks. Canonical manifest precedence over legacy files is retained.

## Failure and scope

Internally valid checksums cannot replace semantic reference checks. Repacked wrong references, missing referenced objects and wrong known inventory digests must fail before publishing success. Test roots and databases remain isolated. No general backup framework or new restore entrypoint is needed.

## Acceptance

Run actual native Create→Verify restoration after original roots become unavailable; cover current schema/retained artifacts and both SQL dialects. Retain failing evidence and actual command exits. Local native proof does not certify external encrypted storage providers.

## Native application and interruption proof

Real workspace publication stores bare SHA256 hex; historical fixtures use a single sha256: prefix. Compare either exact representation against verified archive hashes, normalize inventory/chunk comparison only, and retain archive/manifest bytes unchanged.

Current-schema SQLite and private PostgreSQL restores exercise native ingestion and retained workspace generations. The restored trusted-local application proves nonempty BM25 search, current generation citation fields and exact read from the cited restored Markdown path, including an empty file. This does not certify every search strategy or authenticated transport.

A Darwin/Linux child process runs actual archive creation. The test observes nonzero partial archive bytes, confirms native SIGSTOP, kills the process, and checks prior successful archive/receipt and OS lease release before a new verified backup. This covers process death during archive writing; power-loss durability and cleanup of stale temporary artifacts are not claimed.
