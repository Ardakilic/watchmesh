## ADDED Requirements

### Requirement: Incremental fan-out sync
The engine SHALL run Sync by fetching History since the stored cursor, diffing hashes against seen_items, fanning out Push per target, then recording state.

#### Scenario: New items reach all targets
WHEN source returns items unseen since the cursor
THEN each target MUST receive exactly those items and state MUST advance.

### Requirement: Idempotent rerun
The engine SHALL push nothing when a rerun covers an already-synced window.

#### Scenario: Same window reruns clean
WHEN Sync reruns with no new history since the cursor
THEN zero Push calls MUST occur and the cursor MUST NOT regress.

### Requirement: Per-target failure isolation
The engine SHALL NOT let one failing target block pushes to the others.

#### Scenario: One target errors
WHEN target B fails while A and C succeed
THEN A and C MUST still receive items and the error MUST be reported per target.

### Requirement: Interval serve loop
The serve loop SHALL run Sync every interval until signal cancellation.

#### Scenario: Ticker with cancel
WHEN serve starts with an interval and the signal context cancels
THEN Sync MUST run on each tick and stop promptly without a partial state write.
