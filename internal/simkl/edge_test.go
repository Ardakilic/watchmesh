package simkl

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

// TestPushNotFound verifies not_found IDs are omitted from delivered,
// and an absent not_found section leaves delivered unchanged.
func TestPushNotFound(t *testing.T) {
	ctx := context.Background()
	at := time.Now()
	items := []model.WatchItem{
		{IDs: model.IDs{Simkl: 1, TMDB: 100}, MediaType: "movie", WatchedAt: at},
		{IDs: model.IDs{Simkl: 2}, MediaType: "movie", WatchedAt: at},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"not_found":{"movies":[{"ids":{"simkl":2}}],"episodes":[{"ids":{"tmdb":100}}]}}`)
	}))
	defer srv.Close()
	delivered, err := New(srv.URL, "c", "t").Push(ctx, items)
	if err != nil {
		t.Fatal(err)
	}
	// simkl:2 drops (same-category); the cross-category tmdb:100 episode
	// entry must not drop the movie.
	if len(delivered) != 1 || delivered[0].IDs.Simkl != 1 {
		t.Fatalf("delivered=%+v want only simkl:1", delivered)
	}

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{}`)
	}))
	defer plain.Close()
	delivered, err = New(plain.URL, "c", "t").Push(ctx, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 2 {
		t.Fatalf("delivered=%d want 2 without not_found", len(delivered))
	}
}

// TestNameAndHTTPClient verifies Name and the nil/custom HTTP selection.
func TestNameAndHTTPClient(t *testing.T) {
	if New("http://x", "c", "t").Name() != "simkl" {
		t.Fatal("Name must be simkl")
	}
	if New("http://x", "c", "t").httpClient() != http.DefaultClient {
		t.Fatal("nil HTTP must use DefaultClient")
	}
	custom := &http.Client{}
	if (&Client{HTTP: custom}).httpClient() != custom {
		t.Fatal("custom HTTP must win")
	}
}

// TestNewDefaultBaseURL verifies empty base falls back to DefaultBaseURL.
func TestNewDefaultBaseURL(t *testing.T) {
	if got := New("", "c", "t").BaseURL; got != DefaultBaseURL {
		t.Fatalf("got %q", got)
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

// TestDoWithRetryEdges verifies build errors, passthrough, burst 429 retry,
// daily-limit 429s, and cancel.
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
	// Burst 429 retries once after a fixed ~1s wait; the huge daily-reset
	// Retry-After must be ignored.
	hits = 0
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.Header().Set("Retry-After", "45000")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":"rate_limit"}`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv2.Close()
	start := time.Now()
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
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("burst retry waited %v; Retry-After must be ignored", d)
	}
	// Daily caps report out instead of sleeping out the daily reset.
	for _, kind := range []string{"user_limit_exceeded", "app_limit_exceeded"} {
		srvD := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "45000")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(429)
			fmt.Fprintf(w, `{"error":%q}`, kind)
		}))
		start := time.Now()
		_, err := doWithRetry(ctx, srvD.Client(), func() (*http.Request, error) {
			return http.NewRequestWithContext(ctx, "GET", srvD.URL+"/x", nil)
		})
		srvD.Close()
		if err == nil || !strings.Contains(err.Error(), "daily limit") {
			t.Fatalf("%s: err=%v want daily-limit error", kind, err)
		}
		if d := time.Since(start); d > 900*time.Millisecond {
			t.Fatalf("%s: waited %v; daily caps must return promptly", kind, d)
		}
	}
	// Second build fails after a burst 429.
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
	// Cancel during the retry sleep aborts.
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

// TestThrottlePOSTSecondCall verifies the 1 POST/s spacing sleep.
func TestThrottlePOSTSecondCall(t *testing.T) {
	c := New("http://x", "c", "t")
	c.lastPOST = time.Now().Add(-900 * time.Millisecond)
	start := time.Now()
	c.throttlePOST()
	if d := time.Since(start); d < 50*time.Millisecond {
		t.Fatalf("second call must sleep, slept %v", d)
	}
	if c.lastPOST.Before(start) {
		t.Fatal("lastPOST must advance")
	}
}

// TestActivitiesTransportErrors verifies activities build/Do failures surface.
func TestActivitiesTransportErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := New("http://bad-\x7f-host", "c", "t").History(ctx, time.Time{}); err == nil {
		t.Fatal("bad base must fail activities build")
	}
	if _, err := New(closedURL(t), "c", "t").History(ctx, time.Time{}); err == nil {
		t.Fatal("closed must fail activities Do")
	}
}

