package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/watchmesh/watchmesh/internal/model"
)

// failStore injects one error per Store method.
type failStore struct {
	lastRun                      time.Time
	seen                         map[string]time.Time
	lastRunErr, seenErr, markErr error
	setErr                       error
	setRuns                      int
}

// LastRun returns the scripted cursor or lastRunErr.
func (f *failStore) LastRun(context.Context, string) (time.Time, error) {
	if f.lastRunErr != nil {
		return time.Time{}, f.lastRunErr
	}
	return f.lastRun, nil
}

// Seen returns the scripted seen state or seenErr.
func (f *failStore) Seen(_ context.Context, sync, target, hash string) (bool, error) {
	if f.seenErr != nil {
		return false, f.seenErr
	}
	_, ok := f.seen[sync+"\x00"+target+"\x00"+hash]
	return ok, nil
}

// MarkSeen records the hash or returns markErr.
func (f *failStore) MarkSeen(_ context.Context, sync, target, hash string, at time.Time) error {
	if f.markErr != nil {
		return f.markErr
	}
	if f.seen == nil {
		f.seen = map[string]time.Time{}
	}
	f.seen[sync+"\x00"+target+"\x00"+hash] = at
	return nil
}

// SetLastRun advances the cursor or returns setErr.
func (f *failStore) SetLastRun(_ context.Context, _ string, t time.Time) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.lastRun = t
	f.setRuns++
	return nil
}

// TestSyncStoreErrors verifies each Store failure mode surfaces and holds state.
func TestSyncStoreErrors(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)
	boom := errors.New("boom")

	tg := &fakeTarget{}
	if err := Sync(ctx, "s", &fakeSource{items: []model.WatchItem{item(1, at)}}, []Target{tg}, &failStore{lastRunErr: boom}); err == nil {
		t.Fatal("LastRun error must fail Sync")
	}
	if len(tg.pushes) != 0 {
		t.Fatal("no pushes after LastRun error")
	}
	if err := Sync(ctx, "s", &fakeSource{err: boom}, []Target{&fakeTarget{}}, &failStore{}); err == nil {
		t.Fatal("History error must fail Sync")
	}
	if err := Sync(ctx, "s", &fakeSource{items: []model.WatchItem{item(1, at)}}, []Target{&fakeTarget{}}, &failStore{seenErr: boom}); err == nil {
		t.Fatal("Seen error must fail Sync")
	}
	st := &failStore{markErr: boom}
	if err := Sync(ctx, "s", &fakeSource{items: []model.WatchItem{item(1, at)}}, []Target{&fakeTarget{}}, st); err == nil {
		t.Fatal("MarkSeen error must surface")
	}
	if st.setRuns != 0 {
		t.Fatal("cursor must not advance when seen-state persistence fails")
	}
	if err := Sync(ctx, "s", &fakeSource{items: []model.WatchItem{item(1, at)}}, []Target{&fakeTarget{}}, &failStore{setErr: boom}); err == nil {
		t.Fatal("SetLastRun error must surface")
	}
}
