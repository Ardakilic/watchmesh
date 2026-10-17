// Package ryot implements the Ryot connector (target-first).
//
// Ground truth (v10.5.3 sources, Oct 2026): everything is Bearer POST
// {base}/backend/graphql. Push resolves an internal metadata id per
// (lot, tmdb) via metadataSearch — which commits TMDB results server-side —
// strict-matches identifier == the tmdb number via metadataDetails, and
// deploys createNewCompleted progress updates in bulk. History lists the
// user's TMDB-sourced Movie/Show entries and reads each one's seen history.
// GraphQL enums upstream are SCREAMING_SNAKE_CASE (MOVIE, SHOW, TMDB) and
// list responses wrap results in a cache envelope:
// foo(input:…){ cacheId response { items … } }.
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

// pushChunk bounds [MetadataProgressUpdateInput!] items per bulk deploy
// mutation, so a big run stays a series of normal-sized requests.
const pushChunk = 100

// searchTake is the metadataSearch candidate window per (lot, tmdb).
const searchTake = 10

// historyPageTake and historyPageCap bound userMetadataList paging; running
// past the cap errors instead of silently truncating history.
const (
	historyPageTake = 200
	historyPageCap  = 100
)

// detailChunk bounds the ids per combined readDetails request, sizing the
// dynamically aliased query so it stays within normal request limits.
const detailChunk = 16

