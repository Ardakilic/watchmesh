package ryot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/watchmesh/watchmesh/internal/model"
)

// item builds a movie WatchItem with a TMDB ID and fixed timestamp.
func item(tmdb int) model.WatchItem {
	return model.WatchItem{IDs: model.IDs{TMDB: tmdb}, MediaType: "movie",
		Title: "T", WatchedAt: time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)}
}

// epItem builds a numbered episode WatchItem for a show-level TMDB id.
func epItem(tmdb, season, episode int) model.WatchItem {
	it := item(tmdb)
	it.MediaType, it.Season, it.Episode = "episode", season, episode
	return it
}

// gresp builds a 200 GraphQL success envelope carrying payload under field.
func gresp(field, payload string) string {
	return fmt.Sprintf(`{"data":{"%s":%s}}`, field, payload)
}

// searchResp is a metadataSearch payload listing internal metadata ids.
func searchResp(ids ...string) string {
	b, _ := json.Marshal(ids)
	return fmt.Sprintf(`{"response":{"items":%s,"details":{"totalItems":%d,"nextPage":null}}}`, b, len(ids))
}

// detailsResp is a metadataDetails.response object.
func detailsResp(ident, lot, source string) string {
	return fmt.Sprintf(`{"response":{"identifier":%q,"lot":%q,"source":%q,"title":"T"}}`, ident, lot, source)
}

// seenResp is a userMetadataDetails payload wrapping finished history rows.
func seenResp(rows ...string) string {
	return fmt.Sprintf(`{"response":{"history":[%s]}}`, strings.Join(rows, ","))
}

// seenRow is one seen entry; showExtraInformation fields are included when
// at least one is non-negative.
func seenRow(finishedOn string, season, episode int) string {
	if season < 0 {
		return fmt.Sprintf(`{"finishedOn":%q}`, finishedOn)
	}
	return fmt.Sprintf(`{"finishedOn":%q,"showExtraInformation":{"season":%d,"episode":%d}}`,
		finishedOn, season, episode)
}

// listPage is one scripted userMetadataList page.
type listPage struct {
	ids  []string
	next *int
}

// nextP returns a pointer to n, for scripting nextPage values.
func nextP(n int) *int { return &n }

// listResp is a userMetadataList payload; nil next ends paging.
func listResp(ids []string, next *int) string {
	b, _ := json.Marshal(ids)
	nx := "null"
	if next != nil {
		nx = strconv.Itoa(*next)
	}
	return fmt.Sprintf(`{"response":{"items":%s,"details":{"nextPage":%s}}}`, b, nx)
}

// gqlTape records GraphQL requests (decoded) plus per-kind hit counts.
// Requests arrive strictly sequentially, so no locking is needed.
type gqlTape struct {
	search, details, deploy, list, read int
	reqs                                []gqlReq
}

// gqlBody decodes one GraphQL request body.
func gqlBody(r *http.Request) gqlReq {
	var b gqlReq
	_ = json.NewDecoder(r.Body).Decode(&b)
	return b
}

// deployCall scripts one deployBulkMetadataProgressUpdate response.
type deployCall func(n int) (status int, body string)

// deployOK answers every deploy with the bare success bool.
func deployOK() deployCall {
	return func(int) (int, string) { return 200, gresp("deployBulkMetadataProgressUpdate", "true") }
}

// pushRouter spins a fake Ryot backend for Push scenarios. metadataSearch
// lists searchIDs; each candidate id's metadataDetails comes from details
// (an admitted-but-unmatched id means search-then-miss); deploy(n) scripts
// the nth bulk-deploy response. All requests land on the tape.
func pushRouter(details map[string]string, searchIDs []string, deploy deployCall) (*httptest.Server, *gqlTape) {
	tp := &gqlTape{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := gqlBody(r)
		tp.reqs = append(tp.reqs, req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "metadataSearch"):
			tp.search++
			fmt.Fprint(w, gresp("metadataSearch", searchResp(searchIDs...)))
		case strings.Contains(req.Query, "metadataDetails"):
			tp.details++
			id, _ := req.Variables["i"].(string)
			resp, ok := details[id]
			if !ok {
				resp = detailsResp("", "", "") // identifier "" never matches
			}
			fmt.Fprint(w, gresp("metadataDetails", resp))
		case strings.Contains(req.Query, "deployBulkMetadataProgressUpdate"):
			tp.deploy++
			status, body := deploy(tp.deploy)
			w.WriteHeader(status)
			fmt.Fprint(w, body)
		default:
			w.WriteHeader(500)
		}
	}))
	return srv, tp
}

