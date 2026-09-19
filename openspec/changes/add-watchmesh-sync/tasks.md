# Tasks: add-watchmesh-sync

Ordered by dependency; each task verifiable in one session.

## 1. Scaffold

- [x] 1.1 Init go.mod go1.24 with pgx/v5 + golang-migrate/migrate/v4 only (sync-config, sync-state, app-ops) — `go mod init watchmesh`, require `github.com/jackc/pgx/v5` + `github.com/golang-migrate/migrate/v4` (postgres, iofs, file), no other external deps; verify `go mod tidy && go list -m all`.
- [x] 1.2 Create dirs cmd/watchmesh + internal/{config,model,trakt,simkl,ryot,yamtrack,engine,store} + top-level migrations/ (app-ops) — empty package stubs compile; verify `go build ./...`.
- [x] 1.3 Add config.example.json with interval, connections, syncs (sync-config) — matches design §4 example including trakt_main/trakt_alt; verify `python3 -m json.tool config.example.json`; .gitignore already ok, no change.
- [x] 1.4 Re-run Context7 resolve+query for Go/Docker/pgx + all four watch APIs and pin golang/distroless versions (app-ops) — record library IDs + digests in nuances; verify Trakt limits live with 429 backoff planned.

## 2. Config + Model

- [x] 2.1 Implement internal/model WatchItem + Hash (service-connectors, sync-state) — IDs map, title/year/kind/action/watchedAt, sha1(kind|action|ids|watchedAt); verify `go test ./internal/model/`.
- [x] 2.2 Implement internal/config load with flag>env>file>default + ${VAR} expansion (sync-config) — interval, database_url, connections, syncs; verify `go test ./internal/config/`.
- [x] 2.4 Implement config home resolution --config > WATCHMESH_CONFIG > XDG > ~/.config/watchmesh > legacy ~/.watchmesh (sync-config) — first hit wins, writes go to XDG path; verify `go test ./internal/config/ -run TestHome`.
- [x] 2.3 Implement named reference validation failing fast on unknown source/target (sync-config) — duplicate types allowed under distinct names; verify `go test ./internal/config/ -run TestValidate`.

## 3. Trakt connector

- [x] 3.1 Implement trakt History GET /sync/history with trakt-api-key + Bearer + trakt-api-version 2 (service-connectors) — map to WatchItem without dropping watched_at; verify `go test ./internal/trakt/`.
- [x] 3.2 Implement trakt Push POST /sync/history + device-code auth helper (service-connectors, app-ops) — code/token endpoints, poll loop printing URL; verify `go test ./internal/trakt/ -run TestPush`.

## 4. Simkl connector

- [x] 4.1 Implement simkl History via GET /sync/activities then /sync/all-items with 10 GET/s limit (service-connectors) — incremental fetch on newer cursor only; verify `go test ./internal/simkl/`.
- [x] 4.2 Implement simkl Push POST /sync/history at 1 POST/s + PIN auth helper (service-connectors, app-ops) — pin/poll token flow; verify `go test ./internal/simkl/ -run TestPush`.

## 5. Ryot connector

- [x] 5.1 Implement ryot Push via Bearer POST {url}/backend/graphql ImportCompletedItems mutation (service-connectors) — target-first, TMDB-first ID lookup; verify `go test ./internal/ryot/`.
- [x] 5.2 Implement ryot History best-effort + token validation GET (service-connectors, app-ops) — history gaps never fail sync; verify `go test ./internal/ryot/ -run TestHistory`.
- [ ] 5.3 Introspect live Ryot GraphQL schema + auth form before finalizing field/enum names (service-connectors) — candidate mutation/inputs in design §7 are stubs; verify against running instance docs/token screen.

## 6. Yamtrack connector

- [x] 6.1 Implement yamtrack History GET /api/v1/media/{type}/ with limit/offset up to 200 following pagination.next (service-connectors) — Bearer or X-API-Key; verify `go test ./internal/yamtrack/`.
- [x] 6.2 Implement yamtrack Push as looped single POST/PATCH per item, no batch call (service-connectors) — {source:tmdb, media_id} mapping; verify `go test ./internal/yamtrack/ -run TestPush`.
- [ ] 6.3 Confirm live Yamtrack header form + history/push paths against running instance (service-connectors) — webhook/views + token screen are source of truth; design §7 payload is a stub until then.

