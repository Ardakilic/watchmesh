// Package yamtrack implements the Yamtrack connector (target-first STUB).
//
// STUB (task 6.3): history/push paths, header form, and field enums are
// candidates from design §7 + nuances API notes. Confirm against the live
// instance (webhook/views.py + Integrations token screen) before finalizing;
// keep all mapping in mediaKind/toPayload/fromItem so fixes stay one-file.
package yamtrack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/watchmesh/watchmesh/internal/model"
)

// Client talks to Yamtrack via {base}/api/v1/media/{movie|show}/.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client // nil => http.DefaultClient
}

func New(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimSuffix(strings.TrimSpace(baseURL), "/"), Token: token}
}

// Name implements model.Target/model.Source.
func (c *Client) Name() string { return "yamtrack" }

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// addAuth sets Bearer auth.
// STUB note: live instance may expect X-API-Key (or ?token=) instead —
// task 6.3. Swap the header here only.
func (c *Client) addAuth(r *http.Request) {
	// Try Bearer first; documented fallback: X-API-Key: <token>.
	r.Header.Set("Authorization", "Bearer "+c.Token)
}

// mediaKind maps WatchItem type to Yamtrack collection; ok=false skips.
func mediaKind(w model.WatchItem) (string, bool) {
	switch w.MediaType {
	case "movie":
		return "movie", true
	case "show", "episode":
		return "show", true
	}
	return "", false
}

func endDate(w model.WatchItem) string {
	t := w.WatchedAt
	if t.IsZero() {
		t = time.Now().UTC()
	}
	return t.UTC().Format("2006-01-02")
}

// Push loops single POST per item, no batch call. TMDB==0 items skipped.
// Candidate body (STUB): {source:tmdb, media_type, media_id, status, end_date}.
func (c *Client) Push(ctx context.Context, items []model.WatchItem) error {
	for _, it := range items {
		if it.IDs.TMDB == 0 {
			continue
		}
		kind, ok := mediaKind(it)
		if !ok {
			continue
		}
		body, _ := json.Marshal(map[string]any{
			"source":     "tmdb",
			"media_type": kind,
			"media_id":   it.IDs.TMDB,
			"status":     "Completed",
			"end_date":   endDate(it),
		})
		// STUB: POST vs PATCH per-item confirmed live in task 6.3; POST default.
		req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/api/v1/media/"+kind+"/", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		c.addAuth(req)
		resp, err := c.httpClient().Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			return fmt.Errorf("yamtrack push: status %d", resp.StatusCode)
		}
		var v json.RawMessage
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			return fmt.Errorf("yamtrack push: malformed response: %w", err)
		}
	}
	return nil
}

type yamItem struct {
	Source    string `json:"source"`
	MediaType string `json:"media_type"`
	MediaID   int    `json:"media_id"`
	Title     string `json:"title"`
	EndDate   string `json:"end_date"`
}

type yamPage struct {
	Results []yamItem `json:"results"`
	Next    *string   `json:"next"`
}

func fromItem(y yamItem) (model.WatchItem, bool) {
	if y.MediaID == 0 {
		return model.WatchItem{}, false
	}
	w := model.WatchItem{MediaType: y.MediaType, Title: y.Title}
	if w.MediaType != "movie" {
		w.MediaType = "show"
	}
	w.IDs.TMDB = y.MediaID
	if y.EndDate != "" {
		if t, err := time.Parse("2006-01-02", y.EndDate); err == nil {
			w.WatchedAt = t
		}
	}
	return w, true
}

// History GETs /api/v1/media/{movie|show}/?limit=200&offset= following
// pagination.next until empty (STUB shape: DRF-style {results,next}).
func (c *Client) History(ctx context.Context, since time.Time) ([]model.WatchItem, error) {
	var out []model.WatchItem
	for _, kind := range []string{"movie", "show"} {
		offset := 0
		for {
			u := fmt.Sprintf("%s/api/v1/media/%s/?limit=200&offset=%d", c.BaseURL, kind, offset)
			req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
			if err != nil {
				return nil, err
			}
			c.addAuth(req)
			resp, err := c.httpClient().Do(req)
			if err != nil {
				return nil, err
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("yamtrack history: status %d", resp.StatusCode)
			}
			var p yamPage
			if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
				return nil, fmt.Errorf("yamtrack history: malformed response: %w", err)
			}
			if len(p.Results) == 0 {
				break
			}
			for _, y := range p.Results {
				w, ok := fromItem(y)
				if !ok {
					continue
				}
				if !since.IsZero() && !w.WatchedAt.After(since) && !w.WatchedAt.Equal(since) && !w.WatchedAt.IsZero() {
					continue
				}
				out = append(out, w)
			}
			offset += len(p.Results)
			if p.Next == nil || *p.Next == "" {
				break
			}
		}
	}
	return out, nil
}

// ValidateToken does one GET expecting 200.
func (c *Client) ValidateToken(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/api/v1/media/movie/?limit=1", nil)
	if err != nil {
		return err
	}
	c.addAuth(req)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("yamtrack auth: status %d", resp.StatusCode)
	}
	return nil
}
