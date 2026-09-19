package trakt

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

// checkHeaders asserts the Trakt key, Bearer, and version headers.
func checkHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("trakt-api-key") != "cid" {
		t.Fatalf("trakt-api-key=%q", r.Header.Get("trakt-api-key"))
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("auth=%q", r.Header.Get("Authorization"))
	}
	if r.Header.Get("trakt-api-version") != "2" {
		t.Fatalf("version=%q", r.Header.Get("trakt-api-version"))
	}
}

// TestHistoryMoviesAndEpisodes verifies movie/episode mapping keeps IDs and WatchedAt.
func TestHistoryMoviesAndEpisodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkHeaders(t, r)
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/movies") {
			if r.URL.Query().Get("page") != "1" {
				fmt.Fprint(w, `[]`)
				return
			}
			if r.URL.Query().Get("start_at") == "" {
				t.Error("missing start_at")
			}
			fmt.Fprint(w, `[{"watched_at":"2026-05-10T20:00:00.000Z","movie":{"title":"M","year":2007,"ids":{"trakt":1,"imdb":"tt1201607","tmdb":603}}}]`)
			return
		}
		if strings.Contains(r.URL.Path, "/episodes") {
			if r.URL.Query().Get("page") != "1" {
				fmt.Fprint(w, `[]`)
				return
			}
			fmt.Fprint(w, `[{"watched_at":"2026-05-13T19:00:00.000Z","episode":{"season":1,"number":1,"ids":{"trakt":16,"tvdb":269953,"tmdb":349232}},"show":{"title":"S","year":2010,"ids":{"trakt":2}}}]`)
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()
	got, err := New(srv.URL, "cid", "sec", "tok").History(context.Background(), time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got=%v", got)
	}
	m, e := got[0], got[1]
	if m.MediaType != "movie" || m.IDs.Trakt != 1 || m.IDs.IMDB != "tt1201607" || m.IDs.TMDB != 603 {
		t.Fatalf("movie=%+v", m)
	}
	if m.WatchedAt.IsZero() || m.WatchedAt.Year() != 2026 {
		t.Fatalf("movie watched_at dropped: %+v", m)
	}
	if e.MediaType != "episode" || e.Season != 1 || e.Episode != 1 || e.IDs.TVDB != 269953 {
		t.Fatalf("episode=%+v", e)
	}
	if e.WatchedAt.IsZero() {
		t.Fatal("episode watched_at dropped")
	}
}

// TestHistoryPagination verifies paging concatenates pages until empty.
func TestHistoryPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/movies") {
			switch r.URL.Query().Get("page") {
			case "1":
				fmt.Fprint(w, `[{"watched_at":"2026-05-10T20:00:00Z","movie":{"title":"A","ids":{"trakt":1}}}]`)
			case "2":
				fmt.Fprint(w, `[{"watched_at":"2026-05-11T20:00:00Z","movie":{"title":"B","ids":{"trakt":2}}}]`)
			default:
				fmt.Fprint(w, `[]`)
			}
			return
		}
		fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()
	got, err := New(srv.URL, "cid", "sec", "tok").History(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].IDs.Trakt != 1 || got[1].IDs.Trakt != 2 {
		t.Fatalf("got=%v", got)
	}
}

// TestHistoryPageLimit verifies endless history errors instead of truncating.
func TestHistoryPageLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"watched_at":"2026-05-10T20:00:00Z","movie":{"title":"A","ids":{"trakt":1}}}]`)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "cid", "sec", "tok").History(context.Background(), time.Time{}); err == nil {
		t.Fatal("endless pages must fail with a limit error")
	} else if !strings.Contains(err.Error(), "pages") {
		t.Fatalf("err=%v want page-limit error", err)
	}
}

// TestHistoryError verifies 401/500/malformed history responses fail.
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
			if _, err := New(srv.URL, "cid", "sec", "tok").History(context.Background(), time.Time{}); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

// TestPush verifies POST /sync/history shape, path, and error statuses.
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
				Movies   []map[string]any `json:"movies"`
				Episodes []map[string]any `json:"episodes"`
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
			at := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)
			items := []model.WatchItem{
				{IDs: model.IDs{Trakt: 1, IMDB: "tt1201607", TMDB: 603}, MediaType: "movie", WatchedAt: at},
				{IDs: model.IDs{Trakt: 16, TVDB: 269953}, MediaType: "episode", Season: 1, Episode: 1, WatchedAt: at},
			}
			delivered, err := New(srv.URL, "cid", "sec", "tok").Push(context.Background(), items)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if !tt.wantErr && len(delivered) != 2 {
				t.Fatalf("delivered=%d want 2", len(delivered))
			}
			if gotPath != "/sync/history" {
				t.Fatalf("path=%q", gotPath)
			}
			if !tt.wantErr {
				if len(gotBody.Movies) != 1 || len(gotBody.Episodes) != 1 {
					t.Fatalf("body=%+v", gotBody)
				}
				if gotBody.Movies[0]["watched_at"] == nil || gotBody.Episodes[0]["watched_at"] == nil {
					t.Fatalf("watched_at dropped: %+v", gotBody)
				}
				mids := gotBody.Movies[0]["ids"].(map[string]any)
				if mids["trakt"].(float64) != 1 || mids["imdb"] != "tt1201607" {
					t.Fatalf("movie ids=%v", mids)
				}
			}
		})
	}
}
