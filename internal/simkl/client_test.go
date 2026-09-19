package simkl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/watchmesh/watchmesh/internal/model"
)

func checkHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("simkl-api-key") != "cid" {
		t.Fatalf("simkl-api-key=%q", r.Header.Get("simkl-api-key"))
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("auth=%q", r.Header.Get("Authorization"))
	}
}

func TestHistorySkipsWhenUnchanged(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		checkHeaders(t, r)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"movies":{"watched_at":"2026-05-10T20:00:00Z"},"shows":{"watched_at":"2026-05-10T20:00:00Z"},"anime":{"watched_at":"2026-05-10T20:00:00Z"}}`)
	}))
	defer srv.Close()
	since := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	got, err := New(srv.URL, "cid", "tok").History(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got=%v want empty", got)
	}
	if n != 1 {
		t.Fatalf("requests=%d want 1 (activities only, never poll all-items)", n)
	}
}

func TestHistoryFetchesWhenNewer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkHeaders(t, r)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/sync/activities":
			fmt.Fprint(w, `{"movies":{"watched_at":"2026-05-15T22:30:00Z"},"shows":{"watched_at":"2026-05-13T19:00:00Z"},"anime":{"watched_at":"2026-05-01T00:00:00Z"}}`)
		case strings.HasSuffix(r.URL.Path, "/movies"):
			if r.URL.Query().Get("date_from") == "" {
				t.Error("missing date_from")
			}
			fmt.Fprint(w, `{"movies":[{"title":"M","year":2007,"ids":{"simkl":1015859},"watched_at":"2026-05-15T22:30:00Z"}]}`)
		case strings.HasSuffix(r.URL.Path, "/shows"):
			fmt.Fprint(w, `{"shows":[{"title":"S","year":2010,"ids":{"simkl":1411674},"seasons":[{"number":1,"episodes":[{"number":1,"watched_at":"2026-05-13T19:00:00Z"}]}]}]}`)
		case strings.HasSuffix(r.URL.Path, "/anime"):
			fmt.Fprint(w, `{"anime":[]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	since := time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC)
	got, err := New(srv.URL, "cid", "tok").History(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got=%v", got)
	}
	var movie, ep *model.WatchItem
	for i := range got {
		switch got[i].MediaType {
		case "movie":
			movie = &got[i]
		case "episode":
			ep = &got[i]
		}
	}
	if movie == nil || movie.IDs.Simkl != 1015859 || movie.WatchedAt.IsZero() {
		t.Fatalf("movie=%+v", movie)
	}
	if ep == nil || ep.Season != 1 || ep.Episode != 1 || ep.IDs.Simkl != 1411674 || ep.WatchedAt.IsZero() {
		t.Fatalf("episode=%+v", ep)
	}
}

func TestHistoryError(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, `{}`},
		{"server-error", 500, `{}`},
		{"malformed", 200, `not json`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()
			if _, err := New(srv.URL, "cid", "tok").History(context.Background(), time.Time{}); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestPush(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{"success", 200, `{"added":{"movies":1}}`, false},
		{"created", 201, `{}`, false},
		{"unauthorized", 401, `{}`, true},
		{"server-error", 500, `{}`, true},
		{"malformed", 200, `not json`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath string
			var gotBody struct {
				Movies []map[string]any `json:"movies"`
				Shows  []map[string]any `json:"shows"`
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				checkHeaders(t, r)
				gotPath = r.URL.Path
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()
			at := time.Date(2026, 5, 15, 22, 30, 0, 0, time.UTC)
			items := []model.WatchItem{
				{IDs: model.IDs{Simkl: 1015859}, MediaType: "movie", WatchedAt: at},
				{IDs: model.IDs{Simkl: 1411674}, MediaType: "episode", Season: 1, Episode: 1, WatchedAt: at},
			}
			if err := New(srv.URL, "cid", "tok").Push(context.Background(), items); (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if gotPath != "/sync/history" {
				t.Fatalf("path=%q", gotPath)
			}
			if !tt.wantErr {
				if len(gotBody.Movies) != 1 || len(gotBody.Shows) != 1 {
					t.Fatalf("body=%+v", gotBody)
				}
				if gotBody.Movies[0]["watched_at"] == nil {
					t.Fatal("movie watched_at dropped")
				}
				seasons := gotBody.Shows[0]["seasons"].([]any)
				ep := seasons[0].(map[string]any)["episodes"].([]any)[0].(map[string]any)
				if ep["watched_at"] == nil || ep["number"].(float64) != 1 {
					t.Fatalf("episode=%v", ep)
				}
			}
		})
	}
}
