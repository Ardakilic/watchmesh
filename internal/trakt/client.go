package trakt

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

// Client talks to Trakt history endpoints.
type Client struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
	Token        string
	HTTP         *http.Client // nil => http.DefaultClient
}

// New builds a Client; empty baseURL means DefaultBaseURL.
func New(baseURL, clientID, clientSecret, token string) *Client {
	baseURL = strings.TrimSuffix(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{BaseURL: baseURL, ClientID: clientID, ClientSecret: clientSecret, Token: token}
}

// Name implements model Source/Target naming.
func (c *Client) Name() string { return "trakt" }

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func (c *Client) setHeaders(r *http.Request) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	r.Header.Set("trakt-api-key", c.ClientID)
	if c.Token != "" {
		r.Header.Set("Authorization", "Bearer "+c.Token)
	}
	r.Header.Set("trakt-api-version", "2")
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

type traktIDs struct {
	Trakt int    `json:"trakt,omitempty"`
	Slug  string `json:"slug,omitempty"`
	IMDB  string `json:"imdb,omitempty"`
	TMDB  int    `json:"tmdb,omitempty"`
	TVDB  int    `json:"tvdb,omitempty"`
}

type movieEntry struct {
	WatchedAt string `json:"watched_at"`
	Movie     struct {
		Title string   `json:"title"`
		Year  int      `json:"year"`
		IDs   traktIDs `json:"ids"`
	} `json:"movie"`
}

type episodeEntry struct {
	WatchedAt string `json:"watched_at"`
	Episode   struct {
		Season int      `json:"season"`
		Number int      `json:"number"`
		IDs    traktIDs `json:"ids"`
	} `json:"episode"`
	Show struct {
		Title string   `json:"title"`
		Year  int      `json:"year"`
		IDs   traktIDs `json:"ids"`
	} `json:"show"`
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func (c *Client) fetchKind(ctx context.Context, kind string, since time.Time) ([]model.WatchItem, error) {
	var out []model.WatchItem
	for page := 1; ; page++ {
		u := fmt.Sprintf("%s/sync/history/%s?page=%d&limit=100", c.BaseURL, kind, page)
		if !since.IsZero() {
			u += "&start_at=" + since.UTC().Format(time.RFC3339)
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
			return nil, fmt.Errorf("trakt history %s: status %d", kind, resp.StatusCode)
		}
		if kind == "movies" {
			var entries []movieEntry
			if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
				return nil, fmt.Errorf("trakt history %s: malformed response: %w", kind, err)
			}
			if len(entries) == 0 {
				break
			}
			for _, e := range entries {
				out = append(out, model.WatchItem{
					IDs:       model.IDs{Trakt: e.Movie.IDs.Trakt, IMDB: e.Movie.IDs.IMDB, TMDB: e.Movie.IDs.TMDB, TVDB: e.Movie.IDs.TVDB},
					MediaType: "movie",
					Title:     e.Movie.Title,
					Year:      e.Movie.Year,
					WatchedAt: parseTime(e.WatchedAt),
				})
			}
		} else {
			var entries []episodeEntry
			if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
				return nil, fmt.Errorf("trakt history %s: malformed response: %w", kind, err)
			}
			if len(entries) == 0 {
				break
			}
			for _, e := range entries {
				ids := model.IDs{Trakt: e.Episode.IDs.Trakt, TMDB: e.Episode.IDs.TMDB, TVDB: e.Episode.IDs.TVDB}
				if e.Episode.IDs.IMDB != "" {
					ids.IMDB = e.Episode.IDs.IMDB
				} else {
					ids.IMDB = e.Show.IDs.IMDB
				}
				if ids.TMDB == 0 {
					ids.TMDB = e.Show.IDs.TMDB
				}
				if ids.TVDB == 0 {
					ids.TVDB = e.Show.IDs.TVDB
				}
				title := e.Show.Title
				year := e.Show.Year
				out = append(out, model.WatchItem{
					IDs:       ids,
					MediaType: "episode",
					Title:     title,
					Year:      year,
					Season:    e.Episode.Season,
					Episode:   e.Episode.Number,
					WatchedAt: parseTime(e.WatchedAt),
				})
			}
		}
	}
	return out, nil
}

// History GETs /sync/history/movies + /episodes with paging until empty.
// WatchedAt is always mapped, never dropped.
func (c *Client) History(ctx context.Context, since time.Time) ([]model.WatchItem, error) {
	movies, err := c.fetchKind(ctx, "movies", since)
	if err != nil {
		return nil, err
	}
	episodes, err := c.fetchKind(ctx, "episodes", since)
	if err != nil {
		return nil, err
	}
	return append(movies, episodes...), nil
}

type pushIDs struct {
	Trakt int    `json:"trakt,omitempty"`
	IMDB  string `json:"imdb,omitempty"`
	TMDB  int    `json:"tmdb,omitempty"`
	TVDB  int    `json:"tvdb,omitempty"`
}

type pushEntry struct {
	WatchedAt string  `json:"watched_at"`
	IDs       pushIDs `json:"ids"`
}

func pushID(ids model.IDs) pushIDs {
	return pushIDs{Trakt: ids.Trakt, IMDB: ids.IMDB, TMDB: ids.TMDB, TVDB: ids.TVDB}
}

// Push POSTs /sync/history grouping movies/episodes per design §7.
// Media types other than movie/episode are skipped (no Trakt shape).
func (c *Client) Push(ctx context.Context, items []model.WatchItem) error {
	body := struct {
		Movies   []pushEntry `json:"movies"`
		Episodes []pushEntry `json:"episodes"`
	}{Movies: []pushEntry{}, Episodes: []pushEntry{}}
	for _, it := range items {
		at := it.WatchedAt.UTC().Format(time.RFC3339)
		if it.WatchedAt.IsZero() {
			at = time.Now().UTC().Format(time.RFC3339)
		}
		switch it.MediaType {
		case "movie":
			body.Movies = append(body.Movies, pushEntry{WatchedAt: at, IDs: pushID(it.IDs)})
		case "episode":
			body.Episodes = append(body.Episodes, pushEntry{WatchedAt: at, IDs: pushID(it.IDs)})
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
		return fmt.Errorf("trakt push: status %d", resp.StatusCode)
	}
	var v json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return fmt.Errorf("trakt push: malformed response: %w", err)
	}
	return nil
}
