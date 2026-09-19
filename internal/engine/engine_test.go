package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/watchmesh/watchmesh/internal/model"
)

// fakeSource is a scripted Source returning canned items and error.
type fakeSource struct {
	items    []model.WatchItem
	err      error
	gotSince time.Time
}

// History records since and returns the scripted items and error.
func (f *fakeSource) History(_ context.Context, since time.Time) ([]model.WatchItem, error) {
	f.gotSince = since
	return f.items, f.err
}

// fakeTarget records Push calls and returns a scripted error.
type fakeTarget struct {
	name   string
	pushes [][]model.WatchItem
	err    error
}

// Name returns the fake connection name.
func (f *fakeTarget) Name() string { return f.name }

// Push records items and returns the scripted error.
func (f *fakeTarget) Push(_ context.Context, items []model.WatchItem) error {
	f.pushes = append(f.pushes, items)
	return f.err
}

// memStore is an in-memory Store: cursor plus per-sync per-target seen hashes.
type memStore struct {
	lastRun time.Time
	setRuns int
	seen    map[string]time.Time
}

// newMemStore returns an empty memStore.
func newMemStore() *memStore { return &memStore{seen: map[string]time.Time{}} }

// LastRun returns the in-memory cursor.
func (m *memStore) LastRun(_ context.Context, _ string) (time.Time, error) {
	return m.lastRun, nil
}

// Seen reports whether hash was recorded for sync and target.
func (m *memStore) Seen(_ context.Context, sync, target, hash string) (bool, error) {
	_, ok := m.seen[sync+"\x00"+target+"\x00"+hash]
	return ok, nil
}

// MarkSeen records hash for sync and target.
func (m *memStore) MarkSeen(_ context.Context, sync, target, hash string, watchedAt time.Time) error {
	m.seen[sync+"\x00"+target+"\x00"+hash] = watchedAt
	return nil
}

// SetLastRun advances the in-memory cursor and counts writes.
func (m *memStore) SetLastRun(_ context.Context, _ string, t time.Time) error {
	m.lastRun = t
	m.setRuns++
	return nil
}

// item builds a movie WatchItem with a Trakt ID and timestamp.
func item(traktID int, at time.Time) model.WatchItem {
	return model.WatchItem{
		IDs:       model.IDs{Trakt: traktID},
		MediaType: "movie",
		Title:     "T",
		WatchedAt: at,
	}
}

// TestSyncFansOut verifies every target gets all fresh items and state records.
func TestSyncFansOut(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)
	src := &fakeSource{items: []model.WatchItem{item(1, at), item(2, at)}}
	a, b := &fakeTarget{name: "a"}, &fakeTarget{name: "b"}
	st := newMemStore()

	if err := Sync(ctx, "s", src, []Target{a, b}, st); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	for i, tg := range []*fakeTarget{a, b} {
		if len(tg.pushes) != 1 || len(tg.pushes[0]) != 2 {
			t.Fatalf("target %d: got %d pushes", i, len(tg.pushes))
		}
	}
	if len(st.seen) != 4 || st.setRuns != 1 {
		t.Fatalf("state not recorded: seen=%d setRuns=%d", len(st.seen), st.setRuns)
	}
}

// TestSyncDiffFiltersSeen verifies already-seen items are not pushed.
func TestSyncDiffFiltersSeen(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)
	old, fresh := item(1, at), item(2, at)
	src := &fakeSource{items: []model.WatchItem{old, fresh}}
	tg := &fakeTarget{name: "t"}
	st := newMemStore()
	st.seen["s\x00t\x00"+old.Hash()] = at

	if err := Sync(ctx, "s", src, []Target{tg}, st); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(tg.pushes) != 1 || len(tg.pushes[0]) != 1 || tg.pushes[0][0].Hash() != fresh.Hash() {
		t.Fatalf("expected only fresh item pushed, got %+v", tg.pushes)
	}
}

// TestSyncPartialFailure verifies one failing target neither blocks others nor state.
func TestSyncPartialFailure(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)
	src := &fakeSource{items: []model.WatchItem{item(1, at)}}
	a := &fakeTarget{name: "a"}
	b := &fakeTarget{name: "b", err: errors.New("boom")}
	c := &fakeTarget{name: "c"}
	st := newMemStore()

	err := Sync(ctx, "s", src, []Target{a, b, c}, st)
	if err == nil {
		t.Fatal("expected combined error")
	}
	if len(a.pushes) != 1 || len(c.pushes) != 1 || len(b.pushes) != 1 {
		t.Fatalf("A/C must still receive items: %+v %+v %+v", len(a.pushes), len(b.pushes), len(c.pushes))
	}
	if len(st.seen) != 2 || st.setRuns != 0 {
		t.Fatalf("only successes record state, cursor held: seen=%d setRuns=%d", len(st.seen), st.setRuns)
	}
}

