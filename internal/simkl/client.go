package simkl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/watchmesh/watchmesh/internal/model"
)

// Client talks to Simkl sync endpoints.
type Client struct {
	BaseURL  string
	ClientID string
	Token    string
	HTTP     *http.Client // nil => http.DefaultClient

	mu       sync.Mutex
	lastGET  time.Time
	lastPOST time.Time
}

// New builds a Client; empty baseURL means DefaultBaseURL.
func New(baseURL, clientID, token string) *Client {
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{BaseURL: baseURL, ClientID: clientID, Token: token}
}

// Name implements model Source/Target naming.
func (c *Client) Name() string { return "simkl" }

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) setHeaders(r *http.Request) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	r.Header.Set("simkl-api-key", c.ClientID)
	if c.Token != "" {
		r.Header.Set("Authorization", "Bearer "+c.Token)
	}
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

// throttleGET enforces 10 GET/s via 100ms spacing.
func (c *Client) throttleGET() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.lastGET.IsZero() {
		if d := 100*time.Millisecond - time.Since(c.lastGET); d > 0 {
			time.Sleep(d)
		}
	}
	c.lastGET = time.Now()
}

// throttlePOST enforces 1 POST/s.
// ponytail: sleep throttle, token-bucket if limits bite.
func (c *Client) throttlePOST() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.lastPOST.IsZero() {
		if d := time.Second - time.Since(c.lastPOST); d > 0 {
			time.Sleep(d)
		}
	}
	c.lastPOST = time.Now()
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05Z07:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

type activityCursor struct {
	WatchedAt string `json:"watched_at"`
}

func (c *Client) activities(ctx context.Context) (map[string]activityCursor, error) {
	c.throttleGET()
	resp, err := doWithRetry(ctx, c.httpClient(), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", c.BaseURL+"/sync/activities", nil)
		if err != nil {
			return nil, err
		}
		c.setHeaders(req)
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("simkl activities: status %d", resp.StatusCode)
	}
	var m map[string]activityCursor
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("simkl activities: malformed response: %w", err)
	}
	return m, nil
}

type simklIDs struct {
	Simkl int    `json:"simkl,omitempty"`
	Slug  string `json:"slug,omitempty"`
	IMDB  string `json:"imdb,omitempty"`
	TMDB  int    `json:"tmdb,omitempty"`
	TVDB  int    `json:"tvdb,omitempty"`
}

type movieItem struct {
	Title       string   `json:"title"`
	Year        int      `json:"year"`
	IDs         simklIDs `json:"ids"`
	WatchedAt   string   `json:"watched_at"`
	LastWatched string   `json:"last_watched_at"`
}

type showItem struct {
	Title   string   `json:"title"`
	Year    int      `json:"year"`
	IDs     simklIDs `json:"ids"`
	Seasons []struct {
		Number   int `json:"number"`
		Episodes []struct {
			Number    int    `json:"number"`
			WatchedAt string `json:"watched_at"`
		} `json:"episodes"`
	} `json:"seasons"`
}

func toIDs(s simklIDs) model.IDs {
	return model.IDs{Simkl: s.Simkl, IMDB: s.IMDB, TMDB: s.TMDB, TVDB: s.TVDB}
}

