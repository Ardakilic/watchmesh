## MODIFIED Requirements

### Requirement: State schema
The database SHALL key `seen_items` by `(sync_name, target, item_hash)` with `watched_at`, where `target` is the connection name from config.

#### Scenario: Per-target rows coexist
WHEN target A and target B both deliver the same item hash under one sync
THEN two rows `(sync, A, hash)` and `(sync, B, hash)` MUST exist independently.

#### Scenario: Lookups are target-scoped
WHEN `Seen` is checked for target B on a hash only target A delivered
THEN it MUST return false.

### Requirement: Success upsert
State SHALL record a `(sync_name, target, item_hash)` row only after that target reports delivery, and SHALL leave a failed target's rows unchanged.

#### Scenario: Failed target writes nothing
WHEN target B's push fails while A and C succeed
THEN rows for A and C MUST be written and no row for B MUST appear.

#### Scenario: Empty window writes nothing
WHEN no fresh items exist for any target
THEN `seen_items` and `sync_state` MUST remain unchanged.

## ADDED Requirements

### Requirement: Target upgrade migration
Migration `000002_target_seen` SHALL apply cleanly on both fresh databases and `000001` databases, preserving every existing `watched_at` value inside migrate's transaction.

#### Scenario: Fresh database migrates
WHEN the app boots against an empty database
THEN `000001` and `000002` MUST apply in order before any sync runs and `seen_items` MUST carry the `(sync_name, target, item_hash)` primary key.

#### Scenario: Upgrade preserves history values
WHEN `000002` applies over a `000001` database holding rows
THEN every pre-existing row MUST keep its `sync_name`, `item_hash`, and `watched_at` unchanged.

#### Scenario: Dirty still fails fast
WHEN the schema is dirty at version N during upgrade
THEN boot MUST still exit non-zero naming N with manual `force <last_good>` recovery and MUST never auto-`Force`.

### Requirement: Backfill keeps existing rows
The upgrade SHALL keep pre-existing rows untouched with `target=''` (backfill default, dropped immediately after) and per-target lookups SHALL never match them, so the current History window naturally re-delivers once and is then marked per target.

#### Scenario: Old rows cause one bounded re-delivery
WHEN a `000001` row `(sync, hash)` exists and `000002` upgrades it to `(sync, '', hash)`
THEN the next run MUST treat the hash as unseen for every real target and, after delivery, MUST record proper `(sync, target, hash)` rows.

#### Scenario: Sentinel rows are never read as delivered
WHEN any per-target `Seen(sync, target, hash)` runs with a real connection name
THEN rows with `target=''` MUST NOT satisfy it.
