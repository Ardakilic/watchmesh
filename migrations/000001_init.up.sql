CREATE TABLE sync_state (
  sync_name TEXT PRIMARY KEY,
  last_run_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_success_at TIMESTAMPTZ
);
CREATE TABLE seen_items (
  sync_name TEXT NOT NULL,
  item_hash TEXT NOT NULL,
  watched_at TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (sync_name, item_hash)
);
