-- 000002_target_seen.down.sql
ALTER TABLE seen_items DROP CONSTRAINT seen_items_pkey;
ALTER TABLE seen_items DROP COLUMN target;
ALTER TABLE seen_items ADD CONSTRAINT seen_items_pkey PRIMARY KEY (sync_name, item_hash);
