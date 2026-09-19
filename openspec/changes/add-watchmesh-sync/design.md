# Design: add-watchmesh-sync

## 1. Layout

`cmd/watchmesh/main.go` is thin: parse flags, load config, open store, wire
connectors, dispatch `sync` / `serve` / `auth`. All logic in `internal/`:
`config` (load + precedence), `model` (types + hash), `trakt|simkl|ryot|yamtrack`
(one client each), `engine` (fan-out), `store` (Postgres cursors).

## 2. Dependencies (Context7-verified)

Go 1.27, stdlib: `net/http`, `encoding/json`, `flag`, `log/slog`,
`time`, `testing` + `net/http/httptest`, `embed`, `os`. Two external deps:
`github.com/jackc/pgx/v5 v5.11.0` (`pgxpool`, queries) + `github.com/golang-migrate/migrate/v4 v4.20.1`
(`database/postgres` + `source/iofs` embed-first + `source/file` dev override).
Context7 IDs used: `/golang/go`, `/docker/docs`, `/jackc/pgx`,
`/golang-migrate/migrate`, plus Trakt/Simkl/Ryot/Yamtrack lookups (see nuances.md).

## 3. Core types

```go
type IDs struct { Trakt int; Simkl int; IMDB string; TMDB int; TVDB int }
type WatchItem struct {
  IDs IDs; MediaType string // movie|show|episode
  Title string; Year int; Season, Episode int
  WatchedAt time.Time
}
func (w WatchItem) Hash() string // sha1(mediatype|trakt|simkl|imdb|tmdb|tvdb|season|episode|watchedAt.UTC)
type Source interface { Name() string; History(ctx context.Context, since time.Time) ([]WatchItem, error) }
type Target interface { Name() string; Push(ctx context.Context, items []WatchItem) error }
```

`engine` depends only on these interfaces; `Hash()` is the idempotency key in `seen_items`.

## 4. Config (map form — canonical)

```json
{
  "interval": "15m",
  "database_url": "env:DATABASE_URL",
  "connections": {
    "trakt_main": {"type": "trakt", "client_id": "env:TRAKT_ID", "client_secret": "env:TRAKT_SECRET"},
    "trakt_alt":  {"type": "trakt", "client_id": "env:TRAKT2_ID", "client_secret": "env:TRAKT2_SECRET"},
    "simkl_main": {"type": "simkl", "client_id": "env:SIMKL_ID"},
    "ryot_self":  {"type": "ryot", "base_url": "env:RYOT_URL", "token": "env:RYOT_TOKEN"},
    "yam_self":   {"type": "yamtrack", "base_url": "env:YAM_URL", "token": "env:YAM_TOKEN"}
  },
  "syncs": [
    {"name": "main-mesh", "source": "trakt_main", "targets": ["simkl_main", "ryot_self", "yam_self"]},
    {"name": "alt-mesh", "source": "trakt_alt", "targets": ["simkl_main"]}
  ]
}
```

Precedence: `flag > env > file > default`. `env:VAR` expands in strings.
Duplicate types allowed; routing is by connection `name`. Unknown
source/target name fails fast at startup before any sync runs.

