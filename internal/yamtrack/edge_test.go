package yamtrack

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/watchmesh/watchmesh/internal/model"
)

// closedURL returns the URL of an already-closed server for transport errors.
func closedURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

// TestNameAndHTTPClient verifies Name and the nil/custom HTTP selection.
func TestNameAndHTTPClient(t *testing.T) {
	if New("http://x", "tok").Name() != "yamtrack" {
		t.Fatal("Name must be yamtrack")
	}
	if New("http://x", "tok").httpClient() != http.DefaultClient {
		t.Fatal("nil HTTP must use DefaultClient")
	}
	custom := &http.Client{}
	if (&Client{HTTP: custom}).httpClient() != custom {
		t.Fatal("custom HTTP must win")
	}
}

// TestWebhookPayloadShapes verifies the payload struct per media shape,
// including which shapes skip (ok=false).
func TestWebhookPayloadShapes(t *testing.T) {
	tm := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)
	// Skipping shapes.
	for _, tt := range []struct {
		name string
		item model.WatchItem
	}{
		{"movie-no-tmdb", model.WatchItem{IDs: model.IDs{IMDB: "tt0"}, MediaType: "movie", WatchedAt: tm}},
		{"bare-show", model.WatchItem{IDs: model.IDs{TMDB: 5}, MediaType: "show", WatchedAt: tm}},
		{"podcast", model.WatchItem{IDs: model.IDs{TMDB: 5}, MediaType: "podcast", WatchedAt: tm}},
		{"episode-no-tmdb", model.WatchItem{IDs: model.IDs{IMDB: "tt0"}, MediaType: "episode", Season: 1, Episode: 2, WatchedAt: tm}},
		{"episode-s0e0", model.WatchItem{IDs: model.IDs{TMDB: 5}, MediaType: "episode", WatchedAt: tm}},
	} {
		if w, ok := webhookPayload(tt.item); ok {
			t.Fatalf("%s must skip, got %#v", tt.name, w)
		}
	}
	// Movie keeps tmdb (+imdb when present) and ignores the watched date.
	movie := model.WatchItem{
		IDs:       model.IDs{TMDB: 27205, IMDB: "tt0133093"},
		MediaType: "movie",
		Title:     "M",
		Season:    9,
		Episode:   8,
		WatchedAt: tm,
	}
	w, ok := webhookPayload(movie)
	if !ok {
		t.Fatal("movie with TMDB must push")
	}
	if w.Event != "Stop" {
		t.Fatalf("event=%q", w.Event)
	}
	if w.Item.Type != "Movie" || w.Item.ParentIndexNumber != nil || w.Item.IndexNumber != nil {
		t.Fatalf("item=%+v", w.Item)
	}
	if w.Item.ProviderIds["Tmdb"] != "27205" || w.Item.ProviderIds["Imdb"] != "tt0133093" {
		t.Fatalf("ids=%v", w.Item.ProviderIds)
	}
	if played, ok := w.Item.UserData["Played"]; !ok || played != true {
		t.Fatalf("userData=%v", w.Item.UserData)
	}
	// Movie without IMDb omits the Imdb id.
	plain, ok := webhookPayload(model.WatchItem{IDs: model.IDs{TMDB: 27205}, MediaType: "movie", WatchedAt: tm})
	_, present := plain.Item.ProviderIds["Imdb"]
	if !ok || present {
		t.Fatalf("bare movie ids=%v", plain.Item.ProviderIds)
	}
	// Episode numbering rides season/episode and the series TMDB block.
	ep := model.WatchItem{IDs: model.IDs{TMDB: 155, IMDB: "tt2339"}, MediaType: "episode",
		Title: "S", Season: 2, Episode: 3, WatchedAt: tm}
	we, ok := webhookPayload(ep)
	if !ok {
		t.Fatal("numbered episode with TMDB must push")
	}
	if we.Item.Type != "Episode" || we.Item.SeriesName != "S" {
		t.Fatalf("item=%+v", we.Item)
	}
	if *we.Item.ParentIndexNumber != 2 || *we.Item.IndexNumber != 3 {
		t.Fatalf("numbering=%d,%d", *we.Item.ParentIndexNumber, *we.Item.IndexNumber)
	}
	if len(we.Item.ProviderIds) != 1 || we.Item.ProviderIds["Imdb"] != "tt2339" {
		t.Fatalf("episode item ids=%v, only Imdb resolves episodes", we.Item.ProviderIds)
	}
	if we.Series.ProviderIds["Tmdb"] != "155" {
		t.Fatalf("series=%+v", we.Series)
	}
	// Episode with zero WatchedAt still pushes: the webhook stamps upstream now.
	if _, ok := webhookPayload(model.WatchItem{IDs: model.IDs{TMDB: 155}, MediaType: "episode",
		Season: 1, Episode: 1}); !ok {
		t.Fatal("episode without WatchedAt must push")
	}
}

