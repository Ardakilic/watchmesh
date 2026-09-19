# Quickstart: Trakt → Simkl + self-hosted Ryot + Yamtrack

Import your Trakt watch history and fan it out to Simkl, your Ryot instance,
and your Yamtrack instance. Ryot/Yamtrack take any `base_url`, so custom
domains work (`https://ryot.example.com` — no path needed, trailing slash OK).

Two paths, same result. Pick one (~20 minutes including auth flows).

## Path A — Docker (no Go toolchain)

1. Copy and edit the config:
```sh
cp config.example.json config.json
```
2. Fill `config.json` — one sync, Trakt source, three targets (literal values
   are simplest here; `"env:VAR"` reads container env):
```json
{
  "interval": "15m",
  "database_url": "env:DATABASE_URL",
  "connections": {
    "trakt_main": { "type": "trakt", "client_id": "YOUR_TRAKT_ID", "client_secret": "YOUR_TRAKT_SECRET", "token": "env:TRAKT_TOKEN" },
    "simkl_main": { "type": "simkl", "client_id": "YOUR_SIMKL_ID", "token": "env:SIMKL_TOKEN" },
    "ryot_self":  { "type": "ryot", "base_url": "https://ryot.example.com", "token": "env:RYOT_TOKEN" },
    "yam_self":   { "type": "yamtrack", "base_url": "https://yamtrack.example.com", "token": "env:YAM_TOKEN" }
  },
  "syncs": [{ "name": "trakt-mesh", "source": "trakt_main",
              "targets": ["simkl_main", "ryot_self", "yam_self"] }]
}
```
3. Authenticate Trakt (device flow — prints a token):
```sh
docker compose run --rm app auth --connection trakt_main
```
4. Authenticate Simkl (PIN flow — prints a token):
```sh
docker compose run --rm app auth --connection simkl_main
```
5. Get Ryot/Yamtrack tokens from each app's settings/token screen, then check them:
```sh
docker compose run --rm -e RYOT_TOKEN=xxx app auth --connection ryot_self
docker compose run --rm -e YAM_TOKEN=xxx app auth --connection yam_self
```
Want `OK` on both before continuing.
6. Export the four tokens and start the sync loop:
```sh
export TRAKT_TOKEN=xxx SIMKL_TOKEN=xxx RYOT_TOKEN=xxx YAM_TOKEN=xxx
docker compose up --build
```
First run imports full Trakt history; later ticks (every `interval`) are incremental.

## Path B — Binary directly (Go 1.27+)

1. Start Postgres and point at it:
```sh
docker compose up -d postgres
export DATABASE_URL=postgres://watchmesh:watchmesh@localhost:5432/watchmesh?sslmode=disable
```
2. Copy and edit the config (same shape as Path A, step 2 — `"env:VAR"`
   reads your shell env here):
```sh
cp config.example.json config.json
```
3. Build once:
```sh
go build -o watchmesh ./cmd/watchmesh
```
4. Authenticate — Trakt device flow, then Simkl PIN flow (each prints a token):
```sh
./watchmesh auth --config config.json --connection trakt_main
./watchmesh auth --config config.json --connection simkl_main
```
5. Export the printed tokens plus your self-hosted tokens:
```sh
export TRAKT_TOKEN=xxx SIMKL_TOKEN=xxx
export RYOT_TOKEN=xxx YAM_TOKEN=xxx
```
6. Validate the self-hosted instances, then run one full sync:
```sh
./watchmesh auth --config config.json --connection ryot_self
./watchmesh auth --config config.json --connection yam_self
./watchmesh sync --config config.json --sync trakt-mesh
```
7. Stay in sync on a loop:
```sh
./watchmesh serve --config config.json
```

## Notes (both paths)

- State is per target: if Yamtrack is down, Simkl/Ryot still advance and
  Yamtrack catches up next tick. Cursor advances only when all targets succeed.
- Matching is TMDB-based; Trakt items without a TMDB id are skipped on push.
- Ryot token screen also confirms the expected auth header/enums if a push
  ever 401s — re-check the token, not the URL.
