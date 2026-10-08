## ADDED Requirements

### Requirement: Known committed files are verified

The verified backup verifier SHALL compare every known committed active workspace file with the restored archive inventory, including files without chunks. It SHALL reject inconsistent namespace, unsafe paths, missing objects and digest mismatch before reporting successful verification. Historical and unknown legacy inventory SHALL retain their existing compatibility behavior.

#### Scenario: Empty committed file round-trip

- **WHEN** an active known workspace inventory contains an empty committed Markdown file with no chunks
- **THEN** native archive creation and sandbox verification succeed only if the restored file exists with its committed digest

#### Scenario: Internally valid archive contains wrong committed bytes

- **WHEN** archive checksums are valid but a known active file digest differs from the committed workspace inventory
- **THEN** verification fails and no successful receipt replaces a previous successful backup

### Requirement: Native restore is independent

Verification SHALL restore into a disposable independent root and SQL instance/copy and validate native SQL references, raw/structured objects, workspace artifacts and index recovery. Original roots SHALL NOT supply missing restored bytes.

#### Scenario: Original roots are unavailable

- **WHEN** a verified archive is checked after original roots become unavailable
- **THEN** all required restored references resolve within the sandbox and verification is based on its native recovered state
