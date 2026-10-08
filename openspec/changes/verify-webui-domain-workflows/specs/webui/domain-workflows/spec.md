# Spec Delta

## Purpose

Present supported Levara product workflows in the browser with current server authority and truthful loading, failure, conflict and revocation states.

## ADDED Requirements

### Requirement: Imported conversation access
The browser SHALL list and read only server-authorized imported conversations using canonical chat identity. Empty lists, unavailable reads and revoked access SHALL remain distinct.

#### Scenario: Selection and revoked transcript
- **WHEN** a user changes selection or a current detail read is denied
- **THEN** previous transcript content is cleared and late responses cannot restore it.

#### Scenario: Account transition
- **WHEN** the verified account changes
- **THEN** the browser discards prior imported conversation state before publishing the new account's data.

### Requirement: Project chat collaboration
The browser SHALL support owner-consent project attachment and authorized owner/admin detachment through existing server checks, linking project audience management to the project. It SHALL NOT infer authority from client owner hints or global superuser status.

#### Scenario: Administrator-controlled audience
- **WHEN** a project administrator changes colleague grants or detaches a shared conversation
- **THEN** subsequent colleague reads obey the current server grant and the owner retains the private source.

#### Scenario: Rejected sharing action
- **WHEN** sharing or detachment is denied or fails
- **THEN** the browser displays the failure without claiming the action succeeded.

### Requirement: Truthful domain workflow states
Supported browser workflows SHALL display server denial, stale revision conflicts, failed task steps and failed or partial sync results without reporting completion solely from HTTP success.

#### Scenario: Workspace conflict
- **WHEN** a save encounters a stale workspace revision
- **THEN** unsaved text remains recoverable and the user can refresh the current server revision.

#### Scenario: Task and sync failure
- **WHEN** a task step fails or sync returns an error or partial type result
- **THEN** the browser shows the actual failed or partial state.

### Requirement: Release contents
The release artifact SHALL contain exactly the approved product binaries, runnable profile presets and license without development tools or frontend source.

#### Scenario: Local release inspection
- **WHEN** the release archive is built locally
- **THEN** its contents match the approved allowlist and its binaries can be inspected without deploying services.