// TestHistoryFetchError verifies an all-items failure fails History.
func TestHistoryFetchError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sync/activities" {
			fmt.Fprint(w, `{"all":"2026-05-15T22:30:00Z","movies":{"all":"2026-05-15T22:30:00Z"},"tv_shows":{"all":"2026-05-01T00:00:00Z"},"anime":{"all":"2026-05-01T00:00:00Z"}}`)
			return
		}
		w.WriteHeader(500)
	}))
	defer srv.Close()
	since := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	if _, err := New(srv.URL, "c", "t").History(context.Background(), since); err == nil {
		t.Fatal("fetch error must fail History")
	}
}

// TestFetchCategoryEdges verifies transport, body, nested-shape, and filter edges.
func TestFetchCategoryEdges(t *testing.T) {
	ctx := context.Background()
	bad := New("http://bad-\x7f-host", "c", "t")
	if _, err := bad.fetchCategory(ctx, "movies", time.Time{}); err == nil {
		t.Fatal("bad base must fail build")
	}
	shut := New(closedURL(t), "c", "t")
	if _, err := shut.fetchCategory(ctx, "movies", time.Time{}); err == nil {
		t.Fatal("closed must fail Do")
	}
	// Non-200 + malformed movies.
	for _, tt := range []struct {
		status int
		body   string
	}{
		{500, `{}`},
		{200, `not json`},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tt.status)
			fmt.Fprint(w, tt.body)
		}))
		_, err := New(srv.URL, "c", "t").fetchCategory(ctx, "movies", time.Time{})
		srv.Close()
		if err == nil {
			t.Fatalf("%v must fail", tt)
		}
	}
	// Movies fetch one bucket; date_from is sent and old entries the server
	// still returns pass through (over-delivery is accepted).
	since := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/completed") {
			t.Errorf("movies use only the completed bucket, got %s", r.URL.Path)
		}
		if r.URL.Query().Get("date_from") == "" {
			t.Error("missing date_from")
		}
		fmt.Fprint(w, `{"movies":[
			{"status":"completed","last_watched_at":"2026-05-15T22:30:00Z","movie":{"title":"Fallback","year":2007,"ids":{"simkl":1}}},
			{"status":"completed","last_watched_at":"2026-05-01T00:00:00Z","movie":{"title":"Old","year":2001,"ids":{"simkl":2}}}
		]}`)
	}))
	defer srv.Close()
	got, err := New(srv.URL, "c", "t").fetchCategory(ctx, "movies", since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Title != "Fallback" || got[1].Title != "Old" {
		t.Fatalf("got=%+v want Fallback + over-delivered Old", got)
	}
	if got[0].WatchedAt.Day() != 15 {
		t.Fatalf("last_watched not used: %+v", got[0])
	}
	// anime uses its own top-level key in nested shape.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/watching") {
			fmt.Fprint(w, `{"anime":[]}`)
			return
		}
		fmt.Fprint(w, `{"anime":[{"status":"completed","last_watched_at":"2026-05-13T19:00:00Z","show":{"title":"S","ids":{"simkl":9}},"seasons":[{"number":1,"episodes":[{"number":2,"watched_at":"2026-05-13T19:00:00Z"}]}]}]}`)
	}))
	defer srv2.Close()
	got, err = New(srv2.URL, "c", "t").fetchCategory(ctx, "anime", time.Time{})
	if err != nil || len(got) != 1 || got[0].Episode != 2 {
		t.Fatalf("got=%v err=%v", got, err)
	}
	// Malformed shows.
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `not json`)
	}))
	defer srv3.Close()
	if _, err := New(srv3.URL, "c", "t").fetchCategory(ctx, "shows", time.Time{}); err == nil {
		t.Fatal("malformed shows must fail")
	}
	// Old episodes still emit when the server over-delivers past date_from.
	srv4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("date_from") == "" {
			t.Error("missing date_from")
		}
		fmt.Fprint(w, `{"shows":[{"status":"completed","last_watched_at":"2020-01-01T00:00:00Z","show":{"title":"S","ids":{"simkl":9}},"seasons":[{"number":1,"episodes":[{"number":1,"watched_at":"2020-01-01T00:00:00Z"}]}]}]}`)
	}))
	defer srv4.Close()
	got, err = New(srv4.URL, "c", "t").fetchCategory(ctx, "shows", since)
	if err != nil || len(got) != 1 {
		t.Fatalf("over-delivered old episode must pass through: got=%v err=%v", got, err)
	}
}

