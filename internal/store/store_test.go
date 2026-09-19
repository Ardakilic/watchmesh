package store

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	migrations "github.com/watchmesh/watchmesh/migrations"
)

// pgURL returns the test database URL, skipping when PG is unavailable.
func pgURL(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("short: needs real PG")
	}
	// Isolated test database first; generic DATABASE_URL only as fallback so
	// migrations and roundtrips never touch shared application data.
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		url = os.Getenv("DATABASE_URL")
	}
	if url == "" {
		t.Skip("TEST_DATABASE_URL/DATABASE_URL unset")
	}
	return url
}

// TestMigrate verifies embedded migrations create both tables and rerun cleanly.
func TestMigrate(t *testing.T) {
	url := pgURL(t)
	ctx := context.Background()
	if err := MigrateUp(url, ""); err != nil {
		t.Fatalf("MigrateUp embed: %v", err)
	}
	// Second run exercises the ErrNoChange path.
	if err := MigrateUp(url, ""); err != nil {
		t.Fatalf("MigrateUp rerun: %v", err)
	}
	st, err := New(ctx, url)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer st.Close()
	for _, tbl := range []string{"sync_state", "seen_items"} {
		var one int
		if err := st.pool.QueryRow(ctx,
			`SELECT 1 FROM information_schema.tables WHERE table_name=$1`, tbl).Scan(&one); err != nil {
			t.Fatalf("table %s missing: %v", tbl, err)
		}
	}
	// seen_items PK must be (sync_name, target, item_hash) in key order.
	rows, err := st.pool.Query(ctx,
		`SELECT a.attname FROM pg_constraint c
		 JOIN pg_class t ON t.oid=c.conrelid
		 JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=ANY(c.conkey)
		 WHERE t.relname='seen_items' AND c.contype='p'
		 ORDER BY array_position(c.conkey, a.attnum)`)
	if err != nil {
		t.Fatalf("PK query: %v", err)
	}
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			t.Fatalf("PK scan: %v", err)
		}
		cols = append(cols, c)
	}
	rows.Close()
	if got, want := strings.Join(cols, ","), "sync_name,target,item_hash"; got != want {
		t.Fatalf("seen_items PK = (%s); want (%s)", got, want)
	}
}