// doWithRetry runs build once, and on 429 waits Retry-After once then
// retries once.
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
	if ra := strings.TrimSpace(resp.Header.Get("Retry-After")); ra != "" {
		if n, err := strconv.Atoi(ra); err == nil && n >= 0 {
			secs = n
		} else if t, err := http.ParseTime(ra); err == nil {
			// ponytail: HTTP-date form; past dates fall to 0 (immediate retry)
			if d := time.Until(t); d > 0 {
				secs = int(d/time.Second) + 1
			} else {
				secs = 0
			}
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

// gqlReq is a GraphQL request body with query and variables.
type gqlReq struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// gqlErrors is a GraphQL errors[] list; first is "" when none.
type gqlErrors []struct {
	Message string `json:"message"`
}

// first returns the first error message or "" when the list is empty.
func (g gqlErrors) first() string {
	if len(g) == 0 {
		return ""
	}
	return g[0].Message
}

// gqlRaw POSTs one GraphQL body and decodes the envelope. Transport, HTTP
// status, and malformed-body failures error; field-level errors[] are
// RETURNED, because only the caller knows which partial misses are ok.
func (c *Client) gqlRaw(ctx context.Context, body []byte) (map[string]json.RawMessage, gqlErrors, error) {
	resp, err := doWithRetry(ctx, c.httpClient(), func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/backend/graphql", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("User-Agent", model.UserAgent)
		return req, nil
	})
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("ryot graphql: status %d", resp.StatusCode)
	}
	var gr struct {
		Data   map[string]json.RawMessage `json:"data"`
		Errors gqlErrors                  `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return nil, nil, fmt.Errorf("ryot graphql: malformed response: %w", err)
	}
	return gr.Data, gr.Errors, nil
}

// gql is gqlRaw under a no-errors rule for single-purpose calls.
func (c *Client) gql(ctx context.Context, body []byte) (map[string]json.RawMessage, error) {
	data, errs, err := c.gqlRaw(ctx, body)
	if err != nil {
		return nil, err
	}
	if m := errs.first(); m != "" {
		return nil, fmt.Errorf("ryot graphql: %s", m)
	}
	return data, nil
}

// pushLot maps an item to its Ryot media lot; ok=false skips TMDB==0 (all
// resolution runs through TMDB), bare "show" entries (TV progress needs
// season+episode numbers) and unknown kinds. Episodes resolve under the
// SHOW lot because their payload itself carries the numbering.
func pushLot(w model.WatchItem) (lot string, ok bool) {
	switch w.MediaType {
	case "movie":
		return "MOVIE", w.IDs.TMDB != 0
	case "episode":
		return "SHOW", w.IDs.TMDB != 0 && !(w.Season == 0 && w.Episode == 0)
	default:
		return "", false
	}
}

// finishedOn formats WatchedAt for the mutation; zero time becomes now.
func finishedOn(w model.WatchItem) string {
	t := w.WatchedAt
	if t.IsZero() {
		t = time.Now().UTC()
	}
	return t.UTC().Format(time.RFC3339)
}

// datedInput is a finishedOnDate body: RFC3339 timestamp plus optional
// episode numbers (nil for movies; pointers so real season/episode zeros
// are not dropped).
type datedInput struct {
	Timestamp         string `json:"timestamp"`
	ShowSeasonNumber  *int   `json:"showSeasonNumber,omitempty"`
	ShowEpisodeNumber *int   `json:"showEpisodeNumber,omitempty"`
}

// changeInput carries the oneof createNewCompleted variant.
type changeInput struct {
	CreateNewCompleted datedInput `json:"createNewCompleted"`
}

// progressInput is one MetadataProgressUpdateInput element.
type progressInput struct {
	MetadataID string      `json:"metadataId"`
	Change     changeInput `json:"change"`
}

// finishedDated builds the createNewCompleted body for one item; episode
// items ride season+episode numbers, movies stay bare.
func finishedDated(w model.WatchItem) datedInput {
	in := datedInput{Timestamp: finishedOn(w)}
	if w.MediaType == "episode" {
		s, e := w.Season, w.Episode
		in.ShowSeasonNumber, in.ShowEpisodeNumber = &s, &e
	}
	return in
}

// bucket keys one resolved search space: media lot + TMDB number.
type bucket struct {
	lot  string
	tmdb int
}

// resolve finds the internal metadata id whose details carry exactly the
// tmdb number with source TMDB. metadataSearch commits candidates
// server-side before returning ids, so the metadataDetails read sees them
// even on a cold instance; candidates scan in search order and the first
// strict identifier match wins (title/year look-alikes never match).
// Request errors return "", so the bucket's items merely skip this run.
func (c *Client) resolve(ctx context.Context, lot string, tmdb int, title string) (string, error) {
	body, _ := json.Marshal(gqlReq{
		Query: `query($i: MetadataSearchInput!) {
			metadataSearch(input: $i) {
				response { items details { totalItems nextPage } }
			}
		}`,
		Variables: map[string]any{"i": map[string]any{
			"lot":    lot,
			"source": "TMDB",
			"search": map[string]any{"query": title, "take": searchTake},
		}},
	})
	data, err := c.gql(ctx, body)
	if err != nil {
		return "", err
	}
	var sr struct {
		Response struct {
			Items []string `json:"items"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data["metadataSearch"], &sr); err != nil {
		return "", fmt.Errorf("ryot search %s/%d: malformed response: %w", lot, tmdb, err)
	}
	for _, id := range sr.Response.Items {
		dbody, _ := json.Marshal(gqlReq{
			Query: `query($i: String!) {
				metadataDetails(metadataId: $i) {
					response { identifier lot source }
				}
			}`,
			Variables: map[string]any{"i": id},
		})
		ddata, err := c.gql(ctx, dbody)
		if err != nil {
			return "", err
		}
		var md struct {
			Response struct {
				Identifier string `json:"identifier"`
				Source     string `json:"source"`
			} `json:"response"`
		}
		if err := json.Unmarshal(ddata["metadataDetails"], &md); err != nil {
			return "", fmt.Errorf("ryot details %s: malformed response: %w", id, err)
		}
		if md.Response.Source == "TMDB" &&
			md.Response.Identifier == strconv.Itoa(tmdb) {
			return id, nil
		}
	}
	return "", nil
}