// historyRouter spins a fake for History scenarios: pages keys scripted
// userMetadataList pages per lot (out-of-range pages are empty); each id's
// metadataDetails/userMetadataDetails come from the maps — an id missing
// there yields alias nulls, the shape of a failed read upstream.
func historyRouter(pages map[string][]listPage, details, seen map[string]string) (*httptest.Server, *gqlTape) {
	tp := &gqlTape{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := gqlBody(r)
		tp.reqs = append(tp.reqs, req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(req.Query, "userMetadataList"):
			tp.list++
			lot, _ := req.Variables["l"].(string)
			p, _ := req.Variables["p"].(float64)
			idx := int(p) - 1
			pages := pages[lot]
			if idx < 0 || idx >= len(pages) {
				fmt.Fprint(w, gresp("userMetadataList", listResp(nil, nil)))
				return
			}
			lp := pages[idx]
			fmt.Fprint(w, gresp("userMetadataList", listResp(lp.ids, lp.next)))
		case strings.Contains(req.Query, "userMetadataDetails"): // combined d{n}/u{n} read
			tp.read++
			data := map[string]any{}
			for k, v := range req.Variables {
				n, err := strconv.Atoi(strings.TrimPrefix(k, "i"))
				if err != nil {
					continue
				}
				id, _ := v.(string)
				alias := "u" + strconv.Itoa(n)
				if s, ok := seen[id]; ok {
					data[alias] = json.RawMessage(s)
				}
				alias = "d" + strconv.Itoa(n)
				if s, ok := details[id]; ok {
					data[alias] = json.RawMessage(s)
				}
			}
			out, _ := json.Marshal(map[string]any{"data": data})
			_, _ = w.Write(out)
		default:
			w.WriteHeader(500)
		}
	}))
	return srv, tp
}

// checkHeaders asserts the GraphQL path, Bearer auth, and watchmesh UA.
func checkHeaders(t *testing.T, r *http.Request) {
	t.Helper()
	if r.URL.Path != "/backend/graphql" {
		t.Fatalf("path=%q", r.URL.Path)
	}
	if r.Header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("auth=%q", r.Header.Get("Authorization"))
	}
	if r.Header.Get("User-Agent") != model.UserAgent {
		t.Fatalf("ua=%q", r.Header.Get("User-Agent"))
	}
}

