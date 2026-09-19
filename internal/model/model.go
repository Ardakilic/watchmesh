// Package model defines shared watch history types.
package model

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// IDs carries stable external identifiers for one media item.
type IDs struct {
	Trakt int
	Simkl int
	IMDB  string
	TMDB  int
	TVDB  int
}

// WatchItem is one watched media entry exchanged via Source.History and Target.Push.
type WatchItem struct {
	IDs       IDs
	MediaType string // movie|show|episode
	Title     string
	Year      int
	Season    int
	Episode   int
	WatchedAt time.Time
}

// Hash is the idempotency key stored in seen_items.
// Format: sha1(mediatype|trakt|simkl|imdb|tmdb|tvdb|season|episode|watchedAt.UTC).
func (w WatchItem) Hash() string {
	s := fmt.Sprintf("%s|%d|%d|%s|%d|%d|%d|%d|%s",
		strings.ToLower(w.MediaType),
		w.IDs.Trakt, w.IDs.Simkl, w.IDs.IMDB, w.IDs.TMDB, w.IDs.TVDB,
		w.Season, w.Episode,
		w.WatchedAt.UTC().Format(time.RFC3339Nano),
	)
	sum := sha1.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}