// Push resolves each pushable item to Ryot's internal metadata id and
// deploys createNewCompleted progress updates in chunks. show-level items
// and TMDB==0 items are skipped (upstream TV progress needs season+episode
// numbers and everything resolves through TMDB), so they stay out of the
// delivered set and the engine never marks them seen. Buckets that fail
// resolution (search unavailable, no strict identifier match) also skip.
// Items deployed before a chunk failure are returned alongside the error.
//
// Bulk deploy is fire-and-forget server-side: a true response means the
// job was queued while per-item failures are swallowed upstream, so in the
// worst case an item is reported delivered yet not recorded — acceptable
// because the seen-hash means the next sync never re-pushes it.
func (c *Client) Push(ctx context.Context, items []model.WatchItem) ([]model.WatchItem, error) {
	// Collect the distinct (lot, tmdb) buckets to resolve, remembering the
	// first-seen title of each as the search query.
	titles := map[bucket]string{}
	order := []bucket{}
	for _, it := range items {
		lot, ok := pushLot(it)
		if !ok {
			continue
		}
		b := bucket{lot, it.IDs.TMDB}
		if _, seen := titles[b]; !seen {
			titles[b] = it.Title
			order = append(order, b)
		}
	}
	var delivered []model.WatchItem
	resolved := map[bucket]string{}
	for _, b := range order {
		// A resolve error OR an empty id (search found nothing/failed)
		// skips the bucket for this run; the engine holds the cursor.
		// A cancelled context is not a lookup failure: it aborts Push,
		// so a cancelled run never reads as a clean empty push.
		id, err := c.resolve(ctx, b.lot, b.tmdb, titles[b])
		if err != nil && ctx.Err() != nil {
			return delivered, ctx.Err()
		}
		if err == nil && id != "" {
			resolved[b] = id
		}
	}
	var inputs []progressInput
	var pushable []model.WatchItem
	for _, it := range items {
		lot, ok := pushLot(it)
		if !ok {
			continue
		}
		id, ok := resolved[bucket{lot, it.IDs.TMDB}]
		if !ok {
			continue
		}
		inputs = append(inputs, progressInput{MetadataID: id, Change: changeInput{
			CreateNewCompleted: finishedDated(it),
		}})
		pushable = append(pushable, it)
	}
	for start := 0; start < len(inputs); start += pushChunk {
		end := min(start+pushChunk, len(inputs))
		body, _ := json.Marshal(gqlReq{
			Query: `mutation($i: [MetadataProgressUpdateInput!]!) {
				deployBulkMetadataProgressUpdate(input: $i)
			}`,
			Variables: map[string]any{"i": inputs[start:end]},
		})
		data, err := c.gql(ctx, body)
		if err != nil {
			return delivered, err
		}
		var done bool
		if err := json.Unmarshal(data["deployBulkMetadataProgressUpdate"], &done); err != nil || !done {
			return delivered, fmt.Errorf("ryot push: deploy rejected")
		}
		delivered = append(delivered, pushable[start:end]...)
	}
	return delivered, nil
}

// History lists the user's TMDB-sourced Movie and Show entries and their
// seen history (paged userMetadataList, then combined per-id reads). Any
// failure is swallowed and yields empty,nil so history gaps never fail a
// sync — the connector stays target-first.
func (c *Client) History(ctx context.Context, since time.Time) ([]model.WatchItem, error) {
	items, err := c.history(ctx, since)
	if err != nil {
		return nil, nil
	}
	return items, nil
}

// history runs the paged userMetadataList walk. Non-TMDB identifiers and
// entries outside the movie/show lots never enter the result. Pagination
// stops at a nil nextPage; a full page cap is an error, not a silent cut.
func (c *Client) history(ctx context.Context, since time.Time) ([]model.WatchItem, error) {
	var out []model.WatchItem
	for _, lot := range []string{"MOVIE", "SHOW"} {
		for page := 1; ; page++ {
			ids, next, err := c.userListPage(ctx, lot, page)
			if err != nil {
				return nil, err
			}
			if len(ids) == 0 {
				break
			}
			items, err := c.readDetails(ctx, ids)
			if err != nil {
				return nil, err
			}
			out = append(out, items...)
			if next == nil {
				break
			}
			if page >= historyPageCap {
				return nil, fmt.Errorf("ryot history %s: exceeds %d pages, library truncated", lot, historyPageCap)
			}
		}
	}
	var fresh []model.WatchItem
	for _, it := range out {
		if !since.IsZero() && !it.WatchedAt.After(since) && !it.WatchedAt.Equal(since) {
			continue
		}
		fresh = append(fresh, it)
	}
	return fresh, nil
}

// userListPage fetches one userMetadataList page for lot filtered to the
// TMDB source and returns the internal metadata ids plus the next page
// (nil when paging has ended).
func (c *Client) userListPage(ctx context.Context, lot string, page int) ([]string, *int, error) {
	body, _ := json.Marshal(gqlReq{
		Query: fmt.Sprintf(`query($l: MediaLot!, $p: Int!) {
			userMetadataList(input: {lot: $l, filter: {source: TMDB}, search: {take: %d, page: $p}}) {
				response { items details { nextPage } }
			}
		}`, historyPageTake),
		Variables: map[string]any{"l": lot, "p": page},
	})
	data, err := c.gql(ctx, body)
	if err != nil {
		return nil, nil, err
	}
	var ls struct {
		Response struct {
			Items   []string `json:"items"`
			Details struct {
				NextPage *int `json:"nextPage"`
			} `json:"details"`
		} `json:"response"`
	}
	if err := json.Unmarshal(data["userMetadataList"], &ls); err != nil {
		return nil, nil, fmt.Errorf("ryot history %s: malformed response: %w", lot, err)
	}
	return ls.Response.Items, ls.Response.Details.NextPage, nil
}

