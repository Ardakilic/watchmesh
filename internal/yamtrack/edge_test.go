package yamtrack

import (
	"context"
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

// TestMediaKindUnknown verifies unknown types skip and episodes map to shows.
func TestMediaKindUnknown(t *testing.T) {
	if _, ok := mediaKind(model.WatchItem{MediaType: "podcast"}); ok {
		t.Fatal("unknown type must skip")
	}
	if k, ok := mediaKind(model.WatchItem{MediaType: "episode"}); !ok || k != "show" {
		t.Fatalf("episode -> %q,%v", k, ok)
	}
}

// TestEndDateZero verifies zero WatchedAt becomes today's date.
func TestEndDateZero(t *testing.T) {
	if got := endDate(model.WatchItem{}); got != time.Now().UTC().Format("2006-01-02") {
		t.Fatalf("got %q", got)
	}
}

// TestResolveNextOrigin verifies continuation URLs never leave the
// configured origin: absolute same-origin URLs and paths are followed,
// cross-origin/scheme/port URLs and opaque tokens fall back to offset paging.
func TestResolveNextOrigin(t *testing.T) {
	base := "https://yam.example:8443/base"
	for _, tt := range []struct {
		next string
		want string
	}{
		{"https://yam.example:8443/base/api/?page=2", "https://yam.example:8443/base/api/?page=2"},
		{"HTTPS://YAM.EXAMPLE:8443/other", "HTTPS://YAM.EXAMPLE:8443/other"},
		{"/api/?page=2", "https://yam.example:8443/base/api/?page=2"},
		{"https://evil.example/api/", ""},
		{"http://yam.example:8443/api/", ""},
		{"https://yam.example:9999/api/", ""},
		{"https://yam.example/api/", ""},
		{"x", ""},
		{"", ""},
		{"://bad", ""},
	} {
		if got := resolveNext(base, tt.next); got != tt.want {
			t.Fatalf("next %q: got %q want %q", tt.next, got, tt.want)
		}
	}
	// Default ports compare equal to explicit ones.
	if got := resolveNext("http://h", "http://h:80/x"); got == "" {
		t.Fatal("explicit :80 must match default http port")
	}
}

// TestPushSkipsUnknownKind verifies unmapped media types send no requests.
func TestPushSkipsUnknownKind(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	it := model.WatchItem{IDs: model.IDs{TMDB: 5}, MediaType: "podcast", WatchedAt: time.Now()}
	if delivered, err := New(srv.URL, "t").Push(context.Background(), []model.WatchItem{it}); err != nil {
		t.Fatal(err)
	} else if len(delivered) != 0 {
		t.Fatalf("delivered=%d want 0", len(delivered))
	}
	if n != 0 {
		t.Fatalf("requests=%d want 0", n)
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

// TestFromItemEdges verifies zero IDs fail and odd shapes normalize.
func TestFromItemEdges(t *testing.T) {
	if _, ok := fromItem(yamItem{}); ok {
		t.Fatal("zero MediaID must fail")
	}
	w, ok := fromItem(yamItem{Source: "tmdb", MediaType: "episode", MediaID: 7, Title: "E", EndDate: "bogus"})
	if !ok || w.MediaType != "show" || w.IDs.TMDB != 7 || !w.WatchedAt.IsZero() {
		t.Fatalf("got %+v,%v", w, ok)
	}
}

// TestHistoryTransportErrors verifies request build/Do failures surface.
func TestHistoryTransportErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := New("http://bad-\x7f-host", "t").History(ctx, time.Time{}); err == nil {
		t.Fatal("bad URL must fail")
	}
	if _, err := New(closedURL(t), "t").History(ctx, time.Time{}); err == nil {
		t.Fatal("closed server must fail")
	}
}

// TestHistorySkipsZeroIDAndOld verifies zero IDs skip and since filters old.
func TestHistorySkipsZeroIDAndOld(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/media/movie/") {
			fmt.Fprint(w, `{"results":[
				{"source":"tmdb","media_type":"movie","media_id":0,"title":"Zero","end_date":"2026-05-10"},
				{"source":"tmdb","media_type":"movie","media_id":1,"title":"Old","end_date":"2020-01-01"},
				{"source":"tmdb","media_type":"movie","media_id":2,"title":"New","end_date":"2026-05-11"}
			],"next":null}`)
			return
		}
		fmt.Fprint(w, `{"results":[],"next":null}`)
	}))
	defer srv.Close()
	since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	got, err := New(srv.URL, "t").History(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].IDs.TMDB != 2 {
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
