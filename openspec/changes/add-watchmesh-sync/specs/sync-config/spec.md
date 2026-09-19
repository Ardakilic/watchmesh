## ADDED Requirements

### Requirement: JSON config schema
The system SHALL load JSON config with `interval`, `database_url`, `connections`, and `syncs`.

#### Scenario: Valid config loads
WHEN a config defines interval, database_url, named connections with type and env-referenced creds, and syncs with name, source, and targets
THEN the system MUST resolve all named references successfully.

### Requirement: Precedence order
The system SHALL apply flag > env > file > default precedence.

#### Scenario: Flag overrides env and file
WHEN flag, env, and file all set interval
THEN the flag value MUST win.

### Requirement: Named reference validation
The system SHALL fail fast when a sync references an unknown source or target connection name.

#### Scenario: Missing connection fails fast
WHEN a sync names an undefined source or target
THEN startup MUST abort with an error before any sync runs.

### Requirement: Same-type connections
The system SHALL allow multiple connections of the same type under distinct names.

#### Scenario: Two trakt accounts coexist
WHEN connections define `trakt_main` and `trakt_alt` both with type trakt
THEN both MUST be independently selectable as source or target.

### Requirement: Config home resolution
The system SHALL resolve the config file as `--config` flag > `WATCHMESH_CONFIG` env > `$XDG_CONFIG_HOME/watchmesh/config.json` > `~/.config/watchmesh/config.json` > legacy `~/.watchmesh/config.json` fallback.

#### Scenario: Default home lookup
WHEN no flag or env points at a config
THEN the system MUST load the first existing file in the XDG-then-legacy order and write new state only to the XDG path.