// TestPushTokenEscape verifies the token is path-escaped, not host/auth.
func TestPushTokenEscape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.EscapedPath(); got != "/webhook/jellyfin/a%2Fb" {
			t.Errorf("escaped path=%q", got)
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	if delivered, err := New(srv.URL, "a/b").Push(context.Background(),
		[]model.WatchItem{item("movie", 1)}); err != nil || len(delivered) != 1 {
		t.Fatalf("delivered=%d err=%v", len(delivered), err)
	}
}

// TestPushTransportErrors verifies request build/Do failures surface.
func TestPushTransportErrors(t *testing.T) {
	ctx := context.Background()
	it := []model.WatchItem{{IDs: model.IDs{TMDB: 1}, MediaType: "movie", WatchedAt: time.Now()}}
	if _, err := New("http://bad-\x7f-host", "t").Push(ctx, it); err == nil {
		t.Fatal("bad URL must fail build")
	}
	if _, err := New(closedURL(t), "t").Push(ctx, it); err == nil {
		t.Fatal("closed server must fail Do")
	}
}

// TestPushRetry429 verifies 429→200 delivers and 429→429 errors with nothing marked.
func TestPushRetry429(t *testing.T) {
	ctx := context.Background()
	newItem := func() []model.WatchItem {
		return []model.WatchItem{{IDs: model.IDs{TMDB: 1}, MediaType: "movie", WatchedAt: time.Now()}}
	}
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	if delivered, err := New(srv.URL, "t").Push(ctx, newItem()); err != nil || len(delivered) != 1 {
		t.Fatalf("429→200: delivered=%d err=%v", len(delivered), err)
	}
	if n != 2 {
		t.Fatalf("requests=%d want 2", n)
	}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv2.Close()
	if delivered, err := New(srv2.URL, "t").Push(ctx, newItem()); err == nil || len(delivered) != 0 {
		t.Fatalf("429→429: delivered=%d err=%v, want error and nothing marked", len(delivered), err)
	}
}

// TestPushRetryHTTPDate verifies an HTTP-date Retry-After is honored (future
// waits, past retries immediately) instead of falling back to 1s.
func TestPushRetryHTTPDate(t *testing.T) {
	ctx := context.Background()
	newItem := func() []model.WatchItem {
		return []model.WatchItem{{IDs: model.IDs{TMDB: 1}, MediaType: "movie", WatchedAt: time.Now()}}
	}
	serve := func(date string) (*httptest.Server, *int) {
		n := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n++
			if n == 1 {
				w.Header().Set("Retry-After", date)
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			fmt.Fprint(w, `{}`)
		}))
		return srv, &n
	}
	srv, n := serve(time.Now().Add(500 * time.Millisecond).UTC().Format(http.TimeFormat))
	defer srv.Close()
	start := time.Now()
	if delivered, err := New(srv.URL, "t").Push(ctx, newItem()); err != nil || len(delivered) != 1 || *n != 2 {
		t.Fatalf("future date: delivered=%d err=%v requests=%d", len(delivered), err, *n)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("future date: took %v, want prompt retry", d)
	}
	srv2, n2 := serve(time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat))
	defer srv2.Close()
	start = time.Now()
	if delivered, err := New(srv2.URL, "t").Push(ctx, newItem()); err != nil || len(delivered) != 1 || *n2 != 2 {
		t.Fatalf("past date: delivered=%d err=%v requests=%d", len(delivered), err, *n2)
	}
	if d := time.Since(start); d > 900*time.Millisecond {
		t.Fatalf("past date: took %v, want immediate retry", d)
	}
}

// TestPushRetryCancel verifies cancelling during the retry wait aborts Push
// promptly with the context error and nothing delivered.
func TestPushRetryCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	it := []model.WatchItem{{IDs: model.IDs{TMDB: 1}, MediaType: "movie", WatchedAt: time.Now()}}
	delivered, err := New(srv.URL, "t").Push(ctx, it)
	if err != context.Canceled || len(delivered) != 0 {
		t.Fatalf("cancel: delivered=%d err=%v, want ctx canceled and nothing", len(delivered), err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("cancel: took %v, want prompt abort", d)
	}
}

// TestValidateTokenTransportErrors verifies request build/Do failures surface.
func TestValidateTokenTransportErrors(t *testing.T) {
	ctx := context.Background()
	if err := New("http://bad-\x7f-host", "t").ValidateToken(ctx); err == nil {
		t.Fatal("bad URL must fail")
	}
	if err := New(closedURL(t), "t").ValidateToken(ctx); err == nil {
		t.Fatal("closed server must fail")
	}
}

// TestDoWithRetrySecondBuildFails verifies a 429 followed by a build error surfaces.
func TestDoWithRetrySecondBuildFails(t *testing.T) {
	ctx := context.Background()
	buildErr := errors.New("boom")
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(429)
	}))
	defer srv.Close()
	if _, err := doWithRetry(ctx, srv.Client(), func() (*http.Request, error) {
		calls++
		if calls == 2 {
			return nil, buildErr
		}
		return http.NewRequestWithContext(ctx, "GET", srv.URL+"/x", nil)
	}); err != buildErr {
		t.Fatalf("second build err: %v calls=%d", err, calls)
	}
}
