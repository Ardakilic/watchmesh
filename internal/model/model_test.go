package model

import (
	"testing"
	"time"
)

func TestHashStable(t *testing.T) {
	at := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)
	a := WatchItem{IDs: IDs{Trakt: 1, IMDB: "tt1201607", TMDB: 603}, MediaType: "movie", WatchedAt: at}
	b := a
	if a.Hash() != b.Hash() {
		t.Fatal("identical items must hash identically")
	}
	if len(a.Hash()) != 40 {
		t.Fatalf("want 40-char sha1 hex, got %q", a.Hash())
	}
}

func TestHashDistinct(t *testing.T) {
	at := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)
	base := WatchItem{IDs: IDs{Trakt: 1}, MediaType: "movie", WatchedAt: at}
	for name, other := range map[string]WatchItem{
		"id":   {IDs: IDs{Trakt: 2}, MediaType: "movie", WatchedAt: at},
		"type": {IDs: IDs{Trakt: 1}, MediaType: "episode", Season: 1, Episode: 1, WatchedAt: at},
		"time": {IDs: IDs{Trakt: 1}, MediaType: "movie", WatchedAt: at.Add(time.Second)},
	} {
		if base.Hash() == other.Hash() {
			t.Fatalf("%s: different items must hash differently", name)
		}
	}
}