// TestGQLHeaders verifies every GraphQL request carries auth, path, and UA.
func TestGQLHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checkHeaders(t, r)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"userDetails":{"__typename":"UserDetails","id":"u1"}}}`)
	}))
	defer srv.Close()
	if err := New(srv.URL, "tok").ValidateToken(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// TestPush verifies the full search→details→deploy pipeline: strict source
// identifier matching picks met_3 out of three candidates and the deploy
// input carries the metadata id, exact timestamp, and no episode fields.
func TestPush(t *testing.T) {
	details := map[string]string{
		"met_1": detailsResp("603", "MOVIE", "IMDB"), // wrong source: never matches
		"met_2": detailsResp("604", "MOVIE", "TMDB"), // right source: wrong id
		"met_3": detailsResp("603", "MOVIE", "TMDB"), // strict match
	}
	srv, tp := pushRouter(details, []string{"met_1", "met_2", "met_3"}, deployOK())
	defer srv.Close()
	delivered, err := New(srv.URL, "tok").Push(context.Background(), []model.WatchItem{item(603)})
	if err != nil || len(delivered) != 1 {
		t.Fatalf("delivered=%v err=%v", delivered, err)
	}
	if tp.search != 1 || tp.details != 3 || tp.deploy != 1 {
		t.Fatalf("hits search=%d details=%d deploy=%d", tp.search, tp.details, tp.deploy)
	}
	// Search body: TMDB source, lot, title query, window of 10.
	sreq := tp.reqs[0].Variables["i"].(map[string]any)
	if sreq["lot"] != "MOVIE" || sreq["source"] != "TMDB" {
		t.Fatalf("search input=%v", sreq)
	}
	sq := sreq["search"].(map[string]any)
	if sq["query"] != "T" || sq["take"].(float64) != searchTake {
		t.Fatalf("search=%v", sq)
	}
	// Deploy body: matched id, fixed timestamp, no season/episode fields for movies.
	var input []any
	for _, req := range tp.reqs {
		if strings.Contains(req.Query, "deployBulkMetadataProgressUpdate") {
			input = req.Variables["i"].([]any)
		}
	}
	if len(input) != 1 {
		t.Fatalf("deploy input=%v", input)
	}
	pi := input[0].(map[string]any)
	if pi["metadataId"] != "met_3" {
		t.Fatalf("metadataId=%v", pi["metadataId"])
	}
	done := pi["change"].(map[string]any)["createNewCompleted"].(map[string]any)
	if done["timestamp"] != "2026-05-10T20:00:00Z" {
		t.Fatalf("timestamp=%v", done["timestamp"])
	}
	if _, ok := done["showSeasonNumber"]; ok {
		t.Fatalf("movie must omit episode fields: %v", done)
	}
}

// TestPushDeployResults verifies the deploy stage's response handling: only
// the bare true means delivered; false/null, GraphQL errors, HTTP failures,
// and malformed bodies all error out.
func TestPushDeployResults(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantErr   bool
		delivered int
	}{
		{"accepted", 200, `{"data":{"deployBulkMetadataProgressUpdate":true}}`, false, 1},
		{"rejected", 200, `{"data":{"deployBulkMetadataProgressUpdate":false}}`, true, 0},
		{"null", 200, `{"data":{"deployBulkMetadataProgressUpdate":null}}`, true, 0},
		{"graphql-error", 200, `{"errors":[{"message":"boom"}]}`, true, 0},
		{"status-500", 500, `{}`, true, 0},
		{"malformed", 200, `not json`, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := pushRouter(
				map[string]string{"met_1": detailsResp("1", "MOVIE", "TMDB")},
				[]string{"met_1"},
				func(int) (int, string) { return tt.status, tt.body },
			)
			defer srv.Close()
			delivered, err := New(srv.URL, "tok").Push(context.Background(), []model.WatchItem{item(1)})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, tt.wantErr)
			}
			if len(delivered) != tt.delivered {
				t.Fatalf("delivered=%d want %d", len(delivered), tt.delivered)
			}
			if tt.wantErr && !strings.Contains(err.Error(), "ryot") {
				t.Fatalf("err=%v, must stay branded", err)
			}
		})
	}
}

// TestPushEpisodes verifies episode payloads ride under the SHOW lot with
// season/episode numbers in the createNewCompleted variant.
func TestPushEpisodes(t *testing.T) {
	srv, tp := pushRouter(
		map[string]string{"met_1": detailsResp("155", "SHOW", "TMDB")},
		[]string{"met_1"},
		deployOK(),
	)
	defer srv.Close()
	delivered, err := New(srv.URL, "tok").Push(context.Background(), []model.WatchItem{epItem(155, 2, 3)})
	if err != nil || len(delivered) != 1 {
		t.Fatalf("delivered=%v err=%v", delivered, err)
	}
	sreq := tp.reqs[0].Variables["i"].(map[string]any)
	if sreq["lot"] != "SHOW" {
		t.Fatalf("episodes must resolve under SHOW, input=%v", sreq)
	}
	var input []any
	for _, req := range tp.reqs {
		if strings.Contains(req.Query, "deployBulkMetadataProgressUpdate") {
			input = req.Variables["i"].([]any)
		}
	}
	done := input[0].(map[string]any)["change"].(map[string]any)["createNewCompleted"].(map[string]any)
	if done["showSeasonNumber"].(float64) != 2 || done["showEpisodeNumber"].(float64) != 3 {
		t.Fatalf("numbering=%v", done)
	}
	if done["timestamp"] != "2026-05-10T20:00:00Z" {
		t.Fatalf("timestamp=%v", done)
	}
}

// TestPushChunks verifies bulk deploys split at pushChunk boundaries.
func TestPushChunks(t *testing.T) {
	items := make([]model.WatchItem, 0, pushChunk+15)
	for i := 0; i < pushChunk+15; i++ {
		items = append(items, item(603)) // one bucket resolves once, deploys per item
	}
	srv, tp := pushRouter(
		map[string]string{"met_1": detailsResp("603", "MOVIE", "TMDB")},
		[]string{"met_1"},
		deployOK(),
	)
	defer srv.Close()
	delivered, err := New(srv.URL, "tok").Push(context.Background(), items)
	if err != nil || len(delivered) != pushChunk+15 {
		t.Fatalf("delivered=%d err=%v", len(delivered), err)
	}
	if tp.deploy != 2 {
		t.Fatalf("deploys=%d want 2", tp.deploy)
	}
	var lens []int
	for _, req := range tp.reqs {
		if strings.Contains(req.Query, "deployBulkMetadataProgressUpdate") {
			lens = append(lens, len(req.Variables["i"].([]any)))
		}
	}
	if len(lens) != 2 || lens[0] != pushChunk || lens[1] != 15 {
		t.Fatalf("chunk lens=%v, want [%d, 15]", lens, pushChunk)
	}
}

// TestHistorySuccess verifies the paged TMDB-list walk maps finished seen
// rows to movie/episode items and skips show-lot rows without numbering,
// empty timestamps, and unparsable values.
func TestHistorySuccess(t *testing.T) {
	srv, tp := historyRouter(
		map[string][]listPage{
			"MOVIE": {{ids: []string{"met_1", "met_2", "met_3"}}},
			"SHOW":  {{ids: []string{"met_4"}}},
		},
		map[string]string{
			"met_1": detailsResp("603", "MOVIE", "TMDB"),
			"met_2": detailsResp("abc", "MOVIE", "TMDB"), // unparsable identifier skips
			"met_3": detailsResp("604", "MOVIE", "IMDB"), // non-TMDB source skips
			"met_4": detailsResp("155", "SHOW", "TMDB"),
		},
		map[string]string{
			"met_1": seenResp(seenRow("2026-05-10T20:00:00Z", -1, -1)),
			"met_4": seenResp(
				seenRow("2026-05-13T19:00:00Z", 2, 3),
				seenRow("2026-05-14T09:00:00Z", -1, -1), // show-lot row without numbering skips
				seenRow("", 4, 1),                       // empty finishedOn skips
				seenRow("bogus", 4, 2),                  // unparsable finishedOn skips
			),
		},
	)
	defer srv.Close()
	got, err := New(srv.URL, "tok").History(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("history must not fail: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got=%+v", got)
	}
	m, e := got[0], got[1]
	if m.MediaType != "movie" || m.IDs.TMDB != 603 || m.Title != "T" {
		t.Fatalf("movie=%+v", m)
	}
	want := time.Date(2026, 5, 10, 20, 0, 0, 0, time.UTC)
	if !m.WatchedAt.Equal(want) {
		t.Fatalf("watchedAt=%v want %v", m.WatchedAt, want)
	}
	if e.MediaType != "episode" || e.IDs.TMDB != 155 || e.Season != 2 || e.Episode != 3 {
		t.Fatalf("episode=%+v", e)
	}
	if tp.list != 2 || tp.read == 0 {
		t.Fatalf("list=%d read=%d", tp.list, tp.read)
	}
	// The list input must be TMDB-filtered with the scripted page size.
	for _, req := range tp.reqs {
		if !strings.Contains(req.Query, "userMetadataList") {
			continue
		}
		if !strings.Contains(req.Query, fmt.Sprintf("take: %d", historyPageTake)) {
			t.Fatalf("page size=%q", req.Query)
		}
	}
}

// TestHistoryPagination verifies next-paged lists walk until nextPage ends.
func TestHistoryPagination(t *testing.T) {
	srv, tp := historyRouter(
		map[string][]listPage{
			"MOVIE": {{ids: []string{"met_1"}, next: nextP(2)}, {ids: []string{"met_2"}}},
			"SHOW":  {},
		},
		map[string]string{
			"met_1": detailsResp("603", "MOVIE", "TMDB"),
			"met_2": detailsResp("604", "MOVIE", "TMDB"),
		},
		map[string]string{
			"met_1": seenResp(seenRow("2026-05-10T20:00:00Z", -1, -1)),
			"met_2": seenResp(seenRow("2026-05-11T20:00:00Z", -1, -1)),
		},
	)
	defer srv.Close()
	got, err := New(srv.URL, "tok").History(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("history must not fail: %v", err)
	}
	if len(got) != 2 || got[0].IDs.TMDB != 603 || got[1].IDs.TMDB != 604 {
		t.Fatalf("got=%+v", got)
	}
	if tp.list != 3 { // two movie pages + one empty show page
		t.Fatalf("list hits=%d want 3", tp.list)
	}
}

// TestHistorySinceFilter verifies old entries drop and boundary-precise
// timestamps (== since) stay.
func TestHistorySinceFilter(t *testing.T) {
	srv, _ := historyRouter(
		map[string][]listPage{"MOVIE": {{ids: []string{"met_1", "met_2"}}}, "SHOW": {}},
		map[string]string{
			"met_1": detailsResp("603", "MOVIE", "TMDB"),
			"met_2": detailsResp("604", "MOVIE", "TMDB"),
		},
		map[string]string{
			"met_1": seenResp(seenRow("2026-05-01T00:00:00Z", -1, -1)),
			"met_2": seenResp(seenRow("2026-05-01T10:00:00Z", -1, -1)),
		},
	)
	defer srv.Close()
	since := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	got, err := New(srv.URL, "tok").History(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].IDs.TMDB != 604 {
		t.Fatalf("got=%+v, boundary-precise rows must stay", got)
	}
}

// TestValidateToken verifies the userDetails probe: __typename UserDetails
// (the union's object member) passes; error variants, GraphQL errors, and
// malformed/scrambled responses fail.
func TestValidateToken(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{"ok", 200, `{"data":{"userDetails":{"__typename":"UserDetails","id":"u1"}}}`, false},
		{"error-variant", 200, `{"data":{"userDetails":{"__typename":"UserDetailsError"}}}`, true},
		{"union-null", 200, `{"data":{"userDetails":null}}`, true},
		{"graphql-error", 200, `{"errors":[{"message":"token missing"}]}`, true},
		{"status-401", 401, `{}`, true},
		{"status-500", 500, `{}`, true},
		{"malformed", 200, `not json`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()
			if err := New(srv.URL, "tok").ValidateToken(context.Background()); (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
		})
	}
}
