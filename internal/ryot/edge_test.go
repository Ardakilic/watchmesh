package ryot

import (
	"context"
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

// TestDatedInputFields verifies createNewCompleted bodies: episodes carry
// season/episode pointers, movies stay bare.
func TestDatedInputFields(t *testing.T) {
	ep := finishedDated(epItem(155, 2, 3))
	if ep.ShowSeasonNumber == nil || *ep.ShowSeasonNumber != 2 ||
		ep.ShowEpisodeNumber == nil || *ep.ShowEpisodeNumber != 3 {
		t.Fatalf("episode dated=%+v", ep)
	}
	m := finishedDated(item(603))
	if m.ShowSeasonNumber != nil || m.ShowEpisodeNumber != nil {
		t.Fatalf("movie dated=%+v", m)
	}
}

// TestPushLotKinds verifies the lot mapping and the skip shapes.
func TestPushLotKinds(t *testing.T) {
	for _, tt := range []struct {
		name string
		item model.WatchItem
		lot  string
		push bool
	}{
		{"movie", item(603), "MOVIE", true},
		{"movie-no-tmdb", model.WatchItem{IDs: model.IDs{IMDB: "tt0"}, MediaType: "movie"}, "MOVIE", false},
		{"episode", epItem(155, 2, 3), "SHOW", true},
		{"episode-s0e0", model.WatchItem{IDs: model.IDs{TMDB: 155}, MediaType: "episode"}, "SHOW", false},
		{"episode-no-tmdb", model.WatchItem{MediaType: "episode", Season: 1, Episode: 2}, "SHOW", false},
		{"bare-show", model.WatchItem{IDs: model.IDs{TMDB: 155}, MediaType: "show"}, "", false},
		{"podcast", model.WatchItem{IDs: model.IDs{TMDB: 155}, MediaType: "podcast"}, "", false},
	} {
		lot, ok := pushLot(tt.item)
		if lot != tt.lot || ok != tt.push {
			t.Fatalf("%s: pushLot = (%q, %v), want (%q, %v)", tt.name, lot, ok, tt.lot, tt.push)
		}
	}
}

// TestPushSkipsShapes verifies elements without a resolution target send no
// requests at all.
func TestPushSkipsShapes(t *testing.T) {
	srv, tp := pushRouter(nil, nil, deployOK())
	defer srv.Close()
	items := []model.WatchItem{
		{IDs: model.IDs{TMDB: 155}, MediaType: "show", WatchedAt: time.Now()},
		{IDs: model.IDs{IMDB: "tt0"}, MediaType: "movie", WatchedAt: time.Now()},
		{IDs: model.IDs{TMDB: 155}, MediaType: "episode", WatchedAt: time.Now()},
		{IDs: model.IDs{TMDB: 155}, MediaType: "podcast", WatchedAt: time.Now()},
	}
	delivered, err := New(srv.URL, "t").Push(context.Background(), items)
	if err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 0 || tp.search+tp.details+tp.deploy != 0 {
		t.Fatalf("delivered=%v hits=%+v, want silence", delivered, *tp)
	}
}

// TestPushUnresolved verifies search candidates that never strictly match
// (and erroring reads) skip silently: no deploy, no error, nothing marked.
func TestPushUnresolved(t *testing.T) {
	srv, tp := pushRouter(
		map[string]string{
			"met_1": detailsResp("603", "MOVIE", "IMDB"), // wrong source
			"met_2": detailsResp("604", "MOVIE", "TMDB"), // wrong id
		},
		[]string{"met_1", "met_2"},
		deployOK(),
	)
	defer srv.Close()
	delivered, err := New(srv.URL, "t").Push(context.Background(), []model.WatchItem{item(603)})
	if err != nil || len(delivered) != 0 {
		t.Fatalf("delivered=%v err=%v", delivered, err)
	}
	if tp.deploy != 0 || tp.details != 2 {
		t.Fatalf("deploy=%d details=%d", tp.deploy, tp.details)
	}
}

// TestPushResolveFailures verifies resolution failures (HTTP errors,
// malformed body, GraphQL errors, broken detail reads) swallow into a
// silent whole-bucket skip.
func TestPushResolveFailures(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
		breaks string // query substring to wreck; "" wrecks every call
	}{
		{"search-500", 500, `{}`, ""},
		{"search-malformed", 200, `not json`, ""},
		{"search-graphql-error", 200, `{"errors":[{"message":"boom"}]}`, ""},
		{"details-500", 500, `{}`, "metadataDetails"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := gqlBody(r).Query
				w.Header().Set("Content-Type", "application/json")
				if tt.breaks == "" || strings.Contains(q, tt.breaks) {
					w.WriteHeader(tt.status)
					fmt.Fprint(w, tt.body)
					return
				}
				fmt.Fprint(w, gresp("metadataSearch", searchResp("met_1")))
			}))
			defer srv.Close()
			delivered, err := New(srv.URL, "t").Push(context.Background(), []model.WatchItem{item(603)})
			if err != nil || len(delivered) != 0 {
				t.Fatalf("delivered=%v err=%v, resolution failures must skip silently", delivered, err)
			}
		})
	}
}

