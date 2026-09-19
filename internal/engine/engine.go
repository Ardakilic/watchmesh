// Package engine fans out syncs from one source to N targets.
package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/watchmesh/watchmesh/internal/model"
)

// Source provides watch history since a cursor.
type Source interface {
	History(ctx context.Context, since time.Time) ([]model.WatchItem, error)
}

// Target receives fresh watch history.
type Target interface {
	Push(ctx context.Context, items []model.WatchItem) error
}

// Store persists cursors and seen hashes; *store.Store satisfies it.
type Store interface {
	LastRun(ctx context.Context, syncName string) (time.Time, error)
	Seen(ctx context.Context, syncName, hash string) (bool, error)
	MarkSeen(ctx context.Context, syncName, hash string, watchedAt time.Time) error
	SetLastRun(ctx context.Context, syncName string, t time.Time) error
}

// Sync runs since→History→hash-diff→fan-out Push→record state.
// Fresh hashes are marked seen only for targets whose Push succeeded, so one
// failing target never blocks the others. SetLastRun advances to the
// window-end captured before History iff at least one target fully succeeded
// (Push plus all MarkSeen writes); an empty window pushes nothing and touches
// no state. History gaps are non-fatal: connectors return empty,nil on
// best-effort read failures, which Sync treats as an empty window.
// Per-target errors are combined in the return value.
func Sync(ctx context.Context, syncName string, src Source, targets []Target, st Store) error {
	since, err := st.LastRun(ctx, syncName)
	if err != nil {
		return err
	}
	windowEnd := time.Now()
	items, err := src.History(ctx, since)
	if err != nil {
		return err
	}
	var fresh []model.WatchItem
	for _, it := range items {
		seen, err := st.Seen(ctx, syncName, it.Hash())
		if err != nil {
			return err
		}
		if !seen {
			fresh = append(fresh, it)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	var errs []error
	succeeded := false
	for i, tg := range targets {
		if err := tg.Push(ctx, fresh); err != nil {
			errs = append(errs, fmt.Errorf("target %d: %w", i, err))
			continue
		}
		markOK := true
		for _, it := range fresh {
			if err := st.MarkSeen(ctx, syncName, it.Hash(), it.WatchedAt); err != nil {
				errs = append(errs, fmt.Errorf("target %d mark seen: %w", i, err))
				markOK = false
			}
		}
		if markOK {
			succeeded = true
		}
	}
	if succeeded {
		if err := st.SetLastRun(ctx, syncName, windowEnd); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
