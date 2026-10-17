package yamtrack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/watchmesh/watchmesh/internal/model"
)

// item builds a WatchItem with a TMDB ID and fixed timestamp.
func item(mediaType string, tmdb int) model.WatchItem {
	return model.WatchItem{IDs: model.IDs{TMDB: tmdb}, MediaType: mediaType,
		Title: "T", WatchedAt: time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)}
}

// TestPush verifies per-item webhook POSTs: path carries the token, no
// Authorization header is sent, and the stop-event body matches upstream's
// Jellyfin intake (string provider ids, no watched date).
func TestPush(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"success", 200, false},
		{"created", 201, true},
		{"unauthorized", 401, true},
		{"bad-payload", 400, true},
		{"server-error", 500, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got struct {
				Path, Auth, Ua, Ctype string
				Body                  []byte
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got.Path = r.URL.Path
				got.Auth = r.Header.Get("Authorization")
				got.Ua = r.Header.Get("User-Agent")
				got.Ctype = r.Header.Get("Content-Type")
				data, _ := io.ReadAll(r.Body)
				got.Body = data
				w.WriteHeader(tt.status)
				fmt.Fprint(w, `{}`)
			}))
			defer srv.Close()
			m := item("movie", 27205)
			m.IDs.IMDB = "tt0133093"
			delivered, err := New(srv.URL, "tok").Push(context.Background(), []model.WatchItem{m})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if !tt.wantErr && len(delivered) != 1 {
				t.Fatalf("delivered=%d want 1", len(delivered))
			}
			if got.Path != "/webhook/jellyfin/tok" {
				t.Fatalf("path=%q", got.Path)
			}
			if got.Auth != "" {
				t.Fatalf("auth=%q, token must live in the path", got.Auth)
			}
			if got.Ua != model.UserAgent {
				t.Fatalf("ua=%q", got.Ua)
			}
			if got.Ctype != "application/json" {
				t.Fatalf("content-type=%q", got.Ctype)
			}
			var b map[string]any
			if err := json.Unmarshal(got.Body, &b); err != nil {
				t.Fatalf("body %s: %v", got.Body, err)
			}
			if b["Event"] != "Stop" {
				t.Fatalf("event=%v", b["Event"])
			}
			it := b["Item"].(map[string]any)
			if it["Type"] != "Movie" {
				t.Fatalf("type=%v", it["Type"])
			}
			if it["UserData"].(map[string]any)["Played"] != true {
				t.Fatalf("userData=%v", it["UserData"])
			}
			ids := it["ProviderIds"].(map[string]any)
			if ids["Tmdb"] != "27205" || ids["Imdb"] != "tt0133093" {
				t.Fatalf("providerIds=%v, ids are strings upstream", ids)
			}
			if _, ok := it["ParentIndexNumber"]; ok {
				t.Fatalf("movie carries episode numbering: %v", it)
			}
			if _, ok := b["Series"]; ok {
				t.Fatalf("movie carries a series block: %v", b)
			}
			if _, leaked := b["end_date"]; leaked || len(got.Body) > 300 {
				t.Fatalf("watched dates must never leak: %s", got.Body)
			}
		})
	}
}

// TestPushEpisodePayload verifies episode bodies carry the series TMDB block
// and episode numbering, and only ever an IMDB id at Item level.
func TestPushEpisodePayload(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(data))
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	withImdb := item("episode", 155)
	withImdb.IDs.IMDB = "tt123"
	withImdb.Season, withImdb.Episode = 2, 3
	bare := item("episode", 155)
	bare.Title = "" // SeriesName must be omitted when the series is unnamed
	bare.Season, bare.Episode = 1, 1
	delivered, err := New(srv.URL, "tok").Push(context.Background(), []model.WatchItem{withImdb, bare})
	if err != nil || len(delivered) != 2 {
		t.Fatalf("delivered=%d err=%v", len(delivered), err)
	}
	if len(bodies) != 2 {
		t.Fatalf("bodies=%d want 2", len(bodies))
	}
	var b map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &b); err != nil {
		t.Fatal(err)
	}
	it := b["Item"].(map[string]any)
	if it["Type"] != "Episode" || it["SeriesName"] != "T" {
		t.Fatalf("item=%v", it)
	}
	if it["ParentIndexNumber"].(float64) != 2 || it["IndexNumber"].(float64) != 3 {
		t.Fatalf("numbering=%v", it)
	}
	if ids := it["ProviderIds"].(map[string]any); len(ids) != 1 || ids["Imdb"] != "tt123" {
		t.Fatalf("episode item ids=%v, only Imdb is precise", ids)
	}
	series := b["Series"].(map[string]any)["ProviderIds"].(map[string]any)
	if series["Tmdb"] != "155" {
		t.Fatalf("series ids=%v", series)
	}
	var bare2 map[string]any
	if err := json.Unmarshal([]byte(bodies[1]), &bare2); err != nil {
		t.Fatal(err)
	}
	if _, ok := bare2["Item"].(map[string]any)["ProviderIds"]; ok {
		t.Fatalf("episode without IMDb must omit ProviderIds: %s", bodies[1])
	}
	if _, ok := bare2["Item"].(map[string]any)["SeriesName"]; ok {
		t.Fatalf("empty title must omit SeriesName: %s", bodies[1])
	}
}