## 7. Engine + Store

- [x] 7.1 Write migrations/000001_init.up.sql + .down.sql creating sync_state + seen_items (sync-state) — seq pair per design §5, down drops in reverse; verify pair applies/rolls back on ephemeral PG.
- [x] 7.2 Implement migrate-Up-on-boot before store open via iofs embed with --migrations-path file:// override (sync-state, app-ops) — ErrNoChange continues, dirty aborts non-zero naming version, never auto-Force; verify `go test ./internal/store/ -run TestMigrate`.
- [x] 7.3 Implement store LastRun/SetLastRun + Seen/MarkSeen via pgxpool on DATABASE_URL only (sync-state) — success upsert, failed sync writes nothing; verify `go test ./internal/store/ -short` with fakes or skip flag.
- [x] 7.4 Implement engine Sync: since→History→hash-diff→fan-out Push→record state (sync-engine) — per-target errors collected, hashes marked only for successful targets; verify `go test ./internal/engine/`.
- [x] 7.5 Implement engine idempotent rerun + empty-window no-op without cursor regression (sync-engine, sync-state) — zero Push calls on rerun; verify `go test ./internal/engine/ -run TestIdempotent`.

## 8. CLI + Ticker

- [x] 8.1 Implement cmd/watchmesh sync --config --sync dispatch wiring config+store+connectors (app-ops, sync-engine) — single run fan-out; verify `go run ./cmd/watchmesh sync --help` and one dry run.
- [x] 8.2 Implement serve ticker loop with time.NewTicker + signal.NotifyContext graceful stop (sync-engine, app-ops) — Sync per tick, no partial state write on cancel; verify `timeout 5s go run ./cmd/watchmesh serve --help`.
- [x] 8.3 Implement auth per connection printing URL + polling for Trakt/Simkl, token-validation GET for Ryot/Yamtrack (service-connectors, app-ops) — no inbound ports; verify `go run ./cmd/watchmesh auth --help`.

## 9. Docker / Compose / Makefile

- [x] 9.1 Write Dockerfile golang:1.24-bookworm CGO_ENABLED=0 build to distroless/static-debian12:nonroot (app-ops) — single static nonroot binary, migrations embedded via iofs so no COPY needed; verify `docker build -t watchmesh .`.
- [x] 9.2 Write compose.yml app + postgres:16-alpine with named volume (app-ops, sync-state) — app reaches postgres over internal network; verify `docker compose config`.
- [x] 9.3 Write docker-only Makefile build/dev/test/itest/cover/clean/migrate-new via pinned golang image (app-ops) — no host Go/PG needed, migrate-new scaffolds seq SQL pair; verify `make build && make test`.

## 10. Unit tests + Coverage gate

- [x] 10.1 Add httptest table tests per connector covering 200/401/500, malformed JSON, pagination (service-connectors, app-ops) — one _test.go per connector; verify `go test ./internal/trakt/ ./internal/simkl/ ./internal/ryot/ ./internal/yamtrack/`.
- [x] 10.2 Add engine table tests with fakes: diff, partial failure isolation, empty window (sync-engine, app-ops) — A/C succeed when B fails; verify `go test ./internal/engine/ -v`.
- [x] 10.3 Add cover target failing below 90% on internal/... excluding thin cmd (app-ops) — coverprofile + func output; verify `make cover`.

## 11. Integration

- [x] 11.1 Write compose.test.yml ephemeral postgres:16-alpine + tester service on shared network (app-ops, sync-state) — used only by itest; verify `docker compose -f compose.test.yml config`.
- [x] 11.2 Add store roundtrip TestMain applying DDL asserting cursor + dedupe end-to-end (sync-state, sync-engine) — LastRun/Seen cycle against ephemeral PG; verify `make itest`.

## 12. Docs

- [x] 12.1 Refresh README.md setup, config, CLI, make targets (app-ops) — new operator can sync/serve from scratch; verify markdown renders, commands copy-paste.
- [x] 12.2 Write AGENTS.md build/test workflows and connector notes (app-ops) — docker-only commands, coverage gate, live-instance confirmations for Ryot/Yamtrack; verify file exists at repo root.
