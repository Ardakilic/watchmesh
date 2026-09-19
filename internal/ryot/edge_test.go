package ryot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/watchmesh/watchmesh/internal/model"
)

func closedURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

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

func TestMetadataIDKinds(t *testing.T) {
	for _, tt := range []struct{ typ, want string }{
		{"movie", "tmdb://movie/603"},
		{"show", "tmdb://show/603"},
		{"episode", "tmdb://show/603"},
	} {
		id, ok := metadataID(model.WatchItem{IDs: model.IDs{TMDB: 603}, MediaType: tt.typ})
		if !ok || id != tt.want {
			t.Fatalf("%s: got %q,%v", tt.typ, id, ok)
		}
	}
	if _, ok := metadataID(model.WatchItem{MediaType: "movie"}); ok {
		t.Fatal("TMDB==0 must skip")
	}
}

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

func TestPushTransportErrors(t *testing.T) {
	ctx := context.Background()
	it := []model.WatchItem{{IDs: model.IDs{TMDB: 1}, MediaType: "movie", WatchedAt: time.Now()}}
	if err := New("http://bad-\x7f-host", "t").Push(ctx, it); err == nil {
		t.Fatal("bad URL must fail build")
	}
	if err := New(closedURL(t), "t").Push(ctx, it); err == nil {
		t.Fatal("closed server must fail Do")
	}
}

func TestHistorySwallowsTransportErrors(t *testing.T) {
	ctx := context.Background()
	for _, base := range []string{"http://bad-\x7f-host", closedURL(t)} {
		got, err := New(base, "t").History(ctx, time.Time{})
		if err != nil || len(got) != 0 {
			t.Fatalf("base %q: got %v err %v, want empty,nil", base, got, err)
		}
	}
}

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

func TestValidateTokenTransportErrors(t *testing.T) {
	ctx := context.Background()
	if err := New("http://bad-\x7f-host", "t").ValidateToken(ctx); err == nil {
		t.Fatal("bad URL must fail")
	}
	if err := New(closedURL(t), "t").ValidateToken(ctx); err == nil {
		t.Fatal("closed server must fail")
	}
}
