# nuances — add-watchmesh-sync

Tech decisions + uncertainties for fresh-context apply. Read with `design.md`.

## Decisions (ponytail: lazy, stdlib-first)

- Go 1.27 (builder `golang:1.27.1-bookworm` pinned by digest `sha256:6ed48491acfb40533f6970d9d8ce3cbfc6f2cc7d81413c2f01c871c927d98634). `CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w"`.
- Runtime `gcr.io/distroless/static-debian12:nonroot` (has ca-certs/tzdata, ~2MB). `base-debian12` only if cgo ever returns — it won't (pgx is pure Go).
- Two external deps: `github.com/jackc/pgx/v5 v5.11.0` (`pgxpool.New(ctx, DATABASE_URL)`) + `github.com/golang-migrate/migrate/v4 v4.20.1` (postgres + iofs/file). Everything else stdlib: `net/http`, `encoding/json`, `flag.NewFlagSet`, `log/slog`, `time.NewTicker`, `testing`/`httptest`, `embed`, `os`.
- Config JSON + env (`env:VAR` values), never YAML (would add `yaml.v3`). Precedence `flag>env>file>default`. Map form `connections: {name: {...}}` is canonical (design §4); array form rejected.
- Scheduler = in-Go ticker + `signal.NotifyContext`, single PID 1. Rejected: supercronic sidecar (extra binary), host cron (fragile), K8s CronJob (overkill v1). Add supercronic only when exact cron expressions required.
- DB 2 tables only: `sync_state`, `seen_items`. Hash = stable IDs + `watched_at`. No per-service tables.
- Engine generic over `Source`/`Target`; connectors share `WatchItem`. Trakt canonical IDs (`trakt/imdb/tmdb/tvdb`); Simkl accepts all + title/year fallback; Ryot needs TMDB-first lookup; Yamtrack single-POST loop (no batch assumption).
- Makefile 100% dockerized (`docker run --rm -v $(PWD):/src -w /src golang:… go …`); no local Go/PG required. `itest` via `compose.test.yml` (`postgres:16-alpine` + tester on same network).

## Context7 evidence (queried during spec)

- `/golang/go` — ticker/shutdown/transport internals; stdlib `net/http` + `encoding/json` + `httptest` confirmed sufficient, no framework needed.
- `/docker/docs` — multistage `golang:* AS build` → `gcr.io/distroless/*` with `COPY --from=build`, `CGO_ENABLED=0 GOOS=linux go build`, `USER nonroot:nonroot`, cache mounts for `/go/pkg/mod`. Design §8 follows this pattern.
- `/jackc/pgx` — `pgxpool.New(ctx, os.Getenv("DATABASE_URL"))` + `defer pool.Close()` + `pool.Exec/Query/QueryRow` confirmed; design §5 pool snippet is verbatim pattern.
- Trakt + Simkl via Context7 — CONFIRMED: device/PIN flows, `/sync/history`, `/sync/activities` → `/sync/all-items?date_from=`, push payload shapes in design §7. Simkl rule: never timer-poll `all-items` (suspension risk); respect PIN `interval`.
- Ryot (`/ignisda/ryot`) + Yamtrack (`/fuzzygrim/yamtrack`) via Context7 — SPARSE at spec time: base paths confirmed (`/backend/graphql`, self-hosted) but payloads stayed UNCERTAIN; since resolved by reading pinned upstream sources (see API notes, Oct 2026).

## API notes (Oct 2026 — source-introspected in pinned upstream clones)

- Trakt: device flow `POST /oauth/device/code` → poll `/oauth/device/token`; headers `trakt-api-key` + `Bearer` + `trakt-api-version: 2`; `GET /sync/history` + `POST /sync/history`. Limits confirmed: 500 GET/5min + 1 POST/s. Device-poll statuses: 400 pending; 429 slow_down (+5s interval bump, numeric `Retry-After` honored, wait capped 60s); 404 invalid/expired, 409 already-issued, 410 expired, 418 denied, 401/other terminal (no retry). Device/PIN polling bounded by `expires_in`.
- Simkl: PIN `GET /oauth/pin?client_id` → poll `GET /oauth/pin/{code}` (5y token, no refresh); MUST call `GET /sync/activities` before `GET /sync/all-items`; `date_from` is trusted (server filters by watchlist-entry change; seen-hash dedupe absorbs over-delivery); movies fetch only the `completed` bucket (`movies/watching` is not a valid bucket per the OpenAPI). 429 body `error`: `rate_limit`/unknown/empty → retry once after fixed 1s; `user_limit_exceeded`/`app_limit_exceeded` → fatal with reset seconds (Retry-After here is the daily reset). Simkl ids in responses may be quoted strings — decoded via a tolerant int type, requests still send numbers.
- Ryot ✅ verified in v10.5.3 sources (not a live instance): Bearer `POST {base}/backend/graphql`; enums SCREAMING_SNAKE_CASE (`MOVIE`, `SHOW`, `TMDB`; SeenState `COMPLETED`); list responses wrap `{cacheId, response{items, details{nextPage, totalItems}}}`; `metadataSearch` COMMITS TMDB candidates server-side, so strict match on `metadataDetails` `identifier` (plain numeric TMDB string) + `source == "TMDB"` works even on a cold instance; `deployBulkMetadataProgressUpdate(input:[MetadataProgressUpdateInput!]!)` → bare bool; `userDetails` union members `UserDetails`/`UserDetailsError`; history via paged `userMetadataList` + chunked aliased `metadataDetails`/`userMetadataDetails` reads (per-alias errors tolerated).
- Yamtrack ✅ verified in dev-branch sources (not a live instance; `src/integrations/webhooks/`): intake is `POST {base}/webhook/jellyfin/{token}` (token in path, no auth header); unknown token → 401, empty body → 400 "Missing payload", interesting event → 200. `Event`/`Item`/`Series`/`ProviderIds` casing is upstream contract; completion gated on `UserData.Played=true`; movie path reads `Item.ProviderIds["Tmdb"]` (then `Imdb`); TV chain: `Series.ProviderIds["Tmdb"]` + `ParentIndexNumber`+`IndexNumber` (+ optional Item `Imdb`); Item-level `Tvdb` is a TVDB EPISODE id — never send it; episode dupes guarded 5s upstream; endpoint stamps `now` for watched dates. No REST history endpoint: webhook entries are not listable, so History is a no-op.

## Migrations (golang-migrate/migrate v4 — Context7 `/golang-migrate/migrate`)

- Lib: `github.com/golang-migrate/migrate/v4` + `database/postgres` + `source/iofs` (embed-first) + `source/file` (dev override). Two external Go deps total (pgx + migrate).
- Boot: `migrate.NewWithSourceInstance("iofs", src, databaseURL)` then `Up()`; `ErrNoChange` continues; any other error (incl. dirty) is fatal with version in logs. Never auto-`Force` — manual `migrate force <last_good>` + restart, per upstream FAQ/`Force()` docs.
- Postgres driver holds an advisory lock, so concurrent boots are safe.
- Files: repo-root `migrations/NNNNNN_snake.up.sql` + `.down.sql`, `-seq` numbering (`migrate create -ext sql -dir migrations -seq <name>`); `000001_init` creates `sync_state` + `seen_items`.
- Embed-vs-external: embedded iofs = single distroless binary, no COPY/mount drift (release default). External `file://--migrations-path` = edit SQL without rebuild (dev only). Distroless has no shell to run a separate migrate CLI, so in-binary `Up` on boot is the pattern — not a sidecar.
- Home dir: XDG-first (`$XDG_CONFIG_HOME/watchmesh/` else `~/.config/watchmesh/`) with legacy `~/.watchmesh/` read fallback; `WATCHMESH_CONFIG` env / `--config` flag override. Rationale: XDG is the current convention; the dotfolder still works so existing checkouts don't break. Writes always go to XDG.

## Coverage plan

- `go test -coverprofile=c.out ./...; go tool cover -func=c.out`; gate 90% on `internal/...`, `cmd/...` thin excluded.
- Unit: `httptest.NewServer` per connector + engine table tests (70%). Integration: one `TestMain` + ephemeral PG for `store` (20%).

## Fresh-apply must-do

1. Re-run Context7 `resolve-library-id` + `query-docs` for `golang/go`, `docker/docs`, `jackc/pgx`, `golang-migrate/migrate`, Trakt/Simkl/Ryot/Yamtrack before coding connectors.
2. Pin Go + distroless digests at apply time; Ryot/Yamtrack field maps are source-introspected (tasks 5.3/6.3 done) — re-verify end-to-end on a live instance when first deployed.
3. Verify Trakt rate limits live; keep 429 backoff in every connector.
