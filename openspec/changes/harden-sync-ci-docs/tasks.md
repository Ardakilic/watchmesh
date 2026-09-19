## 1. Connector retry

- [x] 1.1 Add `doWithRetry` (429 retry-once, `Retry-After`, ctx-cancelable) to Ryot push path + `httptest` tests (429→200 delivers, 429→429 errors with nothing marked)
- [x] 1.2 Add `doWithRetry` to Yamtrack push path + same `httptest` tests
- [x] 1.3 Add engine-level test proving rate-limited items stay unseen with held cursor and succeed on re-run

## 2. CI + GHCR

- [x] 2.1 Add `.github/workflows/ci.yml` (vet + gofmt + `go test ./...` + 90% cover gate on PR / main push, golang:1.24 container)
- [x] 2.2 Add `.github/workflows/release.yml` (build + push `ghcr.io/<owner>/watchmesh:sha,latest` on `main` via `GITHUB_TOKEN`)
- [x] 2.3 Validate both workflows with `actionlint` or `yamllint` if available, else careful YAML review

## 3. Repo hygiene + docs

- [x] 3.1 Commit `.env.example` (placeholder `DATABASE_URL`, `TEST_DATABASE_URL`, `WATCHMESH_CONFIG`)
- [x] 3.2 Commit `.opencode/commands/`, `.opencode/skills/`, `.opencode/.gitignore` (intent only)
- [x] 3.3 Rewrite README (ADHD-friendly how-it-works, mermaid poll→diff→push diagram, quickstart, commands)

## 4. Quality gate

- [ ] 4.1 Docblocks on all new/missing funcs in touched packages
- [ ] 4.2 `go vet` + `gofmt` clean, full suite green, coverage ≥90% on `internal/...`
- [ ] 4.3 `openspec validate harden-sync-ci-docs --strict`
