# watchmesh

1-source → N-targets watch-history sync in Go. One `source` connection fans out to many `target` connections per sync; cursors + idempotency hashes live in Postgres.

## Quickstart

```sh
cp config.example.json config.json
export DATABASE_URL=postgres://watchmesh:watchmesh@localhost:5432/watchmesh?sslmode=disable
export TRAKT_ID=... TRAKT_SECRET=...
export SIMKL_ID=...
# Ryot/Yamtrack only if used:
export RYOT_URL=... RYOT_TOKEN=...
export YAM_URL=... YAM_TOKEN=...
docker compose up --build   # or: make dev
```

Authenticate once per connection, then run:

```sh
go run ./cmd/watchmesh auth --config config.json --connection trakt_main
go run ./cmd/watchmesh sync --config config.json
go run ./cmd/watchmesh serve --config config.json
```

## Config

JSON map form (canonical). Routing is by connection **name**; duplicate types under distinct names are allowed (e.g. two Trakt accounts). Unknown source/target names fail fast at startup.

Precedence: `flag > env > file > default`. Any string value `"env:VAR"` expands from the environment. Env names: `DATABASE_URL`, `WATCHMESH_INTERVAL`, `WATCHMESH_CONFIG`.

Config file resolution (first hit wins):

1. `--config` flag
2. `WATCHMESH_CONFIG` env
3. `$XDG_CONFIG_HOME/watchmesh/config.json`
4. `~/.config/watchmesh/config.json`
5. legacy `~/.watchmesh/config.json` (read fallback; writes go to XDG path)

Multi-account example (see `config.example.json`):

```json
{
  "interval": "15m",
  "database_url": "env:DATABASE_URL",
  "connections": {
    "trakt_main": {"type": "trakt", "client_id": "env:TRAKT_ID", "client_secret": "env:TRAKT_SECRET"},
    "trakt_alt":  {"type": "trakt", "client_id": "env:TRAKT2_ID", "client_secret": "env:TRAKT2_SECRET"},
    "simkl_main": {"type": "simkl", "client_id": "env:SIMKL_ID"}
  },
  "syncs": [
    {"name": "main-mesh", "source": "trakt_main", "targets": ["simkl_main"]},
    {"name": "alt-mesh", "source": "trakt_alt", "targets": ["simkl_main"]}
  ]
}
```

## CLI

```sh
watchmesh sync [--config path] [--migrations-path dir] [--sync name]  # --sync empty = all
watchmesh serve [--config path] [--migrations-path dir]               # ticker loop on interval
watchmesh auth [--config path] --connection <name>                    # device/PIN flow or token check
```

`auth`: Trakt prints a device URL and polls for a token; Simkl prints a PIN URL and polls; Ryot/Yamtrack validate the configured token with one GET.

## Make targets (docker-only, no host Go/PG needed)

```sh
make build        # docker build -t watchmesh .
make dev          # docker compose up --build
make test         # go test ./... -count=1 (in pinned golang container)
make itest        # compose.test.yml: ephemeral postgres + tester
make cover        # fails below 90% on ./internal/...
make clean        # rm c.out watchmesh; compose down -v
make migrate-new NAME=add_x  # scaffolds migrations/NNNNNN_add_x.{up,down}.sql
```

## Migrations

Embed-first: SQL in `migrations/` ships inside the distroless binary (`source/iofs`); no `COPY migrations/` needed. Boot runs `migrate Up` before opening the pool; `ErrNoChange` continues.

```sh
go run ./cmd/watchmesh serve --migrations-path ./migrations  # dev: file:// override, no rebuild
```

Dirty DB fails fast with `version + dirty` in logs. Recover manually, never auto-force on boot:

```sh
# inside migrate/migrate:v4.20.1 container against your DATABASE_URL:
migrate -path /migrations -database "$DATABASE_URL" force <last_good_version>
```

## Connectors

| Connector | Status |
|---|---|
| Trakt `https://api.trakt.tv` | Confirmed: device auth, `GET`+`POST /sync/history` |
| Simkl `https://api.simkl.com` | Confirmed, with rule: always `GET /sync/activities` dirty-check before `/sync/all-items?date_from=` — never timer-poll `all-items` or `client_id` gets suspended. PIN auth (5y token) |
| Ryot `{base}/backend/graphql` | STUB — candidate `updateSeenHistory` mutation, history best-effort. Pending live verify (task 5.3) |
| Yamtrack `{base}` | STUB — candidate webhook-style single POST per item, no batch assumption; header form (`Bearer` vs `X-API-Key` vs `?token=`) unconfirmed. Pending live verify (task 6.3) |

See `openspec/changes/add-watchmesh-sync/design.md` §7 and `nuances.md` for payload shapes and live-introspection checklists.

## Rate limits

- Trakt: commonly ~1000 GET/5min, 1 POST/s — verify live.
- Simkl: 10 GET/s, 1 POST/s, 20s per-user write lock (`400 rate_limit`).
- All connectors: on HTTP 429 wait `Retry-After` once, then retry once.

## Coverage

90% gate on `./internal/...` (`cmd/` thin excluded):

```sh
make cover
```
