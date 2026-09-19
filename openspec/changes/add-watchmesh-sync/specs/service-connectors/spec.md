## ADDED Requirements
### Requirement: Shared item and contract
Connectors SHALL exchange WatchItem with ids, media_type, title, year, season, episode, watched_at via Source.History and Target.Push.

#### Scenario: Contract mapping
WHEN a service returns native history
THEN it MUST map to WatchItem fields without dropping watched_at.

### Requirement: Trakt connector
The Trakt connector SHALL use device-code auth, GET /sync/history, POST /sync/history with trakt-api-key, Bearer, trakt-api-version 2.

#### Scenario: Trakt round trip
WHEN History and Push run against Trakt
THEN requests MUST carry all three headers and items MUST round-trip.

### Requirement: Simkl connector
The Simkl connector SHALL use PIN auth, GET /sync/activities then /sync/all-items, POST /sync/history at 10 GET/s and 1 POST/s.

#### Scenario: Simkl incremental fetch
WHEN activities report a newer cursor
THEN all-items MUST be fetched and rate limits MUST be respected.

### Requirement: Ryot connector
The Ryot connector SHALL Bearer-auth POST /backend/graphql mutations, target-first with best-effort history.

#### Scenario: Ryot push succeeds
WHEN Push sends GraphQL mutations
THEN the Bearer token MUST authenticate and history gaps MUST NOT fail the sync.

### Requirement: Yamtrack connector
The Yamtrack connector SHALL Bearer/X-API-Key GET /api/v1/media/{type}/ with limit/offset up to 200 and POST/PATCH single items in a loop.

#### Scenario: Yamtrack paged sync
WHEN history exceeds one page
THEN pagination MUST continue and each item MUST be pushed singly with no batch call.