// TestPushTransportErrors verifies resolution transport failures leave Push
// with nothing delivered — they are swallowed bucket-side, never surfaced.
func TestPushTransportErrors(t *testing.T) {
	ctx := context.Background()
	it := []model.WatchItem{item(603)}
	for _, base := range []string{"http://bad-\x7f-host", closedURL(t)} {
		delivered, err := New(base, "t").Push(ctx, it)
		if err != nil || len(delivered) != 0 {
			t.Fatalf("base %q: delivered=%v err=%v, want empty,nil", base, delivered, err)
		}
	}
}

// TestPushCancelSurfaces verifies a cancelled context aborts Push with the
// cancellation error instead of decoding as a clean empty push.
func TestPushCancelSurfaces(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv, tp := pushRouter(nil, nil, deployOK())
	defer srv.Close()
	delivered, err := New(srv.URL, "t").Push(ctx, []model.WatchItem{item(1)})
	if err == nil || !errors.Is(err, context.Canceled) || len(delivered) != 0 {
		t.Fatalf("delivered=%v err=%v, want context.Canceled", delivered, err)
	}
	if tp.deploy != 0 {
		t.Fatalf("deploys=%d, cancelled runs must not deploy", tp.deploy)
	}
}

// retryScript answers each request (1-based): a non-empty value interrupts
// with 429 and that Retry-After.
type retryScript func(n int) string

// retryRouter spins a fake where request n may be hijacked by the script;
// everything else routes normally (search lists met_1, matched details,
// deploy accepted).
func retryRouter(script retryScript) (*httptest.Server, *int) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		q := gqlBody(r).Query
		w.Header().Set("Content-Type", "application/json")
		if ra := script(hits); ra != "" {
			w.Header().Set("Retry-After", ra)
			w.WriteHeader(http.StatusTooManyRequests)
			fmt.Fprint(w, `{}`)
			return
		}
		switch {
		case strings.Contains(q, "metadataSearch"):
			fmt.Fprint(w, gresp("metadataSearch", searchResp("met_1")))
		case strings.Contains(q, "metadataDetails"):
			fmt.Fprint(w, gresp("metadataDetails", detailsResp("1", "MOVIE", "TMDB")))
		case strings.Contains(q, "deployBulkMetadataProgressUpdate"):
			fmt.Fprint(w, gresp("deployBulkMetadataProgressUpdate", "true"))
		default:
			w.WriteHeader(500)
		}
	}))
	return srv, &hits
}

