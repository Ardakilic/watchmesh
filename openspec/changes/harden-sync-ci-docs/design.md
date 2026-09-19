## Context

Trakt (`internal/trakt/client.go:56-88`) and Simkl (`internal/simkl/client.go:59-91`) share the same retry shape: `doWithRetry` retries once on 429, honors `Retry-After` (default 1s), waits ctx-cancelably. Ryot and Yamtrack have no retry; any 429 fails the push outright. No-loss already holds at the engine layer (delivered-only `MarkSeen`, held cursor), so retry only needs to mirror the proven helper per connector — no shared abstraction (two call sites, keep the diff local).

Repo has no `.github/`, no `.env.example`, untracked `.opencode/` (commands + skills only), and a 4.5K README without a how-it-works section.

## Goals / Non-Goals

**Goals:**
- 429 transparency on all four connectors; watches never lost (retry now, persist + retry-next-tick otherwise).
- PR checks + GHCR images on `main` with zero new credentials (`GITHUB_TOKEN`).
- Committed `.env.example`, `.opencode` intent, ADHD-friendly README with mermaid diagram.

**Non-Goals:**
- Anime/MAL ID mapping (explicitly deferred).
- Simkl-style proactive throttling for Ryot/Yamtrack (no documented limits; retry-once suffices until a live 429 says otherwise).
- Compose file merging (two files are the recommended pattern; works as-is).

## Decisions

- **Copy the `doWithRetry` shape into Ryot/Yamtrack** rather than extracting a shared package. Rationale: 3-line divergence risk isn't worth a new package + import graph for two STUB connectors; revisit if a third behavior (e.g. throttling) lands. Alternative (shared `internal/http/retry`) rejected per YAGNI.
- **CI runs `make`-equivalent steps directly** (`go vet`, `gofmt -l`, `go test`, coverage awk gate) on `golang:1.27.1-bookworm` container to match the pinned builder. Alternative (calling `make test` inside docker) rejected: doubles container-in-container complexity; unit tests need no PG (store tests skip without `TEST_DATABASE_URL`).
- **Separate `release.yml` workflow** for GHCR (build + push on `main` only, `ghcr.io/<owner>/watchmesh`, `GITHUB_TOKEN` with `packages: write`). Alternative (one workflow with `if:`) rejected: noisier; two files read cleaner.
- **README structure**: 30-second what-it-does → mermaid poll→diff→push diagram → quickstart → commands table → how retries/state work (5 lines). Details stay in `AGENTS.md`/specs.

## Risks / Trade-offs

- [Risk] GHCR push needs repo Settings → Actions → "Read and write permissions" or explicit `permissions: packages: write` → Mitigation: set `permissions:` in workflow; document one-time toggle.
- [Risk] Retry-once still fails under sustained 429 storms → Mitigation: engine holds cursor, next tick retries; no loss, only delay.
- [Risk] Ryot/Yamtrack are STUBs awaiting live verification → Mitigation: retry paths tested with `httptest` 429→200 and 429→429 sequences; live-verify later per tasks 5.3/6.3.

## Open Questions

- None blocking. MAL-mapper choice and compose override slimming are parked follow-ups.
