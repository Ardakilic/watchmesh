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

// httpClient returns the injected client, or http.DefaultClient when nil.
func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// setHeaders sets Trakt auth, version, and JSON headers on r.
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

// traktIDs is the Trakt ids object shared by movie/episode/show entries.
type traktIDs struct {
	Trakt int    `json:"trakt,omitempty"`
	Slug  string `json:"slug,omitempty"`
	IMDB  string `json:"imdb,omitempty"`
	TMDB  int    `json:"tmdb,omitempty"`
	TVDB  int    `json:"tvdb,omitempty"`
}

// notFoundEntry is one unresolved element in a push response.
type notFoundEntry struct {
	IDs traktIDs `json:"ids"`
}

// movieEntry is one /sync/history/movies element.
type movieEntry struct {
	WatchedAt string `json:"watched_at"`
	Movie     struct {
		Title string   `json:"title"`
		Year  int      `json:"year"`
		IDs   traktIDs `json:"ids"`
	} `json:"movie"`
}

// episodeEntry is one /sync/history/episodes element with its show.
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

// parseTime parses Trakt timestamps; empty/unparseable yields zero time.
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

// fetchKind pages one history kind until an empty page. The 100-page cap is
// a loud limit, not a silent truncation: when page 100 is full, one probe
// page is fetched and a limit error returns if history continues past it.
// ponytail: page cap, raise if a library ever exceeds 10k history entries.
func (c *Client) fetchKind(ctx context.Context, kind string, since time.Time) ([]model.WatchItem, error) {
	var out []model.WatchItem
	const maxPages = 100
	for page := 1; page <= maxPages+1; page++ {
		u := fmt.Sprintf("%s/sync/history/%s?page=%d&limit=100", c.BaseURL, kind, page)
		if !since.IsZero() {
			u += "&start_at=" + since.UTC().Format(time.RFC3339)
		}
		entries, empty, err := c.fetchPage(ctx, kind, u)
		if err != nil {
			return nil, err
		}
		if empty {
			break
		}
		if page > maxPages {
			return nil, fmt.Errorf("trakt history %s: exceeds %d pages, history truncated", kind, maxPages)
		}
		out = append(out, entries...)
	}
	return out, nil
}

// fetchPage GETs one history page; empty reports a terminal empty page.
func (c *Client) fetchPage(ctx context.Context, kind, u string) (entries []model.WatchItem, empty bool, err error) {
	resp, err := doWithRetry(ctx, c.httpClient(), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
		if err != nil {
			return nil, err
		}
		c.setHeaders(req)
		return req, nil
	})
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("trakt history %s: status %d", kind, resp.StatusCode)
	}
	if kind == "movies" {
		var list []movieEntry
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			return nil, false, fmt.Errorf("trakt history %s: malformed response: %w", kind, err)
		}
		if len(list) == 0 {
			return nil, true, nil
		}
		for _, e := range list {
			entries = append(entries, model.WatchItem{
				IDs:       model.IDs{Trakt: e.Movie.IDs.Trakt, IMDB: e.Movie.IDs.IMDB, TMDB: e.Movie.IDs.TMDB, TVDB: e.Movie.IDs.TVDB},
				MediaType: "movie",
				Title:     e.Movie.Title,
				Year:      e.Movie.Year,
				WatchedAt: parseTime(e.WatchedAt),
			})
		}
		return entries, false, nil
	}
	var list []episodeEntry
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, false, fmt.Errorf("trakt history %s: malformed response: %w", kind, err)
	}
	if len(list) == 0 {
		return nil, true, nil
	}
	for _, e := range list {
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
		entries = append(entries, model.WatchItem{
			IDs:       ids,
			MediaType: "episode",
			Title:     e.Show.Title,
			Year:      e.Show.Year,
			Season:    e.Episode.Season,
			Episode:   e.Episode.Number,
			WatchedAt: parseTime(e.WatchedAt),
		})
	}
	return entries, false, nil
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

// pushIDs is the ids object in a /sync/history POST entry.
type pushIDs struct {
	Trakt int    `json:"trakt,omitempty"`
	IMDB  string `json:"imdb,omitempty"`
	TMDB  int    `json:"tmdb,omitempty"`
	TVDB  int    `json:"tvdb,omitempty"`
}

// pushEntry is one movies/episodes element in a /sync/history POST.
type pushEntry struct {
	WatchedAt string  `json:"watched_at"`
	IDs       pushIDs `json:"ids"`
}

// pushID converts model IDs to the push payload ids shape.
func pushID(ids model.IDs) pushIDs {
	return pushIDs{Trakt: ids.Trakt, IMDB: ids.IMDB, TMDB: ids.TMDB, TVDB: ids.TVDB}
}

// Push POSTs /sync/history grouping movies/episodes per design §7 and
// returns the items actually delivered. Media types other than
// movie/episode and items with zero WatchedAt are skipped (stamping
// time.Now() would fabricate history) and omitted from delivered, as are
// items the service reports under not_found (unresolved IDs), so the
// engine never marks them seen.
func (c *Client) Push(ctx context.Context, items []model.WatchItem) ([]model.WatchItem, error) {
	body := struct {
		Movies   []pushEntry `json:"movies"`
		Episodes []pushEntry `json:"episodes"`
	}{Movies: []pushEntry{}, Episodes: []pushEntry{}}
	var delivered []model.WatchItem
	for _, it := range items {
		if it.WatchedAt.IsZero() {
			continue
		}
		at := it.WatchedAt.UTC().Format(time.RFC3339)
		switch it.MediaType {
		case "movie":
			body.Movies = append(body.Movies, pushEntry{WatchedAt: at, IDs: pushID(it.IDs)})
			delivered = append(delivered, it)
		case "episode":
			body.Episodes = append(body.Episodes, pushEntry{WatchedAt: at, IDs: pushID(it.IDs)})
			delivered = append(delivered, it)
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
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("trakt push: status %d", resp.StatusCode)
	}
	var pr struct {
		NotFound struct {
			Movies   []notFoundEntry `json:"movies"`
			Episodes []notFoundEntry `json:"episodes"`
		} `json:"not_found"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, fmt.Errorf("trakt push: malformed response: %w", err)
	}
	var moviesNF, episodesNF []traktIDs
	for _, e := range pr.NotFound.Movies {
		moviesNF = append(moviesNF, e.IDs)
	}
	for _, e := range pr.NotFound.Episodes {
		episodesNF = append(episodesNF, e.IDs)
	}
	var kept []model.WatchItem
	for _, it := range delivered {
		// Match within the item's own category only: ID spaces (notably
		// TMDB movie vs TV) collide across types, so a not_found episode
		// must never drop a delivered movie.
		list := episodesNF
		if it.MediaType == "movie" {
			list = moviesNF
		}
		if !unresolved(it.IDs, list) {
			kept = append(kept, it)
		}
	}
	return kept, nil
}

// unresolved reports whether ids match a not_found entry on any shared
// non-zero identifier; absent not_found lists match nothing.
func unresolved(ids model.IDs, nfs []traktIDs) bool {
	for _, nf := range nfs {
		if nf.Trakt != 0 && nf.Trakt == ids.Trakt {
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
