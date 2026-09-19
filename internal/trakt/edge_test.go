package trakt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if New("http://x", "c", "s", "t").Name() != "trakt" {
		t.Fatal("Name must be trakt")
	}
	if New("http://x", "c", "s", "t").httpClient() != http.DefaultClient {
		t.Fatal("nil HTTP must use DefaultClient")
	}
	custom := &http.Client{}
	if (&Client{HTTP: custom}).httpClient() != custom {
		t.Fatal("custom HTTP must win")
	}
}

// TestNewDefaultBaseURL verifies empty base falls back and slashes trim.
func TestNewDefaultBaseURL(t *testing.T) {
	if got := New("", "c", "s", "t").BaseURL; got != DefaultBaseURL {
		t.Fatalf("got %q", got)
	}
	if got := New("http://x/", "c", "s", "t").BaseURL; got != "http://x" {
		t.Fatalf("trailing slash: got %q", got)
	}
}

// TestParseTimeZero verifies empty/unparseable timestamps yield zero time.
func TestParseTimeZero(t *testing.T) {
	if !parseTime("").IsZero() {
		t.Fatal("empty must be zero")
	}
	if !parseTime("not-a-time").IsZero() {
		t.Fatal("invalid must be zero")
	}
}

// TestDoWithRetryEdges verifies build errors, passthrough, 429 retry, and cancel.
func TestDoWithRetryEdges(t *testing.T) {
	ctx := context.Background()
	buildErr := errors.New("boom")
	if _, err := doWithRetry(ctx, http.DefaultClient, func() (*http.Request, error) {
		return nil, buildErr
	}); err != buildErr {
		t.Fatalf("build err: %v", err)
	}
	if _, err := doWithRetry(ctx, http.DefaultClient, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "GET", closedURL(t)+"/x", nil)
	}); err == nil {
		t.Fatal("Do error must surface")
	}
	// 500 passes through without retry.
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(500)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	resp, err := doWithRetry(ctx, srv.Client(), func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "GET", srv.URL+"/x", nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 500 || hits != 1 {
		t.Fatalf("status=%d hits=%d", resp.StatusCode, hits)
	}
	// 429 with Retry-After retries once.
	hits = 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv2.Close()
	resp, err = doWithRetry(ctx, srv2.Client(), func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "GET", srv2.URL+"/x", nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || hits != 2 {
		t.Fatalf("status=%d hits=%d", resp.StatusCode, hits)
	}
	// Second build fails after a 429.
	calls := 0
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(429)
	}))
	defer srv3.Close()
	if _, err := doWithRetry(ctx, srv3.Client(), func() (*http.Request, error) {
		calls++
		if calls == 2 {
			return nil, buildErr
		}
		return http.NewRequestWithContext(ctx, "GET", srv3.URL+"/x", nil)
	}); err != buildErr {
		t.Fatalf("second build err: %v calls=%d", err, calls)
	}
	// Cancel during the Retry-After sleep aborts.
	srv4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(429)
	}))
	defer srv4.Close()
	ctx4, cancel4 := context.WithCancel(ctx)
	time.AfterFunc(20*time.Millisecond, cancel4)
	if _, err := doWithRetry(ctx4, srv4.Client(), func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx4, "GET", srv4.URL+"/x", nil)
	}); err == nil {
		t.Fatal("cancel during retry sleep must fail")
	}
}

// TestFetchKindEdges verifies transport failures, ID fallbacks, and bad bodies.
func TestFetchKindEdges(t *testing.T) {
	ctx := context.Background()
	// Build error: control byte in base URL fails before any I/O.
	if _, err := New("http://bad-\x7f-host", "c", "s", "t").History(ctx, time.Time{}); err == nil {
		t.Fatal("bad base URL must fail")
	}
	// Do error: closed server.
	if _, err := New(closedURL(t), "c", "s", "t").History(ctx, time.Time{}); err == nil {
		t.Fatal("closed server must fail")
	}
	// Episode ID fallbacks: episode IMDB wins, show TMDB/TVDB fill zeros.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/movies") {
			fmt.Fprint(w, `[]`)
			return
		}
		if strings.Contains(r.URL.Path, "/episodes") {
			if r.URL.Query().Get("page") != "1" {
				fmt.Fprint(w, `[]`)
				return
			}
			fmt.Fprint(w, `[{"watched_at":"2026-05-13T19:00:00.000Z","episode":{"season":2,"number":3,"ids":{"trakt":5,"imdb":"tt999"}},"show":{"title":"S","year":2020,"ids":{"trakt":2,"tmdb":777,"tvdb":888}}}]`)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	got, err := New(srv.URL, "c", "s", "t").History(ctx, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got=%v", got)
	}
	e := got[0]
	if e.IDs.Trakt != 5 || e.IDs.IMDB != "tt999" || e.IDs.TMDB != 777 || e.IDs.TVDB != 888 {
		t.Fatalf("ids=%+v", e.IDs)
	}
	if e.Season != 2 || e.Episode != 3 {
		t.Fatalf("ep=%+v", e)
	}
	// Malformed episodes body errors even when movies are fine.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/movies") {
			fmt.Fprint(w, `[]`)
			return
		}
		fmt.Fprint(w, `not json`)
	}))
	defer srv2.Close()
	if _, err := New(srv2.URL, "c", "s", "t").History(ctx, time.Time{}); err == nil {
		t.Fatal("malformed episodes must fail")
	}
	// Episodes endpoint error surfaces.
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/movies") {
			fmt.Fprint(w, `[]`)
			return
		}
		w.WriteHeader(500)
		fmt.Fprint(w, `{}`)
	}))
	defer srv3.Close()
	if _, err := New(srv3.URL, "c", "s", "t").History(ctx, time.Time{}); err == nil {
		t.Fatal("episodes 500 must fail")
	}
}

