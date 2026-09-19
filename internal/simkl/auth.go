package simkl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type pinResp struct {
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type pinTokenResp struct {
	AccessToken string `json:"access_token"`
	Result      string `json:"result"`
}

// RequestPIN GETs /oauth/pin?client_id=.
func (c *Client) RequestPIN(ctx context.Context) (*pinResp, error) {
	base := strings.TrimSuffix(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	u := base + "/oauth/pin?client_id=" + url.QueryEscape(c.ClientID)
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("simkl pin: status %d", resp.StatusCode)
	}
	var p pinResp
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return nil, fmt.Errorf("simkl pin: malformed response: %w", err)
	}
	if p.UserCode == "" {
		return nil, fmt.Errorf("simkl pin: empty user_code")
	}
	return &p, nil
}

// PollPINToken GETs /oauth/pin/{code} until an access_token appears.
func (c *Client) PollPINToken(ctx context.Context, userCode string, interval time.Duration) (string, error) {
	base := strings.TrimSuffix(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	u := base + "/oauth/pin/" + url.PathEscape(userCode)
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return "", err
		}
		c.setHeaders(req)
		resp, err := c.httpClient().Do(req)
		if err != nil {
			return "", err
		}
		var tr pinTokenResp
		status := resp.StatusCode
		func() {
			defer resp.Body.Close()
			if status == http.StatusOK {
				_ = json.NewDecoder(resp.Body).Decode(&tr)
			}
		}()
		if status == http.StatusOK && tr.AccessToken != "" {
			return tr.AccessToken, nil
		}
		if status != http.StatusOK && status != http.StatusNotFound && status != http.StatusBadRequest && status != http.StatusUnauthorized {
			return "", fmt.Errorf("simkl pin poll: status %d", status)
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

// PINAuth prints the verification URL + code then polls per interval.
// Returns the access token.
func (c *Client) PINAuth(ctx context.Context, out io.Writer) (string, error) {
	p, err := c.RequestPIN(ctx)
	if err != nil {
		return "", err
	}
	fmt.Fprintln(out, "Visit:", p.VerificationURL)
	fmt.Fprintln(out, "Code:", p.UserCode)
	interval := time.Duration(p.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return c.PollPINToken(ctx, p.UserCode, interval)
}
