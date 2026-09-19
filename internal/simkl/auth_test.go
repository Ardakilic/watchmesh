package simkl

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestPINAuth verifies the PIN print→poll flow returns the token.
func TestPINAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/oauth/pin":
			if !strings.Contains(r.URL.RawQuery, "client_id=cid") {
				t.Errorf("query=%q", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"user_code":"PIN123","verification_url":"https://simkl.com/pin","expires_in":600,"interval":0}`)
		case r.URL.Path == "/oauth/pin/PIN123":
			fmt.Fprint(w, `{"access_token":"simkl-tok","result":"ok"}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tok, err := New(srv.URL, "cid", "").PINAuth(ctx, &out)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "simkl-tok" {
		t.Fatalf("token=%q", tok)
	}
	if !strings.Contains(out.String(), "https://simkl.com/pin") || !strings.Contains(out.String(), "PIN123") {
		t.Fatalf("output=%q", out.String())
	}
}

// TestPINAuthError verifies 401/500 on the PIN step fail.
func TestPINAuthError(t *testing.T) {
	for _, status := range []int{401, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, `{}`)
		}))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := New(srv.URL, "cid", "").PINAuth(ctx, &bytes.Buffer{})
		cancel()
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: want error", status)
		}
	}
}

// TestPollPINPendingThenOK verifies pending polls retry until the token lands.
func TestPollPINPendingThenOK(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "application/json")
		if n < 2 {
			w.WriteHeader(404)
			fmt.Fprint(w, `{}`)
			return
		}
		fmt.Fprint(w, `{"access_token":"tok"}`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tok, err := New(srv.URL, "cid", "").PollPINToken(ctx, "PIN", 0)
	if err != nil || tok != "tok" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
	if n != 2 {
		t.Fatalf("polls=%d want 2", n)
	}
}
