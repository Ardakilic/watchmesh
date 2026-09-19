## ADDED Requirements

### Requirement: PR checks
Every pull request (and every push to `main`) SHALL run `go vet`, `gofmt -l` (fail on output), the full `go test ./...` suite, and the 90% coverage gate on `internal/...`.

#### Scenario: Failing check blocks merge
- **WHEN** vet fails, any file is unformatted, any test fails, or coverage drops below 90%
- **THEN** the workflow fails and the PR cannot merge (pending branch protection).

### Requirement: GHCR image on main
Every commit to `main` SHALL build the Docker image and push it to `ghcr.io/<owner>/watchmesh` tagged with the commit SHA and `latest`, using only `GITHUB_TOKEN` for auth.

#### Scenario: Main commit publishes image
- **WHEN** a commit lands on `main`
- **THEN** the release workflow builds via the repo `Dockerfile` and pushes both tags to GHCR.