// TestPushEdges verifies zero WatchedAt skips and transport failures surface.
// TestPushNotFound verifies not_found IDs are omitted from delivered.
func TestPushNotFound(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"added":{},"not_found":{"movies":[{"ids":{"trakt":2}}],"shows":[{"ids":{"trakt":1}}],"episodes":[{"ids":{"tmdb":999}},{"ids":{"tmdb":603}}]}}`)
	}))
	defer srv.Close()
	at := time.Now()
	items := []model.WatchItem{
		{IDs: model.IDs{Trakt: 1, TMDB: 603}, MediaType: "movie", WatchedAt: at},
		{IDs: model.IDs{Trakt: 2}, MediaType: "movie", WatchedAt: at},
		{IDs: model.IDs{Trakt: 3, TMDB: 999}, MediaType: "episode", WatchedAt: at},
	}
	delivered, err := New(srv.URL, "c", "s", "t").Push(ctx, items)
	if err != nil {
		t.Fatal(err)
	}
	// trakt:2 (same-category match) and tmdb:999 (same-category match) drop;
	// cross-category tmdb:603/trakt:1 entries must not drop the movie.
	if len(delivered) != 1 || delivered[0].IDs.Trakt != 1 {
		t.Fatalf("delivered=%+v want only trakt:1", delivered)
	}
}

func TestPushEdges(t *testing.T) {
	ctx := context.Background()
	// Zero WatchedAt is skipped, never stamped with a fabricated date.
	var gotMovies int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v map[string][]map[string]any
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			t.Error(err)
		}
		gotMovies = len(v["movies"])
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	if delivered, err := New(srv.URL, "c", "s", "t").Push(ctx, []model.WatchItem{{IDs: model.IDs{Trakt: 1}, MediaType: "movie"}}); err != nil {
		t.Fatal(err)
	} else if len(delivered) != 0 {
		t.Fatal("zero WatchedAt must be omitted from delivered")
	}
	if gotMovies != 0 {
		t.Fatal("zero WatchedAt must be skipped, not stamped")
	}
	items := []model.WatchItem{{IDs: model.IDs{Trakt: 1}, MediaType: "movie", WatchedAt: time.Now()}}
	if delivered, err := New(srv.URL, "c", "s", "t").Push(ctx, items); err != nil {
		t.Fatal(err)
	} else if len(delivered) != 1 {
		t.Fatalf("delivered=%d want 1", len(delivered))
	}
	if _, err := New("http://bad-\x7f-host", "c", "s", "t").Push(ctx, items); err == nil {
		t.Fatal("bad base URL must fail build")
	}
	if _, err := New(closedURL(t), "c", "s", "t").Push(ctx, items); err == nil {
		t.Fatal("closed server must fail Do")
	}
}

// TestAuthEdges verifies device-flow transport, body, poll, and cancel errors.
func TestAuthEdges(t *testing.T) {
	ctx := context.Background()
	bad := New("http://bad-\x7f-host", "c", "s", "")
	if _, err := bad.RequestDeviceCode(ctx); err == nil {
		t.Fatal("code build must fail")
	}
	if _, err := bad.PollDeviceToken(ctx, "dev", 0); err == nil {
		t.Fatal("poll build must fail")
	}
	shut := New(closedURL(t), "c", "s", "")
	if _, err := shut.RequestDeviceCode(ctx); err == nil {
		t.Fatal("code Do must fail")
	}
	if _, err := shut.PollDeviceToken(ctx, "dev", 0); err == nil {
		t.Fatal("poll Do must fail")
	}
	// Malformed + empty device_code.
	for _, body := range []string{`not json`, `{}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, body)
		}))
		_, err := New(srv.URL, "c", "s", "").RequestDeviceCode(ctx)
		srv.Close()
		if err == nil {
			t.Fatalf("body %q must fail", body)
		}
	}
	// Token endpoint hard error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	_, err := New(srv.URL, "c", "s", "").PollDeviceToken(ctx, "dev", 0)
	srv.Close()
	if err == nil {
		t.Fatal("token 500 must fail")
	}
	// Cancelled context stops the poll loop immediately.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := New("http://x", "c", "s", "").PollDeviceToken(cancelled, "dev", 0); err == nil {
		t.Fatal("cancelled ctx must fail")
	}
	// Pending then success honors a nonzero interval.
	var n int
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(400)
			return
		}
		fmt.Fprint(w, `{"access_token":"tok"}`)
	}))
	defer srv2.Close()
	tok, err := New(srv2.URL, "c", "s", "").PollDeviceToken(ctx, "dev", time.Millisecond)
	if err != nil || tok != "tok" || n != 2 {
		t.Fatalf("tok=%q err=%v n=%d", tok, err, n)
	}
	// Cancel during the sleep aborts.
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
	}))
	defer srv3.Close()
	ctx3, cancel3 := context.WithCancel(ctx)
	time.AfterFunc(20*time.Millisecond, cancel3)
	if _, err := New(srv3.URL, "c", "s", "").PollDeviceToken(ctx3, "dev", 5*time.Second); err == nil {
		t.Fatal("cancel during sleep must fail")
	}
}