// TestPushEdges verifies zero-time stamping, bare shows, and transport errors.
func TestPushEdges(t *testing.T) {
	ctx := context.Background()
	// Zero WatchedAt stamped; bare show (no season/episode) posts IDs only.
	var got struct {
		Movies []map[string]any `json:"movies"`
		Shows  []map[string]any `json:"shows"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	items := []model.WatchItem{
		{IDs: model.IDs{Simkl: 1}, MediaType: "movie"},
		{IDs: model.IDs{Simkl: 2}, MediaType: "show"},
	}
	if delivered, err := New(srv.URL, "c", "t").Push(ctx, items); err != nil {
		t.Fatal(err)
	} else if len(delivered) != 2 {
		t.Fatalf("delivered=%d want 2", len(delivered))
	}
	if len(got.Movies) != 1 || got.Movies[0]["watched_at"] == nil {
		t.Fatalf("movies=%v", got.Movies)
	}
	if len(got.Shows) != 1 || got.Shows[0]["seasons"] != nil {
		t.Fatalf("bare show=%v", got.Shows)
	}
	if _, err := New("http://bad-\x7f-host", "c", "t").Push(ctx, items); err == nil {
		t.Fatal("bad base must fail build")
	}
	if _, err := New(closedURL(t), "c", "t").Push(ctx, items); err == nil {
		t.Fatal("closed must fail Do")
	}
}

// TestAuthEdges verifies PIN transport, body, poll, and cancel errors.
func TestAuthEdges(t *testing.T) {
	ctx := context.Background()
	bad := New("http://bad-\x7f-host", "c", "t")
	if _, err := bad.RequestPIN(ctx); err == nil {
		t.Fatal("pin build must fail")
	}
	if _, err := bad.PollPINToken(ctx, "PIN", 0); err == nil {
		t.Fatal("poll build must fail")
	}
	shut := New(closedURL(t), "c", "t")
	if _, err := shut.RequestPIN(ctx); err == nil {
		t.Fatal("pin Do must fail")
	}
	if _, err := shut.PollPINToken(ctx, "PIN", 0); err == nil {
		t.Fatal("poll Do must fail")
	}
	// Malformed + empty user_code.
	for _, body := range []string{`not json`, `{}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, body)
		}))
		_, err := New(srv.URL, "c", "t").RequestPIN(ctx)
		srv.Close()
		if err == nil {
			t.Fatalf("body %q must fail", body)
		}
	}
	// PIN poll hard error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	_, err := New(srv.URL, "c", "t").PollPINToken(ctx, "PIN", 0)
	srv.Close()
	if err == nil {
		t.Fatal("poll 500 must fail")
	}
	// Cancelled context stops the poll loop immediately.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := New("http://x", "c", "t").PollPINToken(cancelled, "PIN", 0); err == nil {
		t.Fatal("cancelled ctx must fail")
	}
	// Pending then success honors a nonzero interval.
	var n int
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			w.WriteHeader(404)
			return
		}
		fmt.Fprint(w, `{"access_token":"tok"}`)
	}))
	defer srv2.Close()
	tok, err := New(srv2.URL, "c", "t").PollPINToken(ctx, "PIN", time.Millisecond)
	if err != nil || tok != "tok" || n != 2 {
		t.Fatalf("tok=%q err=%v n=%d", tok, err, n)
	}
	// Cancel during the sleep aborts.
	srv3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer srv3.Close()
	ctx3, cancel3 := context.WithCancel(ctx)
	time.AfterFunc(20*time.Millisecond, cancel3)
	if _, err := New(srv3.URL, "c", "t").PollPINToken(ctx3, "PIN", 5*time.Second); err == nil {
		t.Fatal("cancel during sleep must fail")
	}
}

