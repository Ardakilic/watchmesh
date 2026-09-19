# watchmesh

1 source → N targets watch-history sync. Trakt/Simkl/Ryot/Yamtrack fan out per sync; Postgres holds cursors + per-target seen hashes so reruns only push what's missing.

New here? Start with [QUICKSTART.md](QUICKSTART.md) — Trakt → Simkl + self-hosted Ryot/Yamtrack, Docker or binary.

## How it works

```mermaid
flowchart LR
    A[Poll source: History since cursor] --> B[Diff per target: skip seen hashes]
    B --> C[Push fresh items per target]
    C -- 429 --> R[Wait Retry-After, retry once]
    R --> C
    C -- delivered --> M[MarkSeen per target]
    C -- failed/skipped --> H[Hold cursor, retry next tick]
    M --> D[All targets OK? advance cursor]
```

## Quickstart

```sh
cp config.example.json config.json        # edit connections + syncs
cp .env.example .env                      # fill dummy values with real ones
set -a; . ./.env; set +a                  # export .env for the go run commands below
export DATABASE_URL=postgres://watchmesh:watchmesh@localhost:5432/watchmesh?sslmode=disable
make dev                                  # compose: postgres + serve
go run ./cmd/watchmesh auth --config config.json --connection trakt_main
go run ./cmd/watchmesh sync --config config.json
```

Config resolution: `--config` > `WATCHMESH_CONFIG` > `$XDG_CONFIG_HOME/watchmesh/config.json` > `~/.config/watchmesh/config.json` > legacy `~/.watchmesh/config.json`. Any `"env:VAR"` value expands from the environment. Env: `DATABASE_URL`, `WATCHMESH_INTERVAL`, `WATCHMESH_CONFIG` (+ `TEST_DATABASE_URL` for itest).

## Commands

| Command | What it does |
|---|---|
| `watchmesh sync [--config p] [--migrations-path d] [--sync name]` | Run one (`--sync`) or all syncs once |
| `watchmesh serve [--config p] [--migrations-path d]` | Ticker loop on `interval` until SIGINT/SIGTERM |
| `watchmesh auth [--config p] --connection <name>` | Trakt device / Simkl PIN flow, or Ryot/Yamtrack token check |
| `make build` | `docker build -t watchmesh .` |
| `make dev` | `docker compose up --build` |
| `make test` | `go test ./...` in pinned golang container |
| `make itest` | Ephemeral PG + tester via `compose.test.yml` |
| `make cover` | Fails below 90% on `./internal/...` |
| `make clean` | `rm c.out watchmesh`, `compose down -v` |
| `make migrate-new NAME=add_x` | Scaffold `migrations/NNNNNN_add_x.{up,down}.sql` |

## Retries + state (5 lines)

1. Every connector retries once on HTTP 429, waiting `Retry-After` (default 1s).
2. Only delivered items are marked seen — per target, keyed by connection name.
3. Any failure/skip holds the cursor; the next tick covers exactly the remainder.
4. Cursor advances to the window-end only when every target fully succeeds.
5. History read gaps return empty (non-fatal); empty windows touch no state.

## Connectors

| Connector | Status |
|---|---|
| Trakt `https://api.trakt.tv` | Confirmed: device auth, `GET`+`POST /sync/history` |
| Simkl `https://api.simkl.com` | Confirmed — always dirty-check `GET /sync/activities` before `/sync/all-items`, never timer-poll it |
| Ryot `{base}/backend/graphql` | STUB — candidate `updateSeenHistory`, history best-effort (task 5.3) |
| Yamtrack `{base}` | STUB — single POST per item, header form unconfirmed (task 6.3) |

Limits: Trakt ~1000 GET/5min + 1 POST/s; Simkl 10 GET/s, 1 POST/s. Migrations embed in the binary and run on boot; dirty DB fails fast — `migrate force <last_good>` manually, never auto. Details: `AGENTS.md`, `openspec/changes/add-watchmesh-sync/`.