func (c *Client) fetchCategory(ctx context.Context, cat string, since time.Time) ([]model.WatchItem, error) {
	c.throttleGET()
	u := fmt.Sprintf("%s/sync/all-items/%s", c.BaseURL, cat)
	if !since.IsZero() {
		u += "?date_from=" + since.UTC().Format(time.RFC3339)
	}
	resp, err := doWithRetry(ctx, c.httpClient(), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return nil, err
		}
		c.setHeaders(req)
		return req, nil
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("simkl all-items %s: status %d", cat, resp.StatusCode)
	}
	var out []model.WatchItem
	if cat == "movies" {
		var v struct {
			Movies []movieItem `json:"movies"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			return nil, fmt.Errorf("simkl all-items %s: malformed response: %w", cat, err)
		}
		for _, m := range v.Movies {
			at := parseTime(m.WatchedAt)
			if at.IsZero() {
				at = parseTime(m.LastWatched)
			}
			if !since.IsZero() && !at.IsZero() && at.Before(since) {
				continue
			}
			out = append(out, model.WatchItem{
				IDs: toIDs(m.IDs), MediaType: "movie",
				Title: m.Title, Year: m.Year, WatchedAt: at,
			})
		}
		return out, nil
	}
	var v map[string][]showItem
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("simkl all-items %s: malformed response: %w", cat, err)
	}
	list := v[cat]
	if list == nil {
		list = v["shows"]
	}
	for _, s := range list {
		for _, sn := range s.Seasons {
			for _, ep := range sn.Episodes {
				at := parseTime(ep.WatchedAt)
				if !since.IsZero() && !at.IsZero() && at.Before(since) {
					continue
				}
				out = append(out, model.WatchItem{
					IDs: toIDs(s.IDs), MediaType: "episode",
					Title: s.Title, Year: s.Year,
					Season: sn.Number, Episode: ep.Number, WatchedAt: at,
				})
			}
		}
	}
	return out, nil
}

// History dirty-checks GET /sync/activities, then GETs /sync/all-items only
// for categories newer than since. Never timer-polls all-items when unchanged.
func (c *Client) History(ctx context.Context, since time.Time) ([]model.WatchItem, error) {
	acts, err := c.activities(ctx)
	if err != nil {
		return nil, err
	}
	if !since.IsZero() {
		newer := false
		for _, cat := range []string{"movies", "shows", "anime"} {
			if t := parseTime(acts[cat].WatchedAt); !t.IsZero() && t.After(since) {
				newer = true
				break
			}
		}
		if !newer {
			return nil, nil
		}
	}
	var out []model.WatchItem
	for _, cat := range []string{"movies", "shows", "anime"} {
		if !since.IsZero() {
			if t := parseTime(acts[cat].WatchedAt); !t.IsZero() && !t.After(since) {
				continue
			}
		}
		items, err := c.fetchCategory(ctx, cat, since)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

type pushMovie struct {
	WatchedAt string   `json:"watched_at"`
	IDs       simklIDs `json:"ids"`
}

type pushEpisode struct {
	Number    int    `json:"number"`
	WatchedAt string `json:"watched_at"`
}

type pushSeason struct {
	Number   int           `json:"number"`
	Episodes []pushEpisode `json:"episodes"`
}

type pushShow struct {
	IDs     simklIDs     `json:"ids"`
	Seasons []pushSeason `json:"seasons,omitempty"`
}

// Push POSTs /sync/history per design §7 at 1 POST/s.
func (c *Client) Push(ctx context.Context, items []model.WatchItem) error {
	c.throttlePOST()
	body := struct {
		Movies []pushMovie `json:"movies"`
		Shows  []pushShow  `json:"shows"`
	}{Movies: []pushMovie{}, Shows: []pushShow{}}
	for _, it := range items {
		at := it.WatchedAt.UTC().Format(time.RFC3339)
		if it.WatchedAt.IsZero() {
			at = time.Now().UTC().Format(time.RFC3339)
		}
		switch it.MediaType {
		case "movie":
			body.Movies = append(body.Movies, pushMovie{WatchedAt: at, IDs: simklIDs{
				Simkl: it.IDs.Simkl, IMDB: it.IDs.IMDB, TMDB: it.IDs.TMDB, TVDB: it.IDs.TVDB,
			}})
		case "episode", "show":
			ids := simklIDs{Simkl: it.IDs.Simkl, IMDB: it.IDs.IMDB, TMDB: it.IDs.TMDB, TVDB: it.IDs.TVDB}
			if it.MediaType == "show" && it.Season == 0 && it.Episode == 0 {
				body.Shows = append(body.Shows, pushShow{IDs: ids})
				continue
			}
			body.Shows = append(body.Shows, pushShow{IDs: ids, Seasons: []pushSeason{{
				Number:   it.Season,
				Episodes: []pushEpisode{{Number: it.Episode, WatchedAt: at}},
			}}})
		}
	}
	raw, _ := json.Marshal(body)
	resp, err := doWithRetry(ctx, c.httpClient(), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/sync/history", bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		c.setHeaders(req)
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("simkl push: status %d", resp.StatusCode)
	}
	var v json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return fmt.Errorf("simkl push: malformed response: %w", err)
	}
	return nil
}
