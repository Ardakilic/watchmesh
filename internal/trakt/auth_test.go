package trakt

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

// TestDeviceAuth verifies the code→print→poll flow returns the token.
func TestDeviceAuth(t *testing.T) {
	var tokenHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/device/code":
			fmt.Fprint(w, `{"device_code":"dev123","user_code":"ABC-123","verification_url":"https://trakt.tv/activate","expires_in":600,"interval":0}`)
		case "/oauth/device/token":
			tokenHits++
			fmt.Fprint(w, `{"access_token":"tok123","token_type":"Bearer"}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tok, err := New(srv.URL, "cid", "sec", "").DeviceAuth(ctx, &out)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "tok123" {
		t.Fatalf("token=%q", tok)
	}
	if !strings.Contains(out.String(), "https://trakt.tv/activate") || !strings.Contains(out.String(), "ABC-123") {
		t.Fatalf("output=%q", out.String())
	}
	if tokenHits != 1 {
		t.Fatalf("tokenHits=%d", tokenHits)
	}
}

// TestDeviceAuthCodeError verifies 401/500 on the code step fail.
func TestDeviceAuthCodeError(t *testing.T) {
	for _, status := range []int{401, 500} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, `{}`)
		}))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := New(srv.URL, "cid", "sec", "").DeviceAuth(ctx, &bytes.Buffer{})
		cancel()
		srv.Close()
		if err == nil {
			t.Fatalf("status %d: want error", status)
		}
	}
}

// TestPollRespectsInterval verifies pending polls retry until the token lands.
func TestPollRespectsInterval(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Content-Type", "application/json")
		if n < 3 {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"authorization_pending"}`)
			return
		}
		fmt.Fprint(w, `{"access_token":"tok"}`)
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tok, err := New(srv.URL, "cid", "sec", "").PollDeviceToken(ctx, "dev", 0)
	if err != nil || tok != "tok" {
		t.Fatalf("tok=%q err=%v", tok, err)
	}
	if n != 3 {
		t.Fatalf("polls=%d want 3", n)
	}
}

// TestDeviceAuthExpires verifies polling stops at the code's expires_in
// deadline and reports expiry instead of a raw context error.
func TestDeviceAuthExpires(t *testing.T) {
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/oauth/device/code":
			fmt.Fprint(w, `{"device_code":"dev","user_code":"AB","verification_url":"https://trakt.tv/activate","expires_in":1,"interval":0}`)
		case "/oauth/device/token":
			polls++
			w.WriteHeader(400) // always pending
		}
	}))
	defer srv.Close()
	_, err := New(srv.URL, "cid", "sec", "").DeviceAuth(context.Background(), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "trakt device code expired after 1s") {
		t.Fatalf("err=%v want expiry after 1s", err)
	}
	if polls < 1 {
		t.Fatal("poll never ran")
	}
}
