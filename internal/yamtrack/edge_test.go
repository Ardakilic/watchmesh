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

func closedURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	return url
}

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

func TestMediaKindUnknown(t *testing.T) {
	if _, ok := mediaKind(model.WatchItem{MediaType: "podcast"}); ok {
		t.Fatal("unknown type must skip")
	}
	if k, ok := mediaKind(model.WatchItem{MediaType: "episode"}); !ok || k != "show" {
		t.Fatalf("episode -> %q,%v", k, ok)
	}
}

func TestEndDateZero(t *testing.T) {
	if got := endDate(model.WatchItem{}); got != time.Now().UTC().Format("2006-01-02") {
		t.Fatalf("got %q", got)
	}
}

func TestPushSkipsUnknownKind(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	it := model.WatchItem{IDs: model.IDs{TMDB: 5}, MediaType: "podcast", WatchedAt: time.Now()}
	if err := New(srv.URL, "t").Push(context.Background(), []model.WatchItem{it}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("requests=%d want 0", n)
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

func TestFromItemEdges(t *testing.T) {
	if _, ok := fromItem(yamItem{}); ok {
		t.Fatal("zero MediaID must fail")
	}
	w, ok := fromItem(yamItem{Source: "tmdb", MediaType: "episode", MediaID: 7, Title: "E", EndDate: "bogus"})
	if !ok || w.MediaType != "show" || w.IDs.TMDB != 7 || !w.WatchedAt.IsZero() {
		t.Fatalf("got %+v,%v", w, ok)
	}
}

func TestHistoryTransportErrors(t *testing.T) {
	ctx := context.Background()
	if _, err := New("http://bad-\x7f-host", "t").History(ctx, time.Time{}); err == nil {
		t.Fatal("bad URL must fail")
	}
	if _, err := New(closedURL(t), "t").History(ctx, time.Time{}); err == nil {
		t.Fatal("closed server must fail")
	}
}

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

func TestValidateTokenTransportErrors(t *testing.T) {
	ctx := context.Background()
	if err := New("http://bad-\x7f-host", "t").ValidateToken(ctx); err == nil {
		t.Fatal("bad URL must fail")
	}
	if err := New(closedURL(t), "t").ValidateToken(ctx); err == nil {
		t.Fatal("closed server must fail")
	}
}
