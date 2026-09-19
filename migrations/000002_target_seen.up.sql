-- 000002_target_seen.up.sql: per-target delivery tracking.
-- Existing rows are kept untouched: the backfill default assigns them
-- target='', then the default is dropped so all future writes must supply
-- a real connection name. '' rows never match per-target lookups, so the
-- current History window (bounded by last_run_at) re-delivers exactly once
-- and is then marked per target. watched_at column and values are carried
-- over verbatim.
ALTER TABLE seen_items ADD COLUMN target TEXT NOT NULL DEFAULT '';
ALTER TABLE seen_items ALTER COLUMN target DROP DEFAULT;
ALTER TABLE seen_items DROP CONSTRAINT seen_items_pkey;
ALTER TABLE seen_items ADD CONSTRAINT seen_items_pkey PRIMARY KEY (sync_name, target, item_hash);
