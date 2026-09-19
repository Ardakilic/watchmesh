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
// type, since two connections may share one type. Push returns the subset of
// items actually delivered, so the engine marks seen only what reached the
// service; skipped items (unsupported type, missing IDs, zero timestamp) are
// reported by omission, never silently marked. Per-item connectors also
// return items delivered before a mid-loop failure alongside the error.
type Target interface {
	Name() string
	Push(ctx context.Context, items []model.WatchItem) ([]model.WatchItem, error)
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
// and count as succeeded. Only items in Push's delivered set are marked seen,
// so a target that skips an item never records it as delivered; partial
// deliveries returned alongside an error are still marked, while the cursor
// stays held so the rerun covers exactly the missing remainder. A nil-error
// subset delivery also holds the cursor: the skipped items stay unseen and an
// advanced window could exclude them from History forever. SetLastRun
// advances to the window-end captured before History iff every target fully
// succeeded (Push plus all MarkSeen writes); a held cursor plus per-target
// diffs makes reruns push only to still-missing targets. A window with
// nothing fresh anywhere pushes nothing and touches no state. History gaps
// are non-fatal: connectors return empty,nil on best-effort read failures,
// which Sync treats as an empty window. Per-target errors are combined in
// the return value.
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
		delivered, err := tg.Push(ctx, fresh)
		if err != nil {
			errs = append(errs, fmt.Errorf("target %d: %w", i, err))
			allOK = false
		}
		markOK := true
		for _, it := range delivered {
			if err := st.MarkSeen(ctx, syncName, name, it.Hash(), it.WatchedAt); err != nil {
				errs = append(errs, fmt.Errorf("target %d mark seen: %w", i, err))
				markOK = false
			}
		}
		if !markOK {
			allOK = false
		}
		if len(delivered) != len(fresh) {
			// Nil-error subset: skips stay unseen, so hold the cursor or
			// the advanced window would exclude them from History forever.
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
