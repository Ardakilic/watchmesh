# AGENTS.md

## Build / test (docker-only)

Host needs only Docker. All Go runs in the pinned `golang:1.27.1-bookworm` container; PG only via compose.

```sh
make build        # docker build -t watchmesh .
make dev          # compose.yml + compose.dev.yml up --build (local build); compose.yml alone pulls ghcr.io/ardakilic/watchmesh:latest
make test         # go test ./... -count=1 (containerized)
make itest        # compose.test.yml: ephemeral postgres:16-alpine + tester
make cover        # 90% gate on ./internal/...
make clean        # rm artifacts; compose down -v
make migrate-new NAME=add_x
```

- `go test ./...` on the host works only if you have Go + PG; prefer `make test` / `make itest`.
- `make test` = unit (fakes + `httptest`, PG tests skip/short). `make itest` = store roundtrip against ephemeral PG (`TestMain` applies DDL, asserts cursor + dedupe). `make cover` = `go test -coverprofile=c.out ./internal/...` + `go tool cover -func`; fails below 90%.

## Coverage gate

90% on `./internal/...`, `cmd/` excluded (thin wiring). Keep connector table tests (200/401/500, malformed JSON, pagination) + engine fakes (diff, partial-failure isolation, empty-window no-op) green via `make cover`.

## Connectors — live-verify notes

- Trakt: confirmed shapes in `design.md` §7. Still verify rate limits live; keep 429 backoff (`Retry-After`, retry once) in every connector.
- Simkl: never timer-poll `/sync/all-items` — always dirty-check `/sync/activities` first or the `client_id` risks suspension. Respect PIN poll `interval`; 10 GET/s, 1 POST/s.
- Ryot (task 5.3, STUB): introspect the live GraphQL schema before finalizing — candidate `updateSeenHistory`/`UpdateSeenInput`, `metadataId: tmdb://…`. Confirm auth header + enums on the token screen. Target-first; history gaps must never fail sync.
- Yamtrack (task 6.3, STUB): inspect live webhook/`views.py` + token screen — confirm header form (`Bearer` vs `X-API-Key` vs `?token=`), history GET existence, push path. Single-POST loop, no batch assumption. CSV import stays the bulk fallback.

## Migrations

Embed-first (`source/iofs`); `--migrations-path` is dev-only (`file://` override). Dirty version fails boot with version in logs — recover with manual `migrate force <last_good>` + restart, never auto-`Force`.

## Ponytail rules

Stdlib-first (`net/http`, `encoding/json`, `flag`, `log/slog`, `time`, `httptest`, `embed`, `os`). Only two external deps: `pgx/v5` + `golang-migrate/migrate/v4`. No new deps without asking. Shortest diff wins; no speculative abstractions or scaffolding for later.

## Specs

Source of truth: `openspec/changes/add-watchmesh-sync/` — `design.md`, `nuances.md`, `tasks.md`, `specs/*/spec.md`.