// TestSyncPartialFailureRerun verifies a rerun pushes only to still-missing
// targets and advances the cursor only after full success.
func TestSyncPartialFailureRerun(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)
	src := &fakeSource{items: []model.WatchItem{item(1, at), item(2, at)}}
	a := &fakeTarget{name: "a"}
	b := &fakeTarget{name: "b", err: errors.New("boom")}
	c := &fakeTarget{name: "c"}
	st := newMemStore()

	if err := Sync(ctx, "s", src, []Target{a, b, c}, st); err == nil {
		t.Fatal("expected combined error")
	}
	if st.setRuns != 0 {
		t.Fatal("cursor must hold until every target succeeds")
	}
	b.err = nil
	a.pushes, b.pushes, c.pushes = nil, nil, nil
	if err := Sync(ctx, "s", src, []Target{a, b, c}, st); err != nil {
		t.Fatalf("rerun Sync: %v", err)
	}
	if len(a.pushes) != 0 || len(c.pushes) != 0 {
		t.Fatalf("rerun must push only to B: A=%d C=%d", len(a.pushes), len(c.pushes))
	}
	if len(b.pushes) != 1 || len(b.pushes[0]) != 2 {
		t.Fatalf("rerun must push full window to B, got %+v", b.pushes)
	}
	if st.setRuns != 1 {
		t.Fatal("cursor advances only after full success")
	}
}

// TestSyncSkipsCaughtUpTarget verifies a fully-delivered target gets no Push
// while a fresh same-type target still receives the whole window.
func TestSyncSkipsCaughtUpTarget(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)
	src := &fakeSource{items: []model.WatchItem{item(1, at), item(2, at)}}
	a := &fakeTarget{name: "trakt_main"}
	b := &fakeTarget{name: "trakt_alt"}
	st := newMemStore()
	for _, it := range src.items {
		st.seen["s\x00trakt_main\x00"+it.Hash()] = at
	}

	if err := Sync(ctx, "s", src, []Target{a, b}, st); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(a.pushes) != 0 {
		t.Fatal("caught-up target must get no Push")
	}
	if len(b.pushes) != 1 || len(b.pushes[0]) != 2 {
		t.Fatalf("fresh target must receive full window, got %+v", b.pushes)
	}
	if st.setRuns != 1 {
		t.Fatal("cursor advances when all targets succeed")
	}
}

// TestSyncAllFailWritesNothing verifies total failure writes no state.
func TestSyncAllFailWritesNothing(t *testing.T) {
	ctx := context.Background()
	src := &fakeSource{items: []model.WatchItem{item(1, time.Now())}}
	tg := &fakeTarget{err: errors.New("down")}
	st := newMemStore()

	if err := Sync(ctx, "s", src, []Target{tg}, st); err == nil {
		t.Fatal("expected error")
	}
	if len(st.seen) != 0 || st.setRuns != 0 {
		t.Fatal("failed sync must write nothing")
	}
}

// TestSyncEmptyWindow verifies an empty window pushes nothing and holds the cursor.
func TestSyncEmptyWindow(t *testing.T) {
	ctx := context.Background()
	before := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	src := &fakeSource{}
	tg := &fakeTarget{}
	st := newMemStore()
	st.lastRun = before

	if err := Sync(ctx, "s", src, []Target{tg}, st); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(tg.pushes) != 0 {
		t.Fatal("empty window must push nothing")
	}
	if st.setRuns != 0 || !st.lastRun.Equal(before) {
		t.Fatal("cursor must not regress on empty window")
	}
}

// TestIdempotent verifies a rerun pushes nothing and holds the cursor.
func TestIdempotent(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)
	src := &fakeSource{items: []model.WatchItem{item(1, at), item(2, at)}}
	tg := &fakeTarget{}
	st := newMemStore()

	if err := Sync(ctx, "s", src, []Target{tg}, st); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	afterFirst := st.lastRun
	tg.pushes = nil
	if err := Sync(ctx, "s", src, []Target{tg}, st); err != nil {
		t.Fatalf("rerun Sync: %v", err)
	}
	if len(tg.pushes) != 0 {
		t.Fatal("rerun must push nothing")
	}
	if !st.lastRun.Equal(afterFirst) {
		t.Fatal("cursor must not regress on rerun")
	}
}
