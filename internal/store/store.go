// Package store persists sync cursors in Postgres.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store persists sync cursors and seen hashes via pgxpool on DATABASE_URL.
// Writes (SetLastRun/MarkSeen) happen only after a sync succeeds; that
// success-only rule is enforced by the engine, which calls them iff at
// least one target Push succeeded.
type Store struct {
	pool *pgxpool.Pool
}

// New opens a pool on databaseURL. The caller owns it: defer Close.
func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() {
	s.pool.Close()
}

// LastRun returns the cursor for sync, or the zero time if never run.
func (s *Store) LastRun(ctx context.Context, syncName string) (time.Time, error) {
	var t time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT last_run_at FROM sync_state WHERE sync_name=$1`, syncName).Scan(&t)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

// SetLastRun upserts the cursor (and last_success_at: the engine calls this
// only after at least one target succeeded).
func (s *Store) SetLastRun(ctx context.Context, syncName string, t time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO sync_state(sync_name,last_run_at,last_success_at) VALUES($1,$2,$2)
		 ON CONFLICT(sync_name) DO UPDATE SET last_run_at=EXCLUDED.last_run_at, last_success_at=EXCLUDED.last_success_at`,
		syncName, t)
	return err
}

// Seen reports whether hash was already delivered to target for sync.
// target is the connection name (e.g. "simkl_main"), never the connector
// type; backfilled 000002 rows carry an empty target and never match.
func (s *Store) Seen(ctx context.Context, syncName, target, hash string) (bool, error) {
	var one int
	err := s.pool.QueryRow(ctx,
		`SELECT 1 FROM seen_items WHERE sync_name=$1 AND target=$2 AND item_hash=$3`, syncName, target, hash).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// MarkSeen records hash as delivered to target (idempotent upsert).
// target is the connection name (e.g. "simkl_main"), never the connector type.
func (s *Store) MarkSeen(ctx context.Context, syncName, target, hash string, watchedAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO seen_items(sync_name,target,item_hash,watched_at) VALUES($1,$2,$3,$4)
		 ON CONFLICT(sync_name,target,item_hash) DO NOTHING`,
		syncName, target, hash, watchedAt)
	return err
}
