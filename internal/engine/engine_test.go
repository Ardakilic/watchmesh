package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/watchmesh/watchmesh/internal/model"
)

type fakeSource struct {
	items    []model.WatchItem
	err      error
	gotSince time.Time
}

func (f *fakeSource) History(_ context.Context, since time.Time) ([]model.WatchItem, error) {
	f.gotSince = since
	return f.items, f.err
}

type fakeTarget struct {
	pushes [][]model.WatchItem
	err    error
}

func (f *fakeTarget) Push(_ context.Context, items []model.WatchItem) error {
	f.pushes = append(f.pushes, items)
	return f.err
}

type memStore struct {
	lastRun time.Time
	setRuns int
	seen    map[string]time.Time
}

func newMemStore() *memStore { return &memStore{seen: map[string]time.Time{}} }

func (m *memStore) LastRun(_ context.Context, _ string) (time.Time, error) {
	return m.lastRun, nil
}

func (m *memStore) Seen(_ context.Context, sync, hash string) (bool, error) {
	_, ok := m.seen[sync+"\x00"+hash]
	return ok, nil
}

func (m *memStore) MarkSeen(_ context.Context, sync, hash string, watchedAt time.Time) error {
	m.seen[sync+"\x00"+hash] = watchedAt
	return nil
}

func (m *memStore) SetLastRun(_ context.Context, _ string, t time.Time) error {
	m.lastRun = t
	m.setRuns++
	return nil
}

func item(traktID int, at time.Time) model.WatchItem {
	return model.WatchItem{
		IDs:       model.IDs{Trakt: traktID},
		MediaType: "movie",
		Title:     "T",
		WatchedAt: at,
	}
}

func TestSyncFansOut(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)
	src := &fakeSource{items: []model.WatchItem{item(1, at), item(2, at)}}
	a, b := &fakeTarget{}, &fakeTarget{}
	st := newMemStore()

	if err := Sync(ctx, "s", src, []Target{a, b}, st); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	for i, tg := range []*fakeTarget{a, b} {
		if len(tg.pushes) != 1 || len(tg.pushes[0]) != 2 {
			t.Fatalf("target %d: got %d pushes", i, len(tg.pushes))
		}
	}
	if len(st.seen) != 2 || st.setRuns != 1 {
		t.Fatalf("state not recorded: seen=%d setRuns=%d", len(st.seen), st.setRuns)
	}
}

func TestSyncDiffFiltersSeen(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)
	old, fresh := item(1, at), item(2, at)
	src := &fakeSource{items: []model.WatchItem{old, fresh}}
	tg := &fakeTarget{}
	st := newMemStore()
	st.seen["s\x00"+old.Hash()] = at

	if err := Sync(ctx, "s", src, []Target{tg}, st); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(tg.pushes) != 1 || len(tg.pushes[0]) != 1 || tg.pushes[0][0].Hash() != fresh.Hash() {
		t.Fatalf("expected only fresh item pushed, got %+v", tg.pushes)
	}
}

func TestSyncPartialFailure(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)
	src := &fakeSource{items: []model.WatchItem{item(1, at)}}
	a := &fakeTarget{}
	b := &fakeTarget{err: errors.New("boom")}
	c := &fakeTarget{}
	st := newMemStore()

	err := Sync(ctx, "s", src, []Target{a, b, c}, st)
	if err == nil {
		t.Fatal("expected combined error")
	}
	if len(a.pushes) != 1 || len(c.pushes) != 1 || len(b.pushes) != 1 {
		t.Fatalf("A/C must still receive items: %+v %+v %+v", len(a.pushes), len(b.pushes), len(c.pushes))
	}
	if len(st.seen) != 1 || st.setRuns != 1 {
		t.Fatalf("successes must record state: seen=%d setRuns=%d", len(st.seen), st.setRuns)
	}
}

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
