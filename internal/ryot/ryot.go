// Package ryot implements the Ryot connector (target-first STUB).
//
// STUB (task 5.3): mutation/query names and field enums are candidates from
// design §7 + nuances API notes. Introspect the live GraphQL schema
// ({base}/backend/graphql) and token screen before finalizing; keep all
// mapping in metadataID/finishedOn below so corrections stay a one-file diff.
package ryot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/watchmesh/watchmesh/internal/model"
)

// Client pushes to Ryot via Bearer POST {base}/backend/graphql.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client // nil => http.DefaultClient
}

// New builds a Client for a Ryot base URL and Bearer token.
func New(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimSuffix(strings.TrimSpace(baseURL), "/"), Token: token}
}

// Name implements model.Target/model.Source.
func (c *Client) Name() string { return "ryot" }

// httpClient returns the injected client, or http.DefaultClient when nil.
func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// metadataID maps a WatchItem to Ryot's tmdb:// identifier, TMDB-first.
// Returns ok=false when TMDB==0 or for episodes: episodes have no
// episode-capable push payload yet, so callers skip them rather than
// recording a wrong show-level completion.
func metadataID(w model.WatchItem) (id string, ok bool) {
	if w.IDs.TMDB == 0 {
		return "", false
	}
	if w.MediaType == "episode" {
		return "", false
	}
	kind := "movie"
	if w.MediaType == "show" {
		kind = "show"
	}
	return fmt.Sprintf("tmdb://%s/%d", kind, w.IDs.TMDB), true
}

// finishedOn formats WatchedAt for the mutation; zero time becomes now.
func finishedOn(w model.WatchItem) string {
	t := w.WatchedAt
	if t.IsZero() {
		t = time.Now().UTC()
	}
	return t.UTC().Format(time.RFC3339)
}

// gqlReq is a GraphQL request body with query and variables.
type gqlReq struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// gqlResp is a GraphQL response body; non-empty Errors means failure.
type gqlResp struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// Push sends one GraphQL mutation per item and returns the items actually
// delivered; TMDB==0 items and episodes are skipped (no episode-capable
// payload yet) and omitted from delivered, so the engine never marks them.
// Items delivered before a mid-loop failure are returned alongside the error.
// Candidate mutation (STUB): mutation($i:UpdateSeenInput!){updateSeenHistory(i:$i)}.
func (c *Client) Push(ctx context.Context, items []model.WatchItem) ([]model.WatchItem, error) {
	var delivered []model.WatchItem
	for _, it := range items {
		mid, ok := metadataID(it)
		if !ok {
			continue
		}
		if err := c.pushOne(ctx, it, mid); err != nil {
			return delivered, err
		}
		delivered = append(delivered, it)
	}
	return delivered, nil
}

// doWithRetry runs build once, and on 429 waits Retry-After once then retries once.
func doWithRetry(ctx context.Context, hc *http.Client, build func() (*http.Request, error)) (*http.Response, error) {
	req, err := build()
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		return resp, nil
	}
	secs := 1
	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(ra)); err == nil && n >= 0 {
			secs = n
		}
	}
	resp.Body.Close()
	if secs > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(secs) * time.Second):
		}
	}
	req2, err := build()
	if err != nil {
		return nil, err
	}
	return hc.Do(req2)
}

// pushOne POSTs one updateSeenHistory mutation for mid.
func (c *Client) pushOne(ctx context.Context, it model.WatchItem, mid string) error {
	body, _ := json.Marshal(gqlReq{
		Query: "mutation($i:UpdateSeenInput!){updateSeenHistory(i:$i)}",
		Variables: map[string]any{"i": map[string]any{
			"metadataId": mid,
			"state":      "Completed",
			"finishedOn": finishedOn(it),
		}},
	})
	resp, err := doWithRetry(ctx, c.httpClient(), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/backend/graphql", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.Token)
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ryot push: status %d", resp.StatusCode)
	}
	var gr gqlResp
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return fmt.Errorf("ryot push: malformed response: %w", err)
	}
	if len(gr.Errors) > 0 {
		return fmt.Errorf("ryot push: %s", gr.Errors[0].Message)
	}
	return nil
}

// History is best-effort: any error yields empty,nil so gaps never fail sync (STUB query).
func (c *Client) History(ctx context.Context, since time.Time) ([]model.WatchItem, error) {
	items, err := c.history(ctx, since)
	if err != nil {
		return nil, nil
	}
	return items, nil
}

// historyItem is one userMediaList element from the history query.
type historyItem struct {
	MetadataID string `json:"metadataId"`
	FinishedOn string `json:"finishedOn"`
	Title      string `json:"title"`
}

// history runs the userMediaList query and maps entries to WatchItems.
func (c *Client) history(ctx context.Context, since time.Time) ([]model.WatchItem, error) {
	body, _ := json.Marshal(gqlReq{Query: "query{userMediaList{metadataId finishedOn title}}"})
	req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/backend/graphql", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var gr struct {
		Data struct {
			List []historyItem `json:"userMediaList"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return nil, err
	}
	if len(gr.Errors) > 0 {
		return nil, fmt.Errorf("%s", gr.Errors[0].Message)
	}
	var out []model.WatchItem
	for _, h := range gr.Data.List {
		var w model.WatchItem
		rest, ok := strings.CutPrefix(h.MetadataID, "tmdb://")
		if !ok {
			continue
		}
		kind, idStr, ok := strings.Cut(rest, "/")
		if !ok {
			continue
		}
		var id int
		if _, err := fmt.Sscanf(idStr, "%d", &id); err != nil || id == 0 {
			continue
		}
		w.IDs.TMDB = id
		if kind == "movie" {
			w.MediaType = "movie"
		} else {
			w.MediaType = "show"
		}
		w.Title = h.Title
		if h.FinishedOn != "" {
			if t, err := time.Parse(time.RFC3339, h.FinishedOn); err == nil {
				w.WatchedAt = t
			}
		}
		if !since.IsZero() && !w.WatchedAt.After(since) && !w.WatchedAt.Equal(since) {
			continue
		}
		out = append(out, w)
	}
	return out, nil
}

// ValidateToken does one GET {base}/backend/config expecting 200.
func (c *Client) ValidateToken(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/backend/config", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ryot auth: status %d", resp.StatusCode)
	}
	return nil
}
