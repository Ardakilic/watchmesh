## ADDED Requirements
### Requirement: CLI surface
The CLI SHALL offer sync with config and sync flags, serve, and auth per connection.

#### Scenario: Commands dispatch
WHEN a user runs sync, serve, or auth
THEN each command MUST execute its defined action.

### Requirement: Container image
The image SHALL build on golang 1.24-bookworm with CGO_ENABLED 0 and ship distroless static-debian12 nonroot with migrations embedded (no external migration mount in prod).

#### Scenario: Minimal runtime
WHEN the image runs
THEN it MUST execute as nonroot from a distroless base and migrate from the embedded source.

### Requirement: Compose topology
Compose SHALL define app plus postgres 16-alpine.

#### Scenario: Stack boots
WHEN compose starts
THEN app MUST reach postgres over the internal network.

### Requirement: Docker-only make and tests
The Makefile SHALL expose docker-only build, dev, test, itest, cover, clean, migrate-new with unit httptest plus ephemeral PG integration and a 90 percent gate on internal packages.

#### Scenario: Gated suite
WHEN cover runs
THEN internal packages MUST meet 90 percent or the target MUST fail.

#### Scenario: Migration scaffold
WHEN `make migrate-new NAME=foo` runs
THEN a seq-numbered up/down SQL pair MUST appear under `migrations/`.

### Requirement: Docs updated
README and AGENTS.md SHALL document setup, commands, and workflows.

#### Scenario: Docs present
WHEN a new operator onboards
THEN README plus AGENTS.md MUST cover config, CLI, and make targets.