// TestPushPartialDelivery verifies items sent before a mid-loop failure
// are returned alongside the error instead of discarded.
func TestPushPartialDelivery(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 2 {
			w.WriteHeader(500)
			fmt.Fprint(w, `{}`)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	delivered, err := New(srv.URL, "t").Push(context.Background(), []model.WatchItem{item("movie", 1), item("movie", 2)})
	if err == nil {
		t.Fatal("second-item failure must surface")
	}
	if len(delivered) != 1 {
		t.Fatalf("delivered=%d want 1", len(delivered))
	}
}

// TestPushSkipsShapes verifies shapes the webhook cannot resolve send no
// requests: bare shows, unknown kinds, TMDB==0 items, S0E0 episodes.
func TestPushSkipsShapes(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	tests := []struct {
		name string
		item model.WatchItem
	}{
		{"show", model.WatchItem{IDs: model.IDs{TMDB: 5}, MediaType: "show", WatchedAt: time.Now()}},
		{"podcast", model.WatchItem{IDs: model.IDs{TMDB: 5}, MediaType: "podcast", WatchedAt: time.Now()}},
		{"movie-no-tmdb", model.WatchItem{IDs: model.IDs{IMDB: "tt0"}, MediaType: "movie", WatchedAt: time.Now()}},
		{"episode-no-tmdb", model.WatchItem{IDs: model.IDs{IMDB: "tt0"}, MediaType: "episode", Season: 1, Episode: 2, WatchedAt: time.Now()}},
		{"episode-s0e0", model.WatchItem{IDs: model.IDs{TMDB: 5}, MediaType: "episode", WatchedAt: time.Now()}},
	}
	var delivered []model.WatchItem
	for _, tt := range tests {
		d, err := New(srv.URL, "t").Push(context.Background(), []model.WatchItem{tt.item})
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		delivered = append(delivered, d...)
	}
	if len(delivered) != 0 {
		t.Fatalf("delivered=%v want none", delivered)
	}
	if n != 0 {
		t.Fatalf("requests=%d want 0", n)
	}
}

// TestHistoryNoOp verifies History reports the read gap silently; nothing
// is fetched even though a server is listening.
func TestHistoryNoOp(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(500)
	}))
	defer srv.Close()
	for _, since := range []time.Time{time.Time{}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} {
		got, err := New(srv.URL, "t").History(context.Background(), since)
		if err != nil || len(got) != 0 {
			t.Fatalf("got=%v err=%v, history must be empty,nil", got, err)
		}
	}
	if hits != 0 {
		t.Fatalf("requests=%d want 0", hits)
	}
}

// TestValidateToken probes the webhook intake: a valid token answers 400
// ("Missing payload"), an unknown one 401; no Authorization header is sent.
func TestValidateToken(t *testing.T) {
	var authSeen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authSeen = r.Header.Get("Authorization")
		if r.URL.Path == "/webhook/jellyfin/good" {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"res":"error","message":"Missing payload"}`)
			return
		}
		w.WriteHeader(401)
	}))
	defer srv.Close()
	if err := New(srv.URL, "good").ValidateToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if authSeen != "" {
		t.Fatalf("auth=%q, token must live in the path", authSeen)
	}
	if err := New(srv.URL, "bad").ValidateToken(context.Background()); err == nil {
		t.Fatal("invalid token must fail")
	}
}
