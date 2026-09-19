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

// Target receives fresh watch history. Name is the connection name from
// config and keys per-target delivery state; it must never be the connector
// type, since two connections may share one type.
type Target interface {
	Name() string
	Push(ctx context.Context, items []model.WatchItem) error
}

// Store persists cursors and per-target seen hashes; *store.Store satisfies it.
type Store interface {
	LastRun(ctx context.Context, syncName string) (time.Time, error)
	Seen(ctx context.Context, syncName, target, hash string) (bool, error)
	MarkSeen(ctx context.Context, syncName, target, hash string, watchedAt time.Time) error
	SetLastRun(ctx context.Context, syncName string, t time.Time) error
}

// Sync runs since→History→per-target diff→fan-out Push→record state.
// Each target is diffed against its own seen rows (keyed by connection name)
// and gets only its missing items; targets with nothing missing get no Push
// and count as succeeded. Hashes are marked seen per target only after that
// target's Push succeeded, so one failing target never blocks the others.
// SetLastRun advances to the window-end captured before History iff every
// target fully succeeded (Push plus all MarkSeen writes); a held cursor plus
// per-target diffs makes reruns push only to still-missing targets. A window
// with nothing fresh anywhere pushes nothing and touches no state. History
// gaps are non-fatal: connectors return empty,nil on best-effort read
// failures, which Sync treats as an empty window. Per-target errors are
// combined in the return value.
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
	var errs []error
	anyFresh, allOK := false, true
	for i, tg := range targets {
		name := tg.Name()
		var fresh []model.WatchItem
		for _, it := range items {
			seen, err := st.Seen(ctx, syncName, name, it.Hash())
			if err != nil {
				return err
			}
			if !seen {
				fresh = append(fresh, it)
			}
		}
		if len(fresh) == 0 {
			continue
		}
		anyFresh = true
		if err := tg.Push(ctx, fresh); err != nil {
			errs = append(errs, fmt.Errorf("target %d: %w", i, err))
			allOK = false
			continue
		}
		markOK := true
		for _, it := range fresh {
			if err := st.MarkSeen(ctx, syncName, name, it.Hash(), it.WatchedAt); err != nil {
				errs = append(errs, fmt.Errorf("target %d mark seen: %w", i, err))
				markOK = false
			}
		}
		if !markOK {
			allOK = false
		}
	}
	if !anyFresh {
		return nil
	}
	if allOK {
		if err := st.SetLastRun(ctx, syncName, windowEnd); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
