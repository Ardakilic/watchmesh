package ryot

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
	if New("http://x", "tok").Name() != "ryot" {
		t.Fatal("Name must be ryot")
	}
	if New("http://x", "tok").httpClient() != http.DefaultClient {
		t.Fatal("nil HTTP must use DefaultClient")
	}
	custom := &http.Client{}
	if (&Client{HTTP: custom}).httpClient() != custom {
		t.Fatal("custom HTTP must win")
	}
}

// TestMetadataIDKinds verifies movie/show mapping and episode/TMDB==0 skips.
func TestMetadataIDKinds(t *testing.T) {
	for _, tt := range []struct{ typ, want string }{
		{"movie", "tmdb://movie/603"},
		{"show", "tmdb://show/603"},
	} {
		id, ok := metadataID(model.WatchItem{IDs: model.IDs{TMDB: 603}, MediaType: tt.typ})
		if !ok || id != tt.want {
			t.Fatalf("%s: got %q,%v", tt.typ, id, ok)
		}
	}
	if _, ok := metadataID(model.WatchItem{IDs: model.IDs{TMDB: 603}, MediaType: "episode"}); ok {
		t.Fatal("episodes must skip: no episode-capable payload yet")
	}
	if _, ok := metadataID(model.WatchItem{MediaType: "movie"}); ok {
		t.Fatal("TMDB==0 must skip")
	}
}

// TestFinishedOnZero verifies zero WatchedAt becomes ~now, set times pass through.
func TestFinishedOnZero(t *testing.T) {
	before := time.Now().UTC()
	s := finishedOn(model.WatchItem{})
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	if at.Before(before.Add(-time.Minute)) || at.After(time.Now().UTC().Add(time.Minute)) {
		t.Fatalf("finishedOn(zero) = %q, want ~now", s)
	}
	want := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)
	if got := finishedOn(model.WatchItem{WatchedAt: want}); got != want.Format(time.RFC3339) {
		t.Fatalf("got %q", got)
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

// TestHistorySwallowsTransportErrors verifies transport failures yield empty,nil.
func TestHistorySwallowsTransportErrors(t *testing.T) {
	ctx := context.Background()
	for _, base := range []string{"http://bad-\x7f-host", closedURL(t)} {
		got, err := New(base, "t").History(ctx, time.Time{})
		if err != nil || len(got) != 0 {
			t.Fatalf("base %q: got %v err %v, want empty,nil", base, got, err)
		}
	}
}

// TestHistorySkipsAndFilters verifies malformed IDs skip and since filters old.
func TestHistorySkipsAndFilters(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"userMediaList":[
			{"metadataId":"bogus","title":"X"},
			{"metadataId":"tmdb://movie","title":"Y"},
			{"metadataId":"tmdb://movie/abc","title":"Z"},
			{"metadataId":"tmdb://movie/0","title":"W"},
			{"metadataId":"tmdb://show/5","finishedOn":"2020-01-01T00:00:00Z","title":"Old"},
			{"metadataId":"tmdb://show/6","finishedOn":"2026-05-10T20:00:00Z","title":"New"}
		]}}`)
	}))
	defer srv.Close()
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got, err := New(srv.URL, "t").History(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].IDs.TMDB != 6 || got[0].MediaType != "show" {
		t.Fatalf("got=%+v", got)
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
		fmt.Fprint(w, `{"data":{}}`)
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
			fmt.Fprint(w, `{"data":{}}`)
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