// TestFetchExcludesUnwatchedStatuses verifies plantowatch/dropped/never-watched
// entries (null last_watched_at, unwatched episodes) emit nothing.
func TestFetchExcludesUnwatchedStatuses(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/watching") {
			if strings.Contains(r.URL.Path, "/movies/") {
				fmt.Fprint(w, `{"movies":[]}`)
			} else {
				fmt.Fprint(w, `{"shows":[]}`)
			}
			return
		}
		if strings.Contains(r.URL.Path, "/movies/") {
			fmt.Fprint(w, `{"movies":[
				{"status":"plantowatch","last_watched_at":null,"movie":{"title":"P","year":2020,"ids":{"simkl":1}}},
				{"status":"dropped","last_watched_at":null,"movie":{"title":"D","year":2021,"ids":{"simkl":2}}},
				{"status":"completed","last_watched_at":null,"movie":{"title":"N","year":2022,"ids":{"simkl":3}}},
				{"status":"completed","last_watched_at":"2026-05-15T22:30:00Z","movie":{"title":"W","year":2023,"ids":{"simkl":4}}}
			]}`)
			return
		}
		fmt.Fprint(w, `{"shows":[
			{"status":"plantowatch","last_watched_at":null,"show":{"title":"P","ids":{"simkl":11}},"seasons":[{"number":1,"episodes":[{"number":1,"watched_at":""}]}]},
			{"status":"dropped","last_watched_at":null,"show":{"title":"D","ids":{"simkl":12}},"seasons":[{"number":1,"episodes":[{"number":1}]}]},
			{"status":"completed","last_watched_at":"2026-05-15T22:30:00Z","show":{"title":"W","ids":{"simkl":13}},"seasons":[{"number":1,"episodes":[{"number":1,"watched_at":"2026-05-15T22:30:00Z"}]}]}
		]}`)
	}))
	defer srv.Close()
	got, err := New(srv.URL, "c", "t").fetchCategory(ctx, "movies", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "W" {
		t.Fatalf("movies got=%+v want only watched", got)
	}
	got, err = New(srv.URL, "c", "t").fetchCategory(ctx, "shows", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].IDs.Simkl != 13 {
		t.Fatalf("shows got=%+v want only watched episode", got)
	}
}

// TestFetchIncludesWatchingBucket verifies watching-bucket episodes with real
// watched_at dates are emitted, not dropped as unwatched.
func TestFetchIncludesWatchingBucket(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/watching") {
			fmt.Fprint(w, `{"shows":[{"status":"watching","last_watched_at":"2026-05-13T19:00:00Z","show":{"title":"W","year":2010,"ids":{"simkl":21}},"seasons":[{"number":1,"episodes":[{"number":3,"watched_at":"2026-05-13T19:00:00Z"}]}]}]}`)
			return
		}
		fmt.Fprint(w, `{"shows":[]}`)
	}))
	defer srv.Close()
	got, err := New(srv.URL, "c", "t").fetchCategory(ctx, "shows", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Episode != 3 || got[0].WatchedAt.IsZero() {
		t.Fatalf("got=%+v want watching-bucket episode", got)
	}
}

