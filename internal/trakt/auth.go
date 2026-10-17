package trakt

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// deviceCodeResp is the /oauth/device/code response with the user code.
type deviceCodeResp struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// deviceTokenResp is the /oauth/device/token success response.
type deviceTokenResp struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
}

// RequestDeviceCode POSTs /oauth/device/code {client_id}.
func (c *Client) RequestDeviceCode(ctx context.Context) (*deviceCodeResp, error) {
	base := strings.TrimSuffix(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	raw, _ := json.Marshal(map[string]string{"client_id": c.ClientID})
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/oauth/device/code", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("trakt device code: status %d", resp.StatusCode)
	}
	var dc deviceCodeResp
	if err := json.NewDecoder(resp.Body).Decode(&dc); err != nil {
		return nil, fmt.Errorf("trakt device code: malformed response: %w", err)
	}
	if dc.DeviceCode == "" {
		return nil, fmt.Errorf("trakt device code: empty device_code")
	}
	return &dc, nil
}

// maxPollWait caps any single device-poll wait, so a hostile Retry-After
// value can never park an interactive auth for minutes.
const maxPollWait = time.Minute

// slowDownBump is the +5s poll-interval bump Trakt mandates on slow_down.
const slowDownBump = 5 * time.Second

// slowDownWait bumps the poll interval by slowDownBump, overrides the one
// wait with a non-negative numeric Retry-After in seconds, and caps the
// wait at maxPollWait.
func slowDownWait(interval time.Duration, retryAfter string) (next, wait time.Duration) {
	next = interval + slowDownBump
	wait = next
	if n, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && n >= 0 {
		wait = time.Duration(n) * time.Second
	}
	return next, min(wait, maxPollWait)
}

// PollDeviceToken POSTs /oauth/device/token until 200. Per the device flow:
// 400 = pending (keep polling), 429 = slow_down (bump the interval by 5s,
// honor Retry-After when present, cap the wait), 404/409/410/418 are
// terminal (bad/expired/already-used/denied code) as is 401 and any other
// unexpected status — hammering a dead code would just read out its
// expiry again.
func (c *Client) PollDeviceToken(ctx context.Context, deviceCode string, interval time.Duration) (string, error) {
	base := strings.TrimSuffix(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	raw, _ := json.Marshal(map[string]string{
		"code": deviceCode, "client_id": c.ClientID, "client_secret": c.ClientSecret,
	})
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		req, err := http.NewRequestWithContext(ctx, "POST", base+"/oauth/device/token", bytes.NewReader(raw))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.httpClient().Do(req)
		if err != nil {
			return "", err
		}
		var tr deviceTokenResp
		func() {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				_ = json.NewDecoder(resp.Body).Decode(&tr)
			}
		}()
		if resp.StatusCode == http.StatusOK && tr.AccessToken != "" {
			return tr.AccessToken, nil
		}
		switch {
		case resp.StatusCode == http.StatusBadRequest:
			// Pending: user has not approved yet.
		case resp.StatusCode == http.StatusTooManyRequests:
			var wait time.Duration
			interval, wait = slowDownWait(interval, resp.Header.Get("Retry-After"))
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(wait):
			}
			continue
		case resp.StatusCode == http.StatusNotFound:
			return "", errors.New("trakt device token: code invalid or expired")
		case resp.StatusCode == http.StatusConflict:
			return "", errors.New("trakt device token: code already issued (another device authenticated)")
		case resp.StatusCode == http.StatusGone:
			return "", errors.New("trakt device token: code expired")
		case resp.StatusCode == http.StatusTeapot:
			return "", errors.New("trakt device token: user denied the code")
		default:
			return "", fmt.Errorf("trakt device token: status %d", resp.StatusCode)
		}
		if interval > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(interval):
			}
		}
	}
}

// DeviceAuth prints verification_url + user_code then polls per interval.
// Polling is bounded by the code's expires_in seconds via a context deadline;
// exhaustion yields an expiry error instead of a raw context error. Returns
// the access token.
func (c *Client) DeviceAuth(ctx context.Context, out io.Writer) (string, error) {
	dc, err := c.RequestDeviceCode(ctx)
	if err != nil {
		return "", err
	}
	fmt.Fprintln(out, "Visit:", dc.VerificationURL)
	fmt.Fprintln(out, "Code:", dc.UserCode)
	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if dc.ExpiresIn <= 0 {
		return c.PollDeviceToken(ctx, dc.DeviceCode, interval)
	}
	pollCtx, cancel := context.WithTimeout(ctx, time.Duration(dc.ExpiresIn)*time.Second)
	defer cancel()
	tok, err := c.PollDeviceToken(pollCtx, dc.DeviceCode, interval)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return "", fmt.Errorf("trakt device code expired after %ds", dc.ExpiresIn)
	}
	return tok, err
}
