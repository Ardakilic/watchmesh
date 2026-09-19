## Why

Watch history is scattered across Trakt, Simkl, Ryot, and Yamtrack with no single sync mesh. A tiny Go daemon that pulls from one named source and fans out to N named targets, on an interval, with Postgres cursors for idempotency, solves it without manual CSV imports.

## What Changes

- New `watchmesh` Go CLI: `sync` (once), `serve` (interval ticker loop), `auth` (device/PIN helper printing URLs + polling).
- JSON+env config with named `connections` and `syncs[]` (`source` + `targets[]`); same account type can appear twice (e.g. `trakt_main`, `trakt_alt`).
- One `Source`/`Target` Go interface; Trakt, Simkl, Ryot, Yamtrack implement it. Trakt is canonical source for v1; all four are valid targets; Simkl also valid source.
- Minimal Postgres state (2 tables) for last-run cursors + seen hashes so reruns never duplicate.
- Dockerized: multi-stage `golang:1.24-bookworm` → `distroless/static-debian12:nonroot`, in-Go ticker (no cron daemon), `compose.yml` with `postgres:16-alpine`.
- Docker-only Makefile (`build`, `dev`, `test`, `itest`, `cover`, `clean`); unit (`httptest`) + integration (ephemeral PG) suites with 90% gate on `internal/...`; refreshed `README.md` + new `AGENTS.md`.

## Capabilities

### New Capabilities

- `sync-config`: named connections + sync definitions, JSON file + env override, `flag>env>file>default` precedence.
- `sync-engine`: 1-source→N-targets fan-out, `since` incremental fetch, hash diff, per-target push, ticker scheduling.
- `service-connectors`: Trakt / Simkl / Ryot / Yamtrack clients behind `History(since)` + `Push(items)` with ID mapping.
- `sync-state`: Postgres `sync_state` + `seen_items` cursors and idempotency.
- `app-ops`: CLI surface, distroless Docker image, compose, dockerized Makefile, test suites + coverage gate, README/AGENTS docs.

### Modified Capabilities

- None (greenfield; `openspec/specs/` is empty).

## Impact

- New Go module, two external deps `github.com/jackc/pgx/v5` + `github.com/golang-migrate/migrate/v4` (postgres + iofs/file sources); otherwise stdlib (`net/http`, `encoding/json`, `flag`, `log/slog`, `time`, `testing`/`httptest`, `embed`, `os`).
- New infra: `Dockerfile`, `compose.yml`, `compose.test.yml`, `Makefile`, `config.example.json`, versioned `migrations/000001_*.up|down.sql` (embedded via iofs, file:// override for dev).
- Runtime home for config/state: `--config` flag > `WATCHMESH_CONFIG` env > `$XDG_CONFIG_HOME/watchmesh/config.json` > `~/.config/watchmesh/config.json` > legacy `~/.watchmesh/config.json` fallback. App runs migrations (`Up`) on every boot before opening the store.
- External systems touched via HTTPS only: `api.trakt.tv`, `api.simkl.com`, self-hosted Ryot `/backend/graphql`, self-hosted Yamtrack `/api/v1`. No webhooks or inbound ports in v1.
