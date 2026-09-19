package trakt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// PollDeviceToken POSTs /oauth/device/token until 200; 400 means pending.
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
		if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusUnauthorized {
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
// Returns the access token.
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
	return c.PollDeviceToken(ctx, dc.DeviceCode, interval)
}
