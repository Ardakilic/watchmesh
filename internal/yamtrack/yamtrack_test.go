package yamtrack

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

// item builds a WatchItem with a TMDB ID and fixed timestamp.
func item(mediaType string, tmdb int) model.WatchItem {
	return model.WatchItem{IDs: model.IDs{TMDB: tmdb}, MediaType: mediaType,
		Title: "T", WatchedAt: time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)}
}

// TestPush verifies per-item POST path, Bearer auth, body, and error statuses.
func TestPush(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{"success", 200, `{}`, false},
		{"created", 201, `{}`, false},
		{"unauthorized", 401, `{}`, true},
		{"server-error", 500, `{}`, true},
		{"malformed", 200, `not json`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAuth, gotPath string
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()
			delivered, err := New(srv.URL, "tok").Push(context.Background(), []model.WatchItem{item("movie", 27205)})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if !tt.wantErr && len(delivered) != 1 {
				t.Fatalf("delivered=%d want 1", len(delivered))
			}
			if gotPath != "/api/v1/media/movie/" {
				t.Fatalf("path=%q", gotPath)
			}
			if gotAuth != "Bearer tok" {
				t.Fatalf("auth=%q", gotAuth)
			}
			if !tt.wantErr {
				if gotBody["source"] != "tmdb" || gotBody["media_type"] != "movie" ||
					gotBody["status"] != "Completed" {
					t.Fatalf("body=%v", gotBody)
				}
				if gotBody["media_id"].(float64) != 27205 {
					t.Fatalf("body=%v", gotBody)
				}
				if gotBody["end_date"] != "2026-05-10" {
					t.Fatalf("body=%v", gotBody)
				}
			}
		})
	}
}

// TestPushSkipsNoTMDB verifies TMDB==0 items send no requests.
func TestPushSkipsNoTMDB(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	if delivered, err := New(srv.URL, "t").Push(context.Background(), []model.WatchItem{item("movie", 0)}); err != nil {
		t.Fatal(err)
	} else if len(delivered) != 0 {
		t.Fatalf("delivered=%d want 0", len(delivered))
	}
	if n != 0 {
		t.Fatalf("requests=%d want 0", n)
	}
}

// TestPushNoBatch verifies one POST goes out per item, no batch call.
func TestPushNoBatch(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	delivered, err := New(srv.URL, "t").Push(context.Background(), []model.WatchItem{item("movie", 1), item("show", 2)})
	if err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 2 {
		t.Fatalf("delivered=%d want 2", len(delivered))
	}
	if n != 2 {
		t.Fatalf("requests=%d want 2 (single POST per item)", n)
	}
}

// TestHistoryPagination verifies next-continuation pages concatenate.
func TestHistoryPagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		off := r.URL.Query().Get("offset")
		if strings.HasPrefix(r.URL.Path, "/api/v1/media/movie/") {
			if off == "0" {
				fmt.Fprint(w, `{"results":[{"source":"tmdb","media_type":"movie","media_id":1,"title":"A","end_date":"2026-05-10"}],"next":"x"}`)
			} else {
				fmt.Fprint(w, `{"results":[{"source":"tmdb","media_type":"movie","media_id":2,"title":"B","end_date":"2026-05-11"}],"next":null}`)
			}
			return
		}
		fmt.Fprint(w, `{"results":[],"next":null}`)
	}))
	defer srv.Close()
	got, err := New(srv.URL, "t").History(context.Background(), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].IDs.TMDB != 1 || got[1].IDs.TMDB != 2 {
		t.Fatalf("got=%v", got)
	}
}

// TestHistoryError verifies 500/malformed history responses fail.
func TestHistoryError(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{"server-error", 500, `{}`},
		{"malformed", 200, `not json`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()
			if _, err := New(srv.URL, "t").History(context.Background(), time.Time{}); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

// TestValidateToken verifies 200 passes and a bad token fails.
func TestValidateToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
	}))
	defer srv.Close()
	if err := New(srv.URL, "tok").ValidateToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := New(srv.URL, "bad").ValidateToken(context.Background()); err == nil {
		t.Fatal("want error")
	}
}