// TestPushRetry429 verifies a rate-limited search retries into success,
// while a deploy that stays rate-limited errors with nothing marked.
func TestPushRetry429(t *testing.T) {
	ctx := context.Background()
	items := []model.WatchItem{item(1)}
	srv, hits := retryRouter(func(n int) string {
		if n == 1 {
			return "0"
		}
		return ""
	})
	defer srv.Close()
	if delivered, err := New(srv.URL, "t").Push(ctx, items); err != nil || len(delivered) != 1 {
		t.Fatalf("search 429→200: delivered=%d err=%v", len(delivered), err)
	}
	if *hits != 4 { // search, search retry, details, deploy
		t.Fatalf("hits=%d want 4", *hits)
	}
	srv2, _ := retryRouter(func(n int) string {
		if n == 3 || n == 4 {
			return "0"
		}
		return ""
	})
	defer srv2.Close()
	if delivered, err := New(srv2.URL, "t").Push(ctx, items); err == nil || len(delivered) != 0 {
		t.Fatalf("deploy 429→429: delivered=%d err=%v, want error and nothing marked", len(delivered), err)
	}
}

// TestPushRetryHTTPDate verifies an HTTP-date Retry-After on the deploy call
// is honored (future waits, past retries immediately).
func TestPushRetryHTTPDate(t *testing.T) {
	ctx := context.Background()
	serve := func(date string) (*httptest.Server, *int) {
		srv, hits := retryRouter(func(n int) string {
			if n == 3 {
				return date
			}
			return ""
		})
		return srv, hits
	}
	srv, hits := serve(time.Now().Add(500 * time.Millisecond).UTC().Format(http.TimeFormat))
	defer srv.Close()
	start := time.Now()
	if delivered, err := New(srv.URL, "t").Push(ctx, []model.WatchItem{item(1)}); err != nil || len(delivered) != 1 {
		t.Fatalf("future date: delivered=%d err=%v", len(delivered), err)
	}
	if *hits != 4 {
		t.Fatalf("hits=%d want 4", *hits)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("future date: took %v, want prompt retry", d)
	}
	srv2, hits2 := serve(time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat))
	defer srv2.Close()
	start = time.Now()
	if delivered, err := New(srv2.URL, "t").Push(ctx, []model.WatchItem{item(1)}); err != nil || len(delivered) != 1 {
		t.Fatalf("past date: delivered=%d err=%v", len(delivered), err)
	}
	if *hits2 != 4 {
		t.Fatalf("hits=%d want 4", *hits2)
	}
	if d := time.Since(start); d > 900*time.Millisecond {
		t.Fatalf("past date: took %v, want immediate retry", d)
	}
}

// TestPushRetryCancel verifies cancelling during the deploy's Retry-After
// wait aborts Push with the context error and nothing delivered.
func TestPushRetryCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	srv, _ := retryRouter(func(n int) string {
		if n == 3 {
			return "30"
		}
		return ""
	})
	defer srv.Close()
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	delivered, err := New(srv.URL, "t").Push(ctx, []model.WatchItem{item(1)})
	if err != context.Canceled || len(delivered) != 0 {
		t.Fatalf("cancel: delivered=%d err=%v, want ctx canceled and nothing", len(delivered), err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("cancel: took %v, want prompt abort", d)
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

// TestResolveMalformedResponses verifies malformed search/details bodies
// error instead of resolving garbage.
func TestResolveMalformedResponses(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `not json`)
	}))
	if _, err := New(srv.URL, "t").resolve(ctx, "MOVIE", 603, "T"); err == nil {
		t.Fatal("malformed search must error")
	}
	srv.Close()
	// A details read that errors, malformed, or mismatches never resolves.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(gqlBody(r).Query, "metadataSearch") {
			fmt.Fprint(w, gresp("metadataSearch", searchResp("met_1")))
			return
		}
		fmt.Fprint(w, `not json`) // malformed details body
	}))
	defer srv2.Close()
	if _, err := New(srv2.URL, "t").resolve(ctx, "MOVIE", 603, "T"); err == nil {
		t.Fatal("malformed details must error")
	}
}

