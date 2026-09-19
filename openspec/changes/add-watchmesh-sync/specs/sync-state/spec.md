## ADDED Requirements

### Requirement: Postgres connection
State SHALL connect via pgxpool using DATABASE_URL.

#### Scenario: Connection configured
WHEN DATABASE_URL is set
THEN the pool MUST connect exclusively through it.

### Requirement: State schema
The database SHALL provide sync_state keyed by sync_name with last_run_at and last_success_at, plus seen_items keyed by sync_name and item_hash with watched_at.

#### Scenario: Tables exist
WHEN migrations run
THEN both tables MUST exist with the stated primary keys.

### Requirement: Success upsert
State SHALL upsert cursors and hashes only after a sync succeeds.

#### Scenario: Failed sync writes nothing
WHEN a sync fails
THEN last_success_at and seen_items MUST remain unchanged.

### Requirement: Stable item hash
The item hash SHALL derive deterministically from stable IDs plus watched_at.

#### Scenario: Identical replays match
WHEN the same item with the same IDs and watched_at reappears
THEN it MUST produce the identical hash and be skipped.

### Requirement: Versioned migrations
Migrations SHALL live as seq-numbered `migrations/NNNNNN_name.up.sql` + `.down.sql` pairs run by `golang-migrate/migrate` (postgres + iofs embed-first, file:// dev override).

#### Scenario: Fresh database migrates
WHEN the app boots against an empty database
THEN all pending up migrations MUST apply in order before any sync runs.

### Requirement: Migrate on boot
The app SHALL run `Up` on every boot and treat `ErrNoChange` as success.

#### Scenario: Already current boots clean
WHEN the database is at the latest version
THEN boot MUST continue without error and open the store.

### Requirement: Dirty fails fast without auto-force
A dirty database SHALL abort boot with the version in the error; the app MUST never auto-`Force`.

#### Scenario: Failed migration blocks start
WHEN the schema is dirty at version N
THEN boot MUST exit non-zero naming N and docs MUST describe manual `force <last_good>` recovery.
