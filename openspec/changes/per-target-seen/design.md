## Context

`seen_items` is keyed `(sync_name, item_hash)` globally (`migrations/000001_init`, design §5 of `add-watchmesh-sync`), while `engine.Sync` fans out one fresh set to N targets (`internal/engine/engine.go`). The code comment claims hashes are "marked seen only for targets whose Push succeeded", but `Store.MarkSeen(ctx, sync, hash, watchedAt)` (`internal/store/store.go`) takes no target — so when A+C succeed and B fails, B's items are marked seen anyway and never retried. `model.WatchItem.Hash()` (sha1 over IDs + `watchedAt`) stays the item half of the key; only the delivery half changes.

## Goals / Non-Goals

**Goals:**

- Track delivery per `(sync, target)` so partial failure retries exactly the missing targets.
- Upgrade `000001` databases without data loss or guessing: keep old rows, re-deliver the bounded current window once.
- Keep the change to one migration, one composite key, and the two existing `Store` methods plus a `Target` identity.

**Non-Goals:**

- No per-target cursor table (`sync_state` shape unchanged; the single cursor plus per-target seen rows is sufficient).
- No attribution guessing (never expand old rows into per-target rows — we cannot know which targets actually received them).
- No poison-item quarantine or auto-skip (a permanently failing target holds the cursor; operator action, see Risks).
- No config format change; empty-window no-op and dirty-boot behavior unchanged.

## Decisions

### 1. One table, composite key — not a table per target

`seen_items` keeps all rows; the PK becomes `(sync_name, target, item_hash)`. A separate table per target would require DDL on every config change (DDL is not config) and one query shape per target; one table keeps migrations bounded and queries uniform.

### 2. Target identifier is the connection name

`target` stores the connection name from config (`simkl_main`), the stable routing key already used for source/target resolution — never the connector type, since two connections may share one type (`trakt_main`/`trakt_alt` both report `Name() == "trakt"` today). `engine.Target` therefore gains `Name() string` whose value MUST be the connection name; wiring (`cmd/watchmesh`) supplies it at construction/wrapping time. Renaming a connection intentionally resets its delivery state (re-delivers once).

### 3. Migration `000002_target_seen` (exact)

Up — one file, applied by migrate inside its transaction under its advisory lock, so concurrent boots are safe; the `ALTER`s take a brief `ACCESS EXCLUSIVE` lock but `seen_items` is tiny sync metadata:

```sql
-- 000002_target_seen.up.sql: per-target delivery tracking.
-- Existing rows are kept untouched: the backfill default assigns them
-- target='', then the default is dropped so all future writes must supply
-- a real connection name. '' rows never match per-target lookups, so the
-- current History window (bounded by last_run_at) re-delivers exactly once
-- and is then marked per target. watched_at column and values are carried
-- over verbatim.
ALTER TABLE seen_items ADD COLUMN target TEXT NOT NULL DEFAULT '';
ALTER TABLE seen_items ALTER COLUMN target DROP DEFAULT;
ALTER TABLE seen_items DROP CONSTRAINT seen_items_pkey;
ALTER TABLE seen_items ADD CONSTRAINT seen_items_pkey PRIMARY KEY (sync_name, target, item_hash);
```

Down — symmetric reverse, emergency use only:

```sql
-- 000002_target_seen.down.sql
ALTER TABLE seen_items DROP CONSTRAINT seen_items_pkey;
ALTER TABLE seen_items DROP COLUMN target;
ALTER TABLE seen_items ADD CONSTRAINT seen_items_pkey PRIMARY KEY (sync_name, item_hash);
```

Rollback caveat, stated plainly: downgrading after new per-target writes exist fails loudly on the restored `(sync_name, item_hash)` PK when two targets delivered the same hash — the operator dedupes first (or stays on `000002`); it never silently drops rows.

### 4. Store API (exact shape at apply time)

```go
Seen(ctx context.Context, syncName, target, hash string) (bool, error)
MarkSeen(ctx context.Context, syncName, target, hash string, watchedAt time.Time) error
```

```sql
-- Seen
SELECT 1 FROM seen_items WHERE sync_name=$1 AND target=$2 AND item_hash=$3;
-- MarkSeen (idempotent)
INSERT INTO seen_items(sync_name,target,item_hash,watched_at) VALUES($1,$2,$3,$4)
  ON CONFLICT(sync_name,target,item_hash) DO NOTHING;
```

No extra index: the PK's leftmost columns `(sync_name, target)` serve every lookup. `LastRun`/`SetLastRun` are untouched.

### 5. Engine flow (exact shape at apply time)

```go
since := store.LastRun(sync)          // unchanged
windowEnd := now()                    // unchanged, captured before History
items := source.History(ctx, since)   // unchanged; gaps still non-fatal
for each target t (by connection name):
    fresh[t] = { it in items : !store.Seen(sync, t.Name(), it.Hash()) }
    if fresh[t] empty: continue       // no Push, counts as succeeded
    if err := t.Push(ctx, fresh[t]): record per-target error; continue
    for it in fresh[t]: if MarkSeen(sync, t.Name(), it.Hash(), it.WatchedAt) fails:
        record error; t counts as failed
if every target succeeded (empty fresh sets count):
    store.SetLastRun(sync, windowEnd)
return joined per-target errors       // unchanged shape
```

Cursor rule change, stated plainly: today the cursor advances when *any* target succeeds; from here it advances only when *all* succeed. The old rule plus a global seen key is exactly the data-loss bug (cursor moves past B's items while the key pretends B got them). The new rule plus per-target keys gives exact retry: a held cursor re-fetches the same window and per-target diffs reduce the push to only the still-missing targets.

## Risks / Trade-offs

- A down target holds the cursor, so the History window regrows until it recovers; each run re-filters via cheap indexed `Seen` lookups, but the source fetch grows — accepted, and a permanently rejecting (poison) item blocks progress until the operator intervenes (no auto-quarantine by design).
- First run after upgrade re-pushes the current window to healthy targets too (bounded by the cursor; duplicates dedupe downstream by IDs/`watched_at`) — the price of never guessing attribution.
- `MarkSeen`-after-`Push` keeps at-least-once semantics: a crash between Push and MarkSeen re-pushes once; targets already tolerate this today.
