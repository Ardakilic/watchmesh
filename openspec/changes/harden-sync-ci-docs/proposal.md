## Why

Two connectors (Ryot, Yamtrack) have no 429 retry, so transient rate limits surface as sync errors instead of transparent retries. The repo also has no CI, no published image, no `.env.example`, uncommitted `.opencode/` skills, and a README that doesn't explain how the tool works. Anime matching is explicitly out of scope for this change.

## What Changes

- Uniform 429 retry-once (`Retry-After`, ctx-cancelable) in Ryot and Yamtrack pushes, mirroring Trakt/Simkl; undelivered watches stay unseen with held cursor and retry next tick (already engine behavior — covered by test).
- `.github/workflows/ci.yml`: `vet` + `gofmt` + `go test ./...` + 90% coverage gate on PRs and pushes to `main`.
- `.github/workflows/release.yml`: build Docker image and push to GHCR on every `main` commit (pinned base digests, `GITHUB_TOKEN`).
- Commit `.env.example` (placeholder `DATABASE_URL`, `TEST_DATABASE_URL`, `WATCHMESH_CONFIG`).
- Commit `.opencode/commands/`, `.opencode/skills/`, `.opencode/.gitignore` (intent only; no generated/secret state).
- Rewrite README: ADHD-friendly how-it-works (poll upstream → push downstream), mermaid flow diagram, setup/commands reference.
- Tests for all new retry paths; coverage stays ≥90% on `internal/...`; docblocks on new/missing funcs.

## Capabilities

### New Capabilities
- `connector-retry`: every connector retries once on HTTP 429 honoring `Retry-After`; no watch is ever marked seen unless delivered.
- `ci-cd`: PRs and `main` pushes run checks; `main` commits publish a GHCR image.

### Modified Capabilities
- (none — engine no-loss semantics already specified; retry only changes connector internals)

## Impact

- `internal/ryot/ryot.go`, `internal/yamtrack/yamtrack.go` (+ tests); no API or schema changes.
- New `.github/workflows/`; `Dockerfile` reused as-is for GHCR builds.
- Docs-only: `README.md`, `.env.example`, `.opencode/` tracked files.
