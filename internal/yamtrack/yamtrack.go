// Package yamtrack implements the Yamtrack connector as a webhook producer.
//
// Ground truth (upstream `dev` branch, Oct 2026): the only history-capable
// intake is the Jellyfin-compatible webhook `POST {base}/webhook/jellyfin/
// {token}` (@require_POST, @csrf_exempt; unknown token -> 401, empty body ->
// 400 "Missing payload", valid payload -> 200). No released REST history API
// exists, so History is a no-op and the webhook doubles as the auth probe.
package yamtrack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/watchmesh/watchmesh/internal/model"
)

// Client pushes to Yamtrack via its Jellyfin-compatible webhook endpoint.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client // nil => http.DefaultClient
}

// New builds a Client for a Yamtrack base URL and integration token.
func New(baseURL, token string) *Client {
	return &Client{BaseURL: strings.TrimSuffix(strings.TrimSpace(baseURL), "/"), Token: token}
}

// Name implements model.Target/model.Source.
func (c *Client) Name() string { return "yamtrack" }

// httpClient returns the injected client, or http.DefaultClient when nil.
func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// webhookEvent is the Jellyfin stop-event body Yamtrack's
// JellyfinWebhookProcessor accepts: a stopped item whose UserData.Played
// flag marks it complete, plus the Series sidecar for episodes. Field key
// casing (Event/Item/ProviderIds/...) is the upstream contract, not Go-ism.
type webhookEvent struct {
	Event  string     `json:"Event"`
	Item   webItem    `json:"Item"`
	Series *webSeries `json:"Series,omitempty"`
}

// webItem mirrors the "Item" block; provider ids are strings because
// upstream stores media ids as text. Movies send Type+ProviderIds only;
// episodes additionally send their numbering and the Series block.
type webItem struct {
	Type              string            `json:"Type"`
	ProviderIds       map[string]string `json:"ProviderIds,omitempty"`
	UserData          map[string]any    `json:"UserData,omitempty"`
	SeriesName        string            `json:"SeriesName,omitempty"`
	ParentIndexNumber *int              `json:"ParentIndexNumber,omitempty"`
	IndexNumber       *int              `json:"IndexNumber,omitempty"`
}

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

// webhookPayload builds the stop-event webhook for one item; ok=false skips
// shapes the intake cannot resolve. Movies need the TMDB id; episodes need
// numbering plus the series block. Episodes never carry Item-level Tvdb:
// upstream treats Item.ProviderIds["Tvdb"] as a TVDB *episode* id and would
// resolve an unrelated record, whereas the series TMDB id and the numbering
// pin the episode exactly. Historical watched_at values are
// unrepresentable — upstream stamps every webhook with now.
func webhookPayload(w model.WatchItem) (webhookEvent, bool) {
	switch w.MediaType {
	case "movie":
		if w.IDs.TMDB == 0 {
			return webhookEvent{}, false
		}
		ids := map[string]string{"Tmdb": strconv.Itoa(w.IDs.TMDB)}
		if w.IDs.IMDB != "" {
			ids["Imdb"] = w.IDs.IMDB
		}
		return webhookEvent{Event: "Stop", Item: webItem{
			Type:        "Movie",
			ProviderIds: ids,
			UserData:    map[string]any{"Played": true},
		}}, true
	case "episode":
		if w.IDs.TMDB == 0 || (w.Season == 0 && w.Episode == 0) {
			return webhookEvent{}, false
		}
		item := webItem{
			Type:              "Episode",
			UserData:          map[string]any{"Played": true},
			ParentIndexNumber: &w.Season,
			IndexNumber:       &w.Episode,
		}
		if w.Title != "" {
			item.SeriesName = w.Title
		}
		if w.IDs.IMDB != "" {
			item.ProviderIds = map[string]string{"Imdb": w.IDs.IMDB}
		}
		return webhookEvent{Event: "Stop", Item: item, Series: &webSeries{
			ProviderIds: map[string]string{"Tmdb": strconv.Itoa(w.IDs.TMDB)},
		}}, true
	default:
		// Bare "show" items and unknown kinds: the upstream TV path tracks
		// one episode at a time, so there is no payload for a show level.
		return webhookEvent{}, false
	}
}

// webSeries mirrors the "Series" block episodes carry so upstream can pin
// the show by TMDB id.
type webSeries struct {
	ProviderIds map[string]string `json:"ProviderIds,omitempty"`
}

// Push loops one webhook POST per item — upstream has no batch endpoint —
// and returns the items actually delivered, so the engine marks seen only
// what reached the service. Items delivered before a mid-loop failure are
// returned alongside the error.
func (c *Client) Push(ctx context.Context, items []model.WatchItem) ([]model.WatchItem, error) {
	var delivered []model.WatchItem
	for _, it := range items {
		payload, ok := webhookPayload(it)
		if !ok {
			continue
		}
		body, _ := json.Marshal(payload)
		if err := c.pushOne(ctx, body); err != nil {
			return delivered, err
		}
		delivered = append(delivered, it)
	}
	return delivered, nil
}

// pushOne POSTs one webhook payload to {base}/webhook/jellyfin/{token}.
// 200 means processed (even when upstream ignored the event, e.g. unsupported
// Item.Type); 401 means the token is invalid and 400 a malformed payload.
func (c *Client) pushOne(ctx context.Context, body []byte) error {
	resp, err := doWithRetry(ctx, c.httpClient(), func() (*http.Request, error) {
		u := c.BaseURL + "/webhook/jellyfin/" + url.PathEscape(c.Token)
		req, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", model.UserAgent)
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	return fmt.Errorf("yamtrack push: status %d", resp.StatusCode)
}

// History is a no-op: Yamtrack exposes no released REST history endpoint,
// and webhook-ingested entries cannot be listed back per integration token.
// Returning empty,nil keeps read gaps non-fatal; state stays one-way there.
func (c *Client) History(ctx context.Context, since time.Time) ([]model.WatchItem, error) {
	return nil, nil
}

// ValidateToken probes the webhook endpoint with an empty body: upstream
// authenticates the token in the URL path before parsing the payload, so a
// valid token answers 400 ("Missing payload") and an invalid one 401.
func (c *Client) ValidateToken(ctx context.Context) error {
	u := c.BaseURL + "/webhook/jellyfin/" + url.PathEscape(c.Token)
	req, err := http.NewRequestWithContext(ctx, "POST", u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", model.UserAgent)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusBadRequest:
		// Token accepted; the payload is legitimately missing.
		return nil
	case http.StatusUnauthorized:
		return fmt.Errorf("yamtrack auth: token rejected (401)")
	default:
		return fmt.Errorf("yamtrack auth: status %d", resp.StatusCode)
	}
}
