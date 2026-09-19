package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
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
}

// TestStoreRoundtrip verifies LastRun/MarkSeen/Seen/SetLastRun against PG.
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
	if err := st.MarkSeen(ctx, sync, "h1", at); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	if seen, err := st.Seen(ctx, sync, "h1"); err != nil || !seen {
		t.Fatalf("Seen = %v, %v; want true", seen, err)
	}
	if seen, err := st.Seen(ctx, sync, "h2"); err != nil || seen {
		t.Fatalf("Seen = %v, %v; want false", seen, err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := st.SetLastRun(ctx, sync, now); err != nil {
		t.Fatalf("SetLastRun: %v", err)
	}
	if got, err := st.LastRun(ctx, sync); err != nil || !got.Equal(now) {
		t.Fatalf("LastRun = %v, %v; want %v", got, err, now)
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
	if _, err := st.Seen(ctx, "s", "h"); err == nil {
		t.Fatal("closed pool Seen must fail")
	}
}
