## ADDED Requirements

### Requirement: Uniform 429 retry
Every connector SHALL retry a request once when the server responds with HTTP 429, waiting for the `Retry-After` duration (default 1 second when absent or unparsable) before retrying. The wait MUST be context-cancelable.

#### Scenario: Retry-After honored then success
- **WHEN** a push gets 429 with `Retry-After: 2` followed by 200
- **THEN** the connector waits ~2s, retries once, and reports the delivery as successful.

#### Scenario: Persistent 429 surfaces as error
- **WHEN** the retry also gets 429
- **THEN** the connector returns an error and marks nothing as seen.

### Requirement: No watch lost on rate limit
A rate-limited item SHALL remain unseen with the sync cursor held, so the next tick retries it.

#### Scenario: 429 storm delays but never drops
- **WHEN** all retries are exhausted with 429s
- **THEN** the sync errors, `seen_items` is unchanged for undelivered items, `last_run_at` is not advanced, and the next tick re-attempts them.