// TestFetchDedupesAcrossBuckets verifies the same episode in completed+watching
// buckets emits once.
func TestFetchDedupesAcrossBuckets(t *testing.T) {
	ctx := context.Background()
	ep := `{"status":"completed","last_watched_at":"2026-05-13T19:00:00Z","show":{"title":"S","year":2010,"ids":{"simkl":1411674}},"seasons":[{"number":1,"episodes":[{"number":1,"watched_at":"2026-05-13T19:00:00Z"}]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"shows":[%s]}`, ep)
	}))
	defer srv.Close()
	got, err := New(srv.URL, "c", "t").fetchCategory(ctx, "shows", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got=%d want 1 (same episode in both buckets emits once)", len(got))
	}
	if got[0].Season != 1 || got[0].Episode != 1 || got[0].IDs.Simkl != 1411674 || got[0].WatchedAt.IsZero() {
		t.Fatalf("got=%+v", got[0])
	}
	// Movies emit straight from the single completed bucket (no cross-bucket
	// duplicates possible); shows dedupe the same episode in both buckets.
	mov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"movies":[{"status":"completed","last_watched_at":"2026-05-15T22:30:00Z","movie":{"title":"M","year":2007,"ids":{"simkl":1015859}}}]}`)
	}))
	defer mov.Close()
	mgot, err := New(mov.URL, "c", "t").fetchCategory(ctx, "movies", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(mgot) != 1 {
		t.Fatalf("got=%d want 1 (same movie in both buckets emits once)", len(mgot))
	}
}

// TestHistorySkipsUnchangedTVShows verifies an unchanged tv_shows cursor skips
// all-items fetches for shows while dirty movies still fetch.
func TestHistorySkipsUnchangedTVShows(t *testing.T) {
	var showsHits, moviesHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/sync/activities":
			fmt.Fprint(w, `{"all":"2026-05-15T22:30:00Z","movies":{"all":"2026-05-15T22:30:00Z"},"tv_shows":{"all":"2026-05-01T00:00:00Z"},"anime":{"all":"2026-05-01T00:00:00Z"}}`)
		case strings.Contains(r.URL.Path, "/movies/"):
			moviesHits++
			fmt.Fprint(w, `{"movies":[]}`)
		case strings.Contains(r.URL.Path, "/shows/"):
			showsHits++
			fmt.Fprint(w, `{"shows":[]}`)
		case strings.Contains(r.URL.Path, "/anime/"):
			t.Error("anime unchanged, must not fetch")
			fmt.Fprint(w, `{"anime":[]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	since := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	if _, err := New(srv.URL, "c", "t").History(context.Background(), since); err != nil {
		t.Fatal(err)
	}
	if showsHits != 0 {
		t.Fatalf("showsHits=%d want 0 (tv_shows unchanged)", showsHits)
	}
	if moviesHits != 1 {
		t.Fatalf("moviesHits=%d want 1 (completed bucket only)", moviesHits)
	}
}

// TestFetchExcludesZeroTimestamps verifies empty/missing timestamps emit nothing.
func TestFetchExcludesZeroTimestamps(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/watching") {
			if strings.Contains(r.URL.Path, "/movies/") {
				fmt.Fprint(w, `{"movies":[]}`)
			} else {
				fmt.Fprint(w, `{"shows":[]}`)
			}
			return
		}
		if strings.Contains(r.URL.Path, "/movies/") {
			fmt.Fprint(w, `{"movies":[
				{"status":"completed","last_watched_at":"","movie":{"title":"E","ids":{"simkl":31}}},
				{"status":"completed","last_watched_at":null,"movie":{"title":"N","ids":{"simkl":32}}}
			]}`)
			return
		}
		fmt.Fprint(w, `{"shows":[{"status":"watching","show":{"title":"Z","ids":{"simkl":33}},"seasons":[{"number":1,"episodes":[{"number":1,"watched_at":""},{"number":2}]}]}]}`)
	}))
	defer srv.Close()
	got, err := New(srv.URL, "c", "t").fetchCategory(ctx, "movies", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("movies got=%v want empty", got)
	}
	got, err = New(srv.URL, "c", "t").fetchCategory(ctx, "shows", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("shows got=%v want empty", got)
	}
}

// TestRequestPINEmptyBaseFallback verifies empty BaseURL falls back to DefaultBaseURL.
func TestRequestPINEmptyBaseFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Client{ClientID: "cid"}
	if _, err := c.RequestPIN(ctx); err == nil {
		t.Fatal("empty base with cancelled ctx must fail after fallback")
	}
}

// TestPollPINTokenEmptyBaseFallback verifies empty BaseURL falls back to DefaultBaseURL.
func TestPollPINTokenEmptyBaseFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Client{ClientID: "cid"}
	if _, err := c.PollPINToken(ctx, "PIN", 0); err == nil {
		t.Fatal("empty base with cancelled ctx must fail after fallback")
	}
}

