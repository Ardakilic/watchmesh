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

// httpClient returns the injected client, or http.DefaultClient when nil.
func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// setHeaders sets Simkl API key, Bearer, and JSON headers on r.
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

// throttle enforces minimum spacing d since last, updating last under mutex.
func (c *Client) throttle(last *time.Time, d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !last.IsZero() {
		if s := d - time.Since(*last); s > 0 {
			time.Sleep(s)
		}
	}
	*last = time.Now()
}

// throttleGET enforces 10 GET/s via 100ms spacing.
func (c *Client) throttleGET() { c.throttle(&c.lastGET, 100*time.Millisecond) }

// throttlePOST enforces 1 POST/s.
// ponytail: sleep throttle, token-bucket if limits bite.
func (c *Client) throttlePOST() { c.throttle(&c.lastPOST, time.Second) }

// parseTime parses Simkl timestamps; empty/unparseable yields zero time.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// activityCursor is the per-category dirty-check timestamp from /sync/activities.
type activityCursor struct {
	WatchedAt string `json:"watched_at"`
}

// activities GETs /sync/activities for the per-category dirty-check cursors.
func (c *Client) activities(ctx context.Context) (map[string]activityCursor, error) {
	resp, err := doWithRetry(ctx, c.httpClient(), func() (*http.Request, error) {
		c.throttleGET()
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

// simklIDs is the Simkl ids object shared by movie/show/push items.
type simklIDs struct {
	Simkl int    `json:"simkl,omitempty"`
	IMDB  string `json:"imdb,omitempty"`
	TMDB  int    `json:"tmdb,omitempty"`
	TVDB  int    `json:"tvdb,omitempty"`
}

// notFoundEntry is one unresolved element in a push response.
type notFoundEntry struct {
	IDs simklIDs `json:"ids"`
}

// nestedMedia is the inner movie/show object in an all-items entry.
type nestedMedia struct {
	Title string   `json:"title"`
	Year  int      `json:"year"`
	IDs   simklIDs `json:"ids"`
}

// allEpisode is one episode with its watched timestamp.
type allEpisode struct {
	Number    int    `json:"number"`
	WatchedAt string `json:"watched_at"`
}

// allSeason groups allEpisodes under one season number.
type allSeason struct {
	Number   int          `json:"number"`
	Episodes []allEpisode `json:"episodes"`
}

// allItemsEntry is one nested all-items entry: top-level status and
// last_watched_at plus the inner movie/show object and seasons.
type allItemsEntry struct {
	Status        string      `json:"status"`
	LastWatchedAt string      `json:"last_watched_at"`
	Movie         nestedMedia `json:"movie"`
	Show          nestedMedia `json:"show"`
	Seasons       []allSeason `json:"seasons"`
}

// toIDs converts Simkl ids to model IDs.
func toIDs(s simklIDs) model.IDs {
	return model.IDs{Simkl: s.Simkl, IMDB: s.IMDB, TMDB: s.TMDB, TVDB: s.TVDB}
}

// allItemsURL builds the /sync/all-items/{cat}/{bucket} URL with the
// extended params needed for real per-episode dates and optional date_from.
func (c *Client) allItemsURL(cat, bucket string, since time.Time) string {
	u := c.BaseURL + "/sync/all-items/" + cat + "/" + bucket + "?extended=full&episode_watched_at=yes&include_all_episodes=original"
	if !since.IsZero() {
		u += "&date_from=" + since.UTC().Format(time.RFC3339)
	}
	return u
}

// fetchCategory GETs completed+watching buckets for cat and emits only
// actually-watched entries: movies with non-empty last_watched_at,
// episodes with non-empty episode watched_at. Everything else
// (plan-to-watch, dropped, never-watched, zero timestamps) is dropped.
// Entries older than since are filtered.
func (c *Client) fetchCategory(ctx context.Context, cat string, since time.Time) ([]model.WatchItem, error) {
	var out []model.WatchItem
	for _, bucket := range []string{"completed", "watching"} {
		u := c.allItemsURL(cat, bucket, since)
		resp, err := doWithRetry(ctx, c.httpClient(), func() (*http.Request, error) {
			c.throttleGET()
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
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("simkl all-items %s: status %d", cat, resp.StatusCode)
		}
		var v map[string][]allItemsEntry
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("simkl all-items %s: malformed response: %w", cat, err)
		}
		resp.Body.Close()
		list := v[cat]
		if cat == "movies" {
			for _, e := range list {
				at := parseTime(e.LastWatchedAt)
				if at.IsZero() {
					continue
				}
				if !since.IsZero() && at.Before(since) {
					continue
				}
				m := e.Movie
				out = append(out, model.WatchItem{
					IDs: toIDs(m.IDs), MediaType: "movie",
					Title: m.Title, Year: m.Year, WatchedAt: at,
				})
			}
			continue
		}
		for _, e := range list {
			s := e.Show
			if s.Title == "" && e.Movie.Title != "" {
				s = e.Movie
			}
			for _, sn := range e.Seasons {
				for _, ep := range sn.Episodes {
					at := parseTime(ep.WatchedAt)
					if at.IsZero() {
						continue
					}
					if !since.IsZero() && at.Before(since) {
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
	var out []model.WatchItem
	for _, cc := range []struct{ cat, actKey string }{
		{"movies", "movies"}, {"shows", "tv_shows"}, {"anime", "anime"},
	} {
		if !since.IsZero() {
			if t := parseTime(acts[cc.actKey].WatchedAt); !t.IsZero() && !t.After(since) {
				continue
			}
		}
		items, err := c.fetchCategory(ctx, cc.cat, since)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

// pushMovie is one movies element in a /sync/history POST.
type pushMovie struct {
	WatchedAt string   `json:"watched_at"`
	IDs       simklIDs `json:"ids"`
}

// pushEpisode is one episode element inside a pushSeason.
type pushEpisode struct {
	Number    int    `json:"number"`
	WatchedAt string `json:"watched_at"`
}

// pushSeason groups pushEpisodes under one season number.
type pushSeason struct {
	Number   int           `json:"number"`
	Episodes []pushEpisode `json:"episodes"`
}

// pushShow is one shows element in a /sync/history POST.
type pushShow struct {
	IDs     simklIDs     `json:"ids"`
	Seasons []pushSeason `json:"seasons,omitempty"`
}

// Push POSTs /sync/history per design §7 at 1 POST/s and returns the items
// actually delivered; unknown media types and items the service reports
// under not_found (unresolved IDs) are omitted from delivered so the
// engine never marks them seen. An absent not_found section leaves
// delivered unchanged.
func (c *Client) Push(ctx context.Context, items []model.WatchItem) ([]model.WatchItem, error) {
	body := struct {
		Movies []pushMovie `json:"movies"`
		Shows  []pushShow  `json:"shows"`
	}{Movies: []pushMovie{}, Shows: []pushShow{}}
	var delivered []model.WatchItem
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
			delivered = append(delivered, it)
		case "episode", "show":
			ids := simklIDs{Simkl: it.IDs.Simkl, IMDB: it.IDs.IMDB, TMDB: it.IDs.TMDB, TVDB: it.IDs.TVDB}
			if it.MediaType == "show" && it.Season == 0 && it.Episode == 0 {
				body.Shows = append(body.Shows, pushShow{IDs: ids})
				delivered = append(delivered, it)
				continue
			}
			body.Shows = append(body.Shows, pushShow{IDs: ids, Seasons: []pushSeason{{
				Number:   it.Season,
				Episodes: []pushEpisode{{Number: it.Episode, WatchedAt: at}},
			}}})
			delivered = append(delivered, it)
		}
	}
	raw, _ := json.Marshal(body)
	resp, err := doWithRetry(ctx, c.httpClient(), func() (*http.Request, error) {
		c.throttlePOST()
		req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/sync/history", bytes.NewReader(raw))
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
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("simkl push: status %d", resp.StatusCode)
	}
	var pr struct {
		NotFound struct {
			Movies   []notFoundEntry `json:"movies"`
			Shows    []notFoundEntry `json:"shows"`
			Episodes []notFoundEntry `json:"episodes"`
		} `json:"not_found"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("simkl push: malformed response: %w", err)
	}
	var kept []model.WatchItem
	for _, it := range delivered {
		// Match within the item's own category only: ID spaces (notably
		// TMDB movie vs TV) collide across types, so a not_found episode
		// must never drop a delivered movie.
		var list []notFoundEntry
		switch it.MediaType {
		case "movie":
			list = pr.NotFound.Movies
		case "show":
			list = pr.NotFound.Shows
		default:
			list = pr.NotFound.Episodes
		}
		if !unresolved(it.IDs, list) {
			kept = append(kept, it)
		}
	}
	return kept, nil
}

// unresolved reports whether ids match a not_found entry on any shared
// non-zero identifier; absent not_found lists match nothing.
func unresolved(ids model.IDs, nfs []notFoundEntry) bool {
	for _, e := range nfs {
		nf := e.IDs
		if nf.Simkl != 0 && nf.Simkl == ids.Simkl {
			return true
		}
		if nf.IMDB != "" && nf.IMDB == ids.IMDB {
			return true
		}
		if nf.TMDB != 0 && nf.TMDB == ids.TMDB {
			return true
		}
		if nf.TVDB != 0 && nf.TVDB == ids.TVDB {
			return true
		}
	}
	return false
}