// TestReadDetailsChunks verifies aliased reads split at detailChunk and
// tolerate partial per-alias payloads.
func TestReadDetailsChunks(t *testing.T) {
	var reads []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := gqlBody(r)
		if !strings.Contains(req.Query, "userMetadataDetails") {
			fmt.Fprint(w, gresp("userMetadataList", listResp(nil, nil)))
			return
		}
		reads = append(reads, len(req.Variables))
		// Empty data: every alias reads as null, ids skip silently.
		fmt.Fprint(w, `{"data":{}}`)
	}))
	defer srv.Close()
	if got, err := New(srv.URL, "t").readDetails(context.Background(), nil); err != nil || len(got) != 0 {
		t.Fatalf("nil ids: got=%v err=%v, want empty,nil", got, err)
	}
	ids := make([]string, 0, detailChunk+1)
	for i := 0; i < detailChunk+1; i++ {
		ids = append(ids, fmt.Sprintf("met_%d", i))
	}
	if _, err := New(srv.URL, "t").readDetails(context.Background(), ids); err != nil {
		t.Fatal(err)
	}
	if len(reads) != 2 || reads[0] != detailChunk || reads[1] != 1 {
		t.Fatalf("chunk vars=%v, want [%d, 1]", reads, detailChunk)
	}
}

// TestReadDetailsAliasMiss verifies unreadable alias payloads skip the id
// instead of failing the batch.
func TestReadDetailsAliasMiss(t *testing.T) {
	// met_1 readable, met_2/3 missing from both maps (alias nulls).
	srv, _ := historyRouter(
		map[string][]listPage{"MOVIE": {{ids: []string{"met_1", "met_2"}}}},
		map[string]string{"met_1": detailsResp("603", "MOVIE", "TMDB")},
		map[string]string{"met_1": seenResp(seenRow("2026-05-10T20:00:00Z", -1, -1))},
	)
	defer srv.Close()
	got, err := New(srv.URL, "t").readDetails(context.Background(), []string{"met_1", "met_2"})
	if err != nil || len(got) != 1 || got[0].IDs.TMDB != 603 {
		t.Fatalf("got=%v err=%v, alias misses must skip", got, err)
	}
}

// TestHistoryPageCap verifies a never-ending list errors the internal walk —
// loud truncation — and public History swallows that error.
func TestHistoryPageCap(t *testing.T) {
	mov := make([]listPage, historyPageCap+2)
	for i := range mov {
		mov[i] = listPage{ids: []string{"met_1"}, next: nextP(2)}
	}
	srv, _ := historyRouter(
		map[string][]listPage{"MOVIE": mov},
		map[string]string{"met_1": detailsResp("603", "MOVIE", "TMDB")},
		map[string]string{"met_1": seenResp(seenRow("2026-05-10T20:00:00Z", -1, -1))},
	)
	defer srv.Close()
	if _, err := New(srv.URL, "t").history(context.Background(), time.Time{}); err == nil ||
		!strings.Contains(err.Error(), "exceeds 100 pages") {
		t.Fatalf("err=%v, page cap must error", err)
	}
	got, err := New(srv.URL, "t").History(context.Background(), time.Time{})
	if err != nil || len(got) != 0 {
		t.Fatalf("public history=%v err=%v, swallow must keep it empty,nil", got, err)
	}
}

// TestHistoryMalformedList verifies a malformed list page fails the walk,
// which public History swallows.
func TestHistoryMalformedList(t *testing.T) {
	for _, body := range []string{`not json`, `{"data":{}}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, body)
		}))
		_, _, err := New(srv.URL, "t").userListPage(context.Background(), "MOVIE", 1)
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("body %q: err=%v, want malformed response error", body, err)
		}
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

// TestHistoryErrorReturnsEmpty verifies HTTP/GraphQL failures at the list
// stage yield empty,nil, never errors.
func TestHistoryErrorReturnsEmpty(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{"server-error", 500, `{}`},
		{"malformed", 200, `not json`},
		{"graphql-errors", 200, `{"errors":[{"message":"x"}]}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()
			got, err := New(srv.URL, "t").History(context.Background(), time.Time{})
			if err != nil {
				t.Fatalf("history must not fail: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("got=%v want empty", got)
			}
		})
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