// TestUnresolvedIDMatches verifies each ID kind matches its not_found entry.
func TestUnresolvedIDMatches(t *testing.T) {
	for _, tt := range []struct {
		name string
		ids  model.IDs
		nf   notFoundEntry
		want bool
	}{
		{"simkl", model.IDs{Simkl: 7}, notFoundEntry{IDs: simklIDs{Simkl: 7}}, true},
		{"imdb", model.IDs{IMDB: "tt123"}, notFoundEntry{IDs: simklIDs{IMDB: "tt123"}}, true},
		{"tmdb", model.IDs{TMDB: 603}, notFoundEntry{IDs: simklIDs{TMDB: 603}}, true},
		{"tvdb", model.IDs{TVDB: 888}, notFoundEntry{IDs: simklIDs{TVDB: 888}}, true},
		{"mismatch", model.IDs{Simkl: 1}, notFoundEntry{IDs: simklIDs{Simkl: 2}}, false},
		{"empty", model.IDs{Simkl: 1}, notFoundEntry{}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := unresolved(tt.ids, []notFoundEntry{tt.nf}); got != tt.want {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
	if unresolved(model.IDs{Simkl: 1}, nil) {
		t.Fatal("nil list must not match")
	}
}

// TestFetchCategoryShowMovieFallback verifies empty show title falls back to movie object.
func TestFetchCategoryShowMovieFallback(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/watching") {
			fmt.Fprint(w, `{"shows":[]}`)
			return
		}
		fmt.Fprint(w, `{"shows":[{"status":"completed","last_watched_at":"2026-05-13T19:00:00Z","show":{"title":"","ids":{"simkl":0}},"movie":{"title":"Fallback","year":2020,"ids":{"simkl":99}},"seasons":[{"number":1,"episodes":[{"number":2,"watched_at":"2026-05-13T19:00:00Z"}]}]}]}`)
	}))
	defer srv.Close()
	got, err := New(srv.URL, "c", "t").fetchCategory(ctx, "shows", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "Fallback" || got[0].IDs.Simkl != 99 {
		t.Fatalf("got=%+v want Fallback movie fallback", got)
	}
}

// TestFlexIntIDs verifies quoted, numeric, null, and garbage external ids
// decode (responses quote tmdb/tvdb; only simkl stays numeric), and that a
// full all-items body with string ids maps intact.
func TestFlexIntIDs(t *testing.T) {
	var ids simklIDs
	if err := json.Unmarshal([]byte(`{"simkl":5,"tmdb":"693134","tvdb":"290272","imdb":"tt1201607"}`), &ids); err != nil {
		t.Fatal(err)
	}
	if ids.TMDB != 693134 || ids.TVDB != 290272 {
		t.Fatalf("ids=%+v want quoted strings decoded", ids)
	}
	for _, in := range []string{`{"tmdb":""}`, `{"tmdb":null}`, `{"tmdb":"abc"}`, `{"other":1}`} {
		var zero simklIDs
		if err := json.Unmarshal([]byte(in), &zero); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if zero.TMDB != 0 {
			t.Fatalf("%s: tmdb=%d want 0", in, zero.TMDB)
		}
	}
	var num simklIDs
	if err := json.Unmarshal([]byte(`{"tmdb":603,"tvdb":275274}`), &num); err != nil {
		t.Fatal(err)
	}
	if num.TMDB != 603 || num.TVDB != 275274 {
		t.Fatalf("num=%+v", num)
	}
	// End to end: a real all-items body quotes tmdb/tvdb.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"movies":[{"status":"completed","last_watched_at":"2026-05-15T22:30:00Z","movie":{"title":"M","ids":{"simkl":1015859,"tmdb":"27205","tvdb":"3436"}}}]}`)
	}))
	defer srv.Close()
	got, err := New(srv.URL, "c", "t").fetchCategory(context.Background(), "movies", time.Time{})
	if err != nil || len(got) != 1 || got[0].IDs.TMDB != 27205 || got[0].IDs.TVDB != 3436 {
		t.Fatalf("got=%v err=%v want string ids decoded", got, err)
	}
}

// TestPushEncodesIntIDs verifies request ids stay plain integers on push.
func TestPushEncodesIntIDs(t *testing.T) {
	var got struct {
		Movies []map[string]any `json:"movies"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	items := []model.WatchItem{{IDs: model.IDs{Simkl: 1015859, TMDB: 27205}, MediaType: "movie", WatchedAt: time.Now()}}
	if _, err := New(srv.URL, "c", "t").Push(context.Background(), items); err != nil {
		t.Fatal(err)
	}
	mids, ok := got.Movies[0]["ids"].(map[string]any)
	if !ok || mids["tmdb"].(float64) != 27205 || mids["simkl"].(float64) != 1015859 {
		t.Fatalf("ids=%v want numeric tmdb", mids)
	}
}

// TestActivitiesBadCursorType verifies a non-string non-object cursor fails as malformed.
func TestActivitiesBadCursorType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"movies":123,"tv_shows":{"all":"2026-05-01T00:00:00Z"},"anime":{"all":"2026-05-01T00:00:00Z"}}`)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "c", "t").History(context.Background(), time.Time{}); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("err=%v want malformed", err)
	}
}
