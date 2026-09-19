## Why

`seen_items` is keyed `(sync_name, item_hash)` globally: one hash, all targets. When targets A+C succeed but B fails, the hash is still marked seen, so the next run diffs it out for every target and B is never retried — the failing target silently loses items. The engine comment in `internal/engine/engine.go` claims "hashes marked only for successful targets", but `MarkSeen` takes no target, so that claim is false today: partial failure is data loss, not a retry.

## What Changes

- `seen_items` key gains a stable target identifier: `(sync_name, target, item_hash)` via a new seq migration `000002_target_seen` on top of `000001_init`.
- Store API gains a `target string` param: `Seen(ctx, sync, target, hash)` and `MarkSeen(ctx, sync, target, hash, watchedAt)` with matching queries.
- Engine computes a fresh set per target, pushes each target only its missing items (no Push when a target's fresh set is empty), records `MarkSeen` per target on that target's success, and advances the cursor to window-end only when every target fully succeeded (changed from the current any-succeeded rule, which is only safe with per-target keys).
- Target identifier is the connection name from config (e.g. `simkl_main`) — the stable routing key — never the connector type (two connections may share one type, e.g. `trakt_main`/`trakt_alt`).
- Existing `000001` rows are kept untouched: backfilled with `target=''` (default applied then dropped), never matched by per-target lookups, so the current History window re-delivers once — bounded by `last_run_at` — and is then marked per target.

## Capabilities

### New Capabilities

- None (no new capability; this re-keys existing delivery state).

### Modified Capabilities

- `sync-state`: per-target seen keys, `000002` migration + backfill rule, per-target store API.
- `sync-engine`: per-target fresh-set computation, exact-retry push rule, all-succeeded cursor rule.

## Impact

- New files at apply time: `migrations/000002_target_seen.up.sql` + `.down.sql` (exact SQL lives in `design.md` fenced blocks; no SQL files are written by this change).
- Code touched at apply time: `internal/store` (`Seen`/`MarkSeen` signatures + queries), `internal/engine` (`Sync` flow + `Target` identity), connector wiring must supply connection names to the engine.
- One-time bounded re-push of the current History window to all targets on the first run after upgrade: previously-lost items finally reach failed targets; healthy targets receive mostly-harmless duplicates they dedupe by IDs/`watched_at`.
- No new dependencies, no config format change, `sync_state` cursor shape unchanged, dirty-boot behavior unchanged, empty-window no-op unchanged.