// TestStoreRoundtrip verifies LastRun/per-target MarkSeen/Seen/SetLastRun.
func TestStoreRoundtrip(t *testing.T) {
	url := pgURL(t)
	ctx := context.Background()
	if err := MigrateUp(url, ""); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}
	st, err := New(ctx, url)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer st.Close()

	sync := fmt.Sprintf("test-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ctx := context.Background()
		cs, err := New(ctx, url)
		if err != nil {
			return
		}
		defer cs.Close()
		_, _ = cs.pool.Exec(ctx, `DELETE FROM seen_items WHERE sync_name=$1`, sync)
		_, _ = cs.pool.Exec(ctx, `DELETE FROM sync_state WHERE sync_name=$1`, sync)
	})
	if got, err := st.LastRun(ctx, sync); err != nil || !got.IsZero() {
		t.Fatalf("fresh LastRun = %v, %v; want zero", got, err)
	}
	at := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)
	// Same hash seen for A but not B.
	if err := st.MarkSeen(ctx, sync, "connA", "h1", at); err != nil {
		t.Fatalf("MarkSeen A: %v", err)
	}
	if seen, err := st.Seen(ctx, sync, "connA", "h1"); err != nil || !seen {
		t.Fatalf("Seen A = %v, %v; want true", seen, err)
	}
	if seen, err := st.Seen(ctx, sync, "connB", "h1"); err != nil || seen {
		t.Fatalf("Seen B = %v, %v; want false", seen, err)
	}
	if seen, err := st.Seen(ctx, sync, "connA", "h2"); err != nil || seen {
		t.Fatalf("Seen h2 = %v, %v; want false", seen, err)
	}
	// Idempotent re-mark; then B delivers the same hash independently.
	if err := st.MarkSeen(ctx, sync, "connA", "h1", at); err != nil {
		t.Fatalf("MarkSeen A again: %v", err)
	}
	if err := st.MarkSeen(ctx, sync, "connB", "h1", at); err != nil {
		t.Fatalf("MarkSeen B: %v", err)
	}
	if seen, err := st.Seen(ctx, sync, "connB", "h1"); err != nil || !seen {
		t.Fatalf("Seen B after mark = %v, %v; want true", seen, err)
	}
	var n int
	if err := st.pool.QueryRow(ctx,
		`SELECT count(*) FROM seen_items WHERE sync_name=$1 AND item_hash=$2`, sync, "h1").Scan(&n); err != nil || n != 2 {
		t.Fatalf("per-target rows = %d, %v; want 2", n, err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := st.SetLastRun(ctx, sync, now); err != nil {
		t.Fatalf("SetLastRun: %v", err)
	}
	if got, err := st.LastRun(ctx, sync); err != nil || !got.Equal(now) {
		t.Fatalf("LastRun = %v, %v; want %v", got, err, now)
	}
}

// TestUpgrade0001To0002 seeds 000001 rows, upgrades, and asserts old rows
// are preserved with an empty target and never match per-target lookups.
func TestUpgrade0001To0002(t *testing.T) {
	url := pgURL(t)
	ctx := context.Background()

	// Reset to a clean slate; tests share one database.
	raw, err := New(ctx, url)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, q := range []string{
		`DROP TABLE IF EXISTS seen_items`,
		`DROP TABLE IF EXISTS sync_state`,
		`DROP TABLE IF EXISTS schema_migrations`,
	} {
		if _, err := raw.pool.Exec(ctx, q); err != nil {
			raw.Close()
			t.Fatalf("reset %q: %v", q, err)
		}
	}
	raw.Close()

	// Stage only 000001 via the dev file:// override, then seed an old row.
	dir := t.TempDir()
	for _, f := range []string{"000001_init.up.sql", "000001_init.down.sql"} {
		b, err := migrations.FS.ReadFile(f)
		if err != nil {
			t.Fatalf("read embed %s: %v", f, err)
		}
		if err := os.WriteFile(dir+"/"+f, b, 0o644); err != nil {
			t.Fatalf("stage %s: %v", f, err)
		}
	}
	if err := MigrateUp(url, dir); err != nil {
		t.Fatalf("MigrateUp 000001: %v", err)
	}
	st, err := New(ctx, url)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sync := fmt.Sprintf("upgrade-%d", time.Now().UnixNano())
	at := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC).Truncate(time.Microsecond)
	if _, err := st.pool.Exec(ctx,
		`INSERT INTO seen_items(sync_name,item_hash,watched_at) VALUES($1,$2,$3)`, sync, "h1", at); err != nil {
		st.Close()
		t.Fatalf("seed 000001 row: %v", err)
	}
	st.Close()
	t.Cleanup(func() {
		ctx := context.Background()
		cs, err := New(ctx, url)
		if err != nil {
			return
		}
		defer cs.Close()
		_, _ = cs.pool.Exec(ctx, `DELETE FROM seen_items WHERE sync_name=$1`, sync)
		_, _ = cs.pool.Exec(ctx, `DELETE FROM sync_state WHERE sync_name=$1`, sync)
	})

	// Upgrade via embedded migrations (000001+000002).
	if err := MigrateUp(url, ""); err != nil {
		t.Fatalf("MigrateUp 000002: %v", err)
	}
	up, err := New(ctx, url)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer up.Close()

	// Old row keeps sync_name, item_hash, watched_at with target=''.
	var target string
	var watched time.Time
	if err := up.pool.QueryRow(ctx,
		`SELECT target, watched_at FROM seen_items WHERE sync_name=$1 AND item_hash=$2`, sync, "h1").Scan(&target, &watched); err != nil {
		t.Fatalf("old row missing: %v", err)
	}
	if target != "" {
		t.Fatalf("old row target = %q; want %q", target, "")
	}
	if !watched.Equal(at) {
		t.Fatalf("old row watched_at = %v; want %v", watched, at)
	}
	// Old rows never match real per-target lookups: bounded re-delivery.
	for _, conn := range []string{"connA", "connB"} {
		if seen, err := up.Seen(ctx, sync, conn, "h1"); err != nil || seen {
			t.Fatalf("Seen %s = %v, %v; want false", conn, seen, err)
		}
	}
	// After delivery, proper per-target rows coexist with the old row.
	for _, conn := range []string{"connA", "connB"} {
		if err := up.MarkSeen(ctx, sync, conn, "h1", at); err != nil {
			t.Fatalf("MarkSeen %s: %v", conn, err)
		}
		if seen, err := up.Seen(ctx, sync, conn, "h1"); err != nil || !seen {
			t.Fatalf("Seen %s after mark = %v, %v; want true", conn, seen, err)
		}
	}
	var n int
	if err := up.pool.QueryRow(ctx,
		`SELECT count(*) FROM seen_items WHERE sync_name=$1 AND item_hash=$2`, sync, "h1").Scan(&n); err != nil || n != 3 {
		t.Fatalf("rows for (sync,h1) = %d, %v; want 3 ('' + A + B)", n, err)
	}
}

// TestStoreErrorPaths verifies bad migrations path, URL, and closed pool fail.
func TestStoreErrorPaths(t *testing.T) {
	url := pgURL(t)
	ctx := context.Background()
	// Bad migrations path fails before touching the DB.
	if err := MigrateUp(url, "/nonexistent-dir"); err == nil {
		t.Fatal("bogus migrations path must fail")
	}
	// Unparseable URL fails New without I/O.
	if _, err := New(ctx, "://bogus"); err == nil {
		t.Fatal("bogus URL must fail New")
	}
	// Closed pool surfaces query errors.
	st, err := New(ctx, url)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	st.Close()
	if _, err := st.LastRun(ctx, "s"); err == nil {
		t.Fatal("closed pool LastRun must fail")
	}
	if _, err := st.Seen(ctx, "s", "t", "h"); err == nil {
		t.Fatal("closed pool Seen must fail")
	}
	if err := st.MarkSeen(ctx, "s", "t", "h", time.Now()); err == nil {
		t.Fatal("closed pool MarkSeen must fail")
	}
}