// detailPart is one alias's metadataDetails response payload.
type detailPart struct {
	Response struct {
		Identifier string `json:"identifier"`
		Lot        string `json:"lot"`
		Source     string `json:"source"`
		Title      string `json:"title"`
	} `json:"response"`
}

// seenPart is one alias's userMetadataDetails response payload.
type seenPart struct {
	Response struct {
		History []historyEntry `json:"history"`
	} `json:"response"`
}

// historyEntry is one seen row; showExtraInformation binds TV entries to a
// season+episode, and only rows with a finishedOn timestamp count as watched.
type historyEntry struct {
	FinishedOn           string `json:"finishedOn"`
	ShowExtraInformation *struct {
		Season  int `json:"season"`
		Episode int `json:"episode"`
	} `json:"showExtraInformation"`
}

// readDetails reads metadataDetails + userMetadataDetails for ids in one
// dynamically aliased request per chunk and maps finished history entries to
// WatchItems. Per-id field errors (deleted metadata, foreign source) leave
// that id out of the result.
func (c *Client) readDetails(ctx context.Context, ids []string) ([]model.WatchItem, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	var out []model.WatchItem
	for start := 0; start < len(ids); start += detailChunk {
		end := min(start+detailChunk, len(ids))
		chunk := ids[start:end]
		var q strings.Builder
		q.WriteString("query(")
		variables := map[string]any{}
		for i, id := range chunk {
			fmt.Fprintf(&q, "$i%d: String!", i)
			if i < len(chunk)-1 {
				q.WriteString(", ")
			}
			variables[fmt.Sprintf("i%d", i)] = id
		}
		q.WriteString(") {")
		for i := range chunk {
			fmt.Fprintf(&q, `
	d%d: metadataDetails(metadataId: $i%d) { response { identifier lot source title } }
	u%d: userMetadataDetails(metadataId: $i%d) { response { history { finishedOn showExtraInformation { season episode } } } }`,
				i, i, i, i)
		}
		q.WriteString("\n}")
		body, _ := json.Marshal(gqlReq{Query: q.String(), Variables: variables})
		data, _, err := c.gqlRaw(ctx, body)
		if err != nil {
			return nil, err
		}
		for i := range chunk {
			var d detailPart
			if err := json.Unmarshal(data[fmt.Sprintf("d%d", i)], &d); err != nil {
				continue // alias errored out (unknown/deleted id)
			}
			r := d.Response
			tmdb, err := strconv.Atoi(r.Identifier)
			if err != nil || tmdb == 0 || r.Source != "TMDB" {
				continue
			}
			var u seenPart
			if err := json.Unmarshal(data[fmt.Sprintf("u%d", i)], &u); err != nil {
				continue
			}
			lot := "movie"
			if r.Lot == "SHOW" {
				lot = "show"
			}
			for _, h := range u.Response.History {
				if h.FinishedOn == "" {
					continue
				}
				watchedAt, err := time.Parse(time.RFC3339, h.FinishedOn)
				if err != nil {
					continue
				}
				it := model.WatchItem{
					IDs:       model.IDs{TMDB: tmdb},
					MediaType: lot,
					Title:     r.Title,
					WatchedAt: watchedAt,
				}
				if lot == "show" {
					if h.ShowExtraInformation == nil {
						continue
					}
					it.MediaType, it.Season, it.Episode = "episode",
						h.ShowExtraInformation.Season, h.ShowExtraInformation.Episode
				}
				out = append(out, it)
			}
		}
	}
	return out, nil
}

// ValidateToken POSTs a userDetails probe: success is a 200 without GraphQL
// errors whose data userDetails carries __typename UserDetails (a union —
// the object member type name, not the variant). Upstream rejects requests
// before the probe with GraphQL errors, which gql surfaces.
func (c *Client) ValidateToken(ctx context.Context) error {
	data, err := c.gql(ctx, []byte(`{userDetails{__typename ... on UserDetails{id}}}`))
	if err != nil {
		return err
	}
	var t struct {
		Typename string `json:"__typename"`
	}
	if err := json.Unmarshal(data["userDetails"], &t); err != nil {
		return fmt.Errorf("ryot auth: malformed response: %w", err)
	}
	if t.Typename != "UserDetails" {
		return fmt.Errorf("ryot auth: unexpected %s", t.Typename)
	}
	return nil
}
