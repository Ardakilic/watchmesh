## MODIFIED Requirements

### Requirement: Incremental fan-out sync
The engine SHALL diff history per target against that target's seen rows and push each target only its own missing items, skipping Push entirely for targets with nothing missing.

#### Scenario: Each target receives only its missing items
WHEN target A already delivered hashes `{h1,h2}` and target B delivered nothing
THEN A MUST receive only post-`h2` items and B MUST receive the full fresh window.

#### Scenario: Fully-caught-up target gets no Push
WHEN every fresh-window hash is already seen for target A but not for target B
THEN zero Push calls MUST occur for A and B MUST still receive its missing items.

### Requirement: Per-target failure isolation
The engine SHALL record delivery per target, hold the cursor back unless every target fully succeeded, and rerun pushes ONLY to still-missing targets — never re-pushing to already-delivered ones.

#### Scenario: Partial failure retries exactly the missing target
WHEN target B fails while A and C succeed
THEN the cursor MUST NOT advance, and the next run MUST push the missing items to B only, with zero Push calls to A and C.

#### Scenario: Full success advances the cursor
WHEN every target's Push and all its `MarkSeen` writes succeed
THEN the cursor MUST advance to the window-end captured before History.

#### Scenario: MarkSeen failure counts as target failure
WHEN target A's Push succeeds but any of its `MarkSeen` writes fails
THEN A MUST count as failed for cursor purposes; rows for its successfully-written items MUST persist, and only items whose `MarkSeen` writes failed MUST be re-pushed on the next run (writes are independent, not transactional).

## ADDED Requirements

### Requirement: Target identity
The engine SHALL identify each delivery target by its connection name from config, which MUST be stable across restarts and MUST never be the connector type.

#### Scenario: Same-type connections stay distinct
WHEN config defines `trakt_main` and `trakt_alt` (both type `trakt`)
THEN their seen rows and fresh sets MUST be fully independent.

#### Scenario: Rename resets delivery state
WHEN a connection is renamed in config
THEN its items MUST be treated as unseen under the new name and re-delivered once.