Config file resolution (first hit wins): `--config` flag >
`WATCHMESH_CONFIG` env > `$XDG_CONFIG_HOME/watchmesh/config.json` >
`~/.config/watchmesh/config.json` > legacy `~/.watchmesh/config.json`
(read-only fallback; new writes go to the XDG path). `--migrations-path`
flag optionally points at an external migrations dir (file://) for dev;
unset means embedded iofs.

## 5. Migrations + state (migrate on boot)

Versioned SQL in repo-root `migrations/`, seq numbering, one pair per change:

```
migrations/000001_init.up.sql
migrations/000001_init.down.sql
migrations/000002_....up.sql
```

`000001_init.up.sql` creates both tables:

```sql
CREATE TABLE sync_state (
  sync_name TEXT PRIMARY KEY,
  last_run_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_success_at TIMESTAMPTZ
);
CREATE TABLE seen_items (
  sync_name TEXT NOT NULL,
  item_hash TEXT NOT NULL,
  watched_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (sync_name, item_hash)
);
```

Down drops in reverse (`seen_items`, then `sync_state`).
Startup order: `migrate Up` → open `pgxpool` → serve/sync. Boot code:

```go
//go:embed migrations/*.sql
var migFS embed.FS
src, _ := iofs.New(migFS, "migrations") // dev override: "file://<--migrations-path>" when flag set
m, _ := migrate.NewWithSourceInstance("iofs", src, databaseURL)
if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
  log.Fatal(...) // dirty version also fatal: manual `force` recovery, never auto-Force on boot
}
pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
defer pool.Close()
```

Embed-first (release: single distroless binary, no extra COPY, no path drift);
`--migrations-path` file:// override for dev SQL iteration without rebuild.
Postgres driver uses an advisory lock so N instances booting together are safe.
Dirty DB fails fast with version in logs; recovery is manual
(`migrate force <last_good>` then restart), documented in README/AGENTS.

## 6. Engine Sync (5 steps)

1. `since := store.LastRun(sync)`; `items := source.History(ctx, since)`.
2. Hash each item; `fresh := filter(!store.Seen(sync, hash))`.
3. Fan out: for each target, `Push(ctx, fresh)`; collect per-target errors.
4. Mark hashes seen only for successful targets (per-target retry next run).
5. `store.SetLastRun(sync, now)` iff at least one target succeeded.

Ticker loop (serve): `time.NewTicker(interval)` + `signal.NotifyContext`
for graceful stop; Sync runs per tick, no partial state write on cancel.

## 7. Connectors: auth + endpoints + payloads

| Conn | Auth (CLI `auth`) | History | Push |
|---|---|---|---|
| Trakt `https://api.trakt.tv` | Device: `POST /oauth/device/code {client_id}` → poll `POST /oauth/device/token {code,client_id,client_secret}` | `GET /sync/history/{movies\|episodes}?start_at=&page=&limit=` headers `trakt-api-key`+`Bearer`+`trakt-api-version: 2` | `POST /sync/history` |
| Simkl `https://api.simkl.com` | PIN: `GET /oauth/pin?client_id` → poll `GET /oauth/pin/{user_code}` every `interval` (5y token) | `GET /sync/activities` dirty-check, then `GET /sync/all-items/{movies\|shows\|anime}/…?date_from=` — never timer-poll all-items or `client_id` is suspended | `POST /sync/history` (10 GET/s, 1 POST/s) |
| Ryot `{base}/backend/graphql` | `Authorization: Bearer <token>` — VERIFY live: token screen / `docs.ryot.io/guides/authentication.html` | Best-effort GraphQL query (introspect live: collection/user-media list, `page/limit`) — gaps never fail sync | GraphQL mutation (introspect live; candidate `updateSeenHistory`) |
| Yamtrack `{base}` | Token from web UI (Integrations); header form VERIFY live (`Bearer` vs `X-API-Key` vs `?token=`) | VERIFY live: paginated media/history GET if present, else source-only mode | Webhook-style POST per item (inspect `views.py`/webhook live); loop single items, no batch assumption |

Trakt push (confirmed shape):

```json
{"movies":[{"watched_at":"2026-05-10T20:00:00Z","ids":{"trakt":1,"imdb":"tt1201607","tmdb":603}}],
 "episodes":[{"watched_at":"2026-05-13T19:00:00Z","ids":{"trakt":16,"tvdb":269953,"tmdb":349232}}]}
```

Simkl push (confirmed shape):

```json
{"movies":[{"watched_at":"2026-05-15T22:30:00Z","ids":{"simkl":1015859}}],
 "shows":[{"ids":{"simkl":1411674},"seasons":[{"number":1,"episodes":[{"number":1,"watched_at":"2026-05-13T19:00:00Z"}]}]}]}
```

Ryot/Yamtrack payloads are STUBS until live introspection (see nuances.md):
Ryot candidate `mutation($i:UpdateSeenInput!){updateSeenHistory(i:$i)}`
with `{metadataId:"tmdb://movie/27205", state:"Completed", finishedOn}`.
Yamtrack candidate `{"source":"tmdb","media_type":"movie","media_id":27205,
"status":"Completed","end_date":"2026-09-19"}`. Confirm enum/field names
against the running instance during apply; keep mapping isolated per
connector so corrections stay one-file diffs.

`auth` prints URL + polls for Trakt/Simkl; Ryot/Yamtrack validate token via
one GET. No inbound ports in v1. HTTP client: stdlib `net/http` with
`Authorization: Bearer` header + `encoding/json` bodies; backoff on 429.

## 8. Ops: image, compose, scheduling

```dockerfile
FROM golang:1.27.1-bookworm@sha256:6ed48491acfb40533f6970d9d8ce3cbfc6f2cc7d81413c2f01c871c927d98634 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/watchmesh ./cmd/watchmesh
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/watchmesh /watchmesh
USER nonroot:nonroot
ENTRYPOINT ["/watchmesh"]
```

No `COPY migrations/` needed: SQL is embedded via `iofs` (design §5).
`compose.yml`: `app` (build ., `command: serve`, env `DATABASE_URL`,
`CONFIG_PATH`) + `postgres:16-alpine` with named volume; app reaches PG over
internal network. `compose.test.yml`: ephemeral `postgres:16-alpine` + tester
on shared network for `itest`. Scheduling is in-Go (see §6). Rejected:
supercronic/cron daemon — extra process, harder log/shutdown story.

## 9. Makefile (docker-only)

```make
GO := golang:1.27.1-bookworm
RUN := docker run --rm -v $(PWD):/src -w /src $(GO)
build: ; docker build -t watchmesh .
dev: ; docker compose up --build
test: ; $(RUN) go test ./... -count=1
itest: ; docker compose -f compose.test.yml up --abort-on-container-exit
cover: ; $(RUN) go test -coverprofile=c.out ./internal/... -count=1 && $(RUN) go tool cover -func=c.out | awk '/^total:/ {print; if ($$3+0 < 90) {print "coverage below 90%" > "/dev/stderr"; exit 1}}'
clean: ; rm -f c.out watchmesh; docker compose down -v
migrate-new: ; docker run --rm -v $(PWD)/migrations:/migrations migrate/migrate:v4 create -ext sql -dir /migrations -seq $(NAME)
```

Host needs only Docker. `cover` fails below 90% on `internal/...`
(`cmd/` thin excluded).

## 10. Test plan (+httptest sketch)

Unit: `httptest` table tests per connector (200/401/500, malformed JSON,
pagination); engine fakes (diff, partial failure, empty). Integration:
`TestMain` spins ephemeral PG, applies DDL, asserts cursor + dedupe.
Gate: 90% on `internal/...` (`cover` target fails below).

```go
srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
  w.Header().Set("Content-Type", "application/json")
  fmt.Fprint(w, `{"movies":[]}`)
}))
defer srv.Close()
```

## 11. Fresh-apply checklist

1. Re-run Context7 `resolve`+`query` for Go/Docker/pgx/migrate + each watch API.
2. Pin `golang:1.27.1-bookworm` + distroless digest at apply time.
3. Introspect live Ryot GraphQL schema + Yamtrack webhook/views before
   finalizing those two connectors; keep them target-first.
4. Verify Trakt rate limits live; keep 429 backoff everywhere.
