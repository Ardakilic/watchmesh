# Tasks: per-target-seen

Ordered by dependency; each verifiable in one session. Spec text only in this change — Go/SQL files are written at apply time.

## 1. Migration pair

- [ ] 1.1 Create `migrations/000002_target_seen.up.sql` + `.down.sql` exactly as in `design.md` §3 (sync-state) — verify `make itest` passes on a fresh DB and `migrate down 1 && migrate up 1` round-trips on ephemeral PG.
- [ ] 1.2 Verify `000001→000002` upgrade with pre-existing rows on ephemeral PG (sync-state) — seed `(sync, hash, watched_at)` rows under `000001`, apply `000002`, assert `watched_at` preserved, old rows carry `target=''`, and per-target `Seen(sync, <real-name>, hash)` returns false.

## 2. Store

- [ ] 2.1 Change `Seen`/`MarkSeen` to take `target string` with the queries in `design.md` §4 (sync-state) — verify `go test ./internal/store/ -run TestStoreRoundtrip` with a per-target roundtrip (mark for A, assert unseen for B, mark for B, assert both seen).
- [ ] 2.2 Extend `store_test.go` error paths for the new signatures (sync-state) — closed-pool `Seen`/`MarkSeen` with target still fail; verify `go test ./internal/store/ -short`.

## 3. Engine

- [ ] 3.1 Add `Name()` (connection name) to `engine.Target` and compute a fresh set per target, skipping Push on empty sets (sync-engine) — verify `go test ./internal/engine/ -v` with fakes: A caught-up (zero Pushes) while B receives the full window.
- [ ] 3.2 Implement all-succeeded cursor rule + per-target `MarkSeen` (sync-engine) — verify `go test ./internal/engine/ -run TestPartial` covers: B fails → cursor held; rerun pushes ONLY to B (zero Pushes to A/C); all succeed → cursor advances to window-end; `MarkSeen` failure counts as target failure.
- [ ] 3.3 Wire connection names into engine targets at `cmd/watchmesh` construction (sync-engine, sync-state) — two same-type connections (`trakt_main`/`trakt_alt`) stay independent; verify with a fake-target dry run or unit test on the wiring.

## 4. Gates

- [ ] 4.1 Run full suites + coverage gate (app-ops) — verify `make test && make itest && make cover` (90% on `./internal/...`) all green.
