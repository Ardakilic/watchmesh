# AGENTS.md

## Commands (docker-only; host needs only Docker)

```sh
make build        # docker build -t watchmesh .
make dev          # compose.yml + compose.dev.yml up --build (local build); compose.yml alone pulls ghcr.io/ardakilic/watchmesh:latest
make test         # go test ./... -count=1 in pinned golang:1.27.1-bookworm
make itest        # compose.test.yml: ephemeral postgres:16-alpine + tester (store roundtrip)
make cover        # 90% gate on ./internal/... (cmd/ excluded, thin wiring)
make clean        # rm artifacts; compose down -v
make migrate-new NAME=add_x   # scaffold migrations/NNNNNN_add_x.{up,down}.sql
```

CI runs on PRs + main: `go vet ./...`, `gofmt -l` empty, `go test ./... -count=1`, plus the coverage pair directly (`go test -coverprofile=c.out ./internal/...` + `go tool cover`, same 90% gate as `make cover`). Match it before opening a PR.

## Layout

`cmd/watchmesh/` (CLI: `sync|serve|auth`) → `internal/engine/` (`Sync` fans out to connectors, records state via `Store`) → `internal/{trakt,simkl,ryot,yamtrack}/` (`Source`/`Target`) + `internal/store/` (`Store`: pgx cursors + seen hashes). Shared types in `internal/model/`, config in `internal/config/`. Specs: `openspec/changes/add-watchmesh-sync/` (`design.md`, `nuances.md`, `tasks.md`).

## Go rules

- Stdlib-first (`net/http`, `encoding/json`, `flag`, `log/slog`, `time`, `httptest`, `embed`, `os`). Only deps: `pgx/v5` + `golang-migrate/migrate/v4`. Ask before adding any.
- `gofmt` + `go vet` clean, always. Doc comment on every exported symbol.
- Errors: wrap with `%w`, no `failed to` prefix, match with `errors.Is`:

```go
return fmt.Errorf("sync %q: %w", name, err)
```

- `ctx` first, thread cancellation; `signal.NotifyContext` at edges only. Structured logs via `log/slog` keyvals.
- Small interfaces, inject fakes (`HTTP *http.Client`, nil ⇒ default). Shortest diff wins; no speculative abstractions.
- Tests: table-driven, `httptest` fakes (never live APIs), edge cases in `edge_test.go`. Keep connector tables (200/401/500, malformed JSON, pagination) + engine fakes (diff, partial-failure isolation, empty-window no-op) green via `make cover`.

## Engine + connector contracts (do not break)

- State is keyed by **connection name**, never connector type. `Push` returns the delivered subset — only delivered items are marked seen; any failure/skip holds the cursor; empty windows touch no state.
- History read gaps return `empty, nil` — never fail a sync. Matching is TMDB-based; items without a TMDB id are skipped on push.
- Every connector retries **once** on 429, waiting `Retry-After` (default 1s).
- Simkl: always dirty-check `GET /sync/activities` before `/sync/all-items` — timer-polling it risks `client_id` suspension.
- Ryot/Yamtrack are STUBS: target-first, history best-effort. Introspect live instances before finalizing field maps.

## Migrations

Embed-first (`source/iofs`); `--migrations-path` is dev-only. Never edit a committed migration — new change, new sequence. Dirty DB fails boot; recover manually with `migrate force <last_good>` + restart, never auto-`Force`.

## Boundaries

- Always: `gofmt`/`vet`/relevant tests before finishing; reuse existing helpers.
- Ask first: new deps, schema changes, CLI surface changes, real-API calls, commit/push/PR.
- Never: commit secrets or real tokens (`.env`, `config.json`); auto-`Force` a dirty DB; timer-poll Simkl `/sync/all-items`; add a dep for what stdlib covers.

## Commits

Conventional commits, lowercase subject: `feat:`, `fix:`, `docs:`, `chore:` (e.g. `fix: simkl source emits only watched entries`).
