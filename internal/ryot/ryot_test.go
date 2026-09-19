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

func item(tmdb int) model.WatchItem {
	return model.WatchItem{IDs: model.IDs{TMDB: tmdb}, MediaType: "movie",
		Title: "T", WatchedAt: time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)}
}

func TestPush(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{"success", 200, `{"data":{}}`, false},
		{"unauthorized", 401, `{}`, true},
		{"server-error", 500, `{}`, true},
		{"malformed", 200, `not json`, true},
		{"graphql-errors", 200, `{"errors":[{"message":"bad"}]}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAuth, gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()
			c := New(srv.URL, "tok")
			err := c.Push(context.Background(), []model.WatchItem{item(603)})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if gotPath != "/backend/graphql" {
				t.Fatalf("path=%q", gotPath)
			}
			if gotAuth != "Bearer tok" {
				t.Fatalf("auth=%q", gotAuth)
			}
		})
	}
}

func TestPushSkipsNoTMDB(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		fmt.Fprint(w, `{"data":{}}`)
	}))
	defer srv.Close()
	if err := New(srv.URL, "t").Push(context.Background(), []model.WatchItem{item(0)}); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("requests=%d want 0", n)
	}
}

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

func TestHistorySuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"userMediaList":[{"metadataId":"tmdb://movie/603","finishedOn":"2026-05-10T20:00:00Z","title":"T"}]}}`)
	}))
	defer srv.Close()
	got, err := New(srv.URL, "t").History(context.Background(), time.Time{})
	if err != nil || len(got) != 1 || got[0].IDs.TMDB != 603 {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestValidateToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend/config" {
			w.WriteHeader(404)
			return
		}
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
