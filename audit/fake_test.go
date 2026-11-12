package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/schneik80/FusionDataCLI/api"
)

// fakeHub is an in-memory stand-in for the MFGDM API. It answers the
// four query shapes the crawler sends, enforces the real API's 1000
// points per query, and can enforce a points-per-minute quota and
// inject failures — so the tests exercise the crawl against the same
// rules that make it lose data when it gets them wrong.
type fakeHub struct {
	t        *testing.T
	name     string
	projects []*fProject

	mu           sync.Mutex
	clock        time.Time
	folders      map[string]*fFolder
	items        map[string]*fItem
	requests     int
	throttleNext int            // answer this many requests with HTTP 429
	serverErrs   int            // answer this many requests with HTTP 503
	quota        int            // points per minute; 0 = unlimited
	quotaMinute  int64          // the minute quotaCur counts
	quotaCur     int            // points spent in that minute
	quotaPrev    int            // points spent in the minute before
	listFails    map[string]int // container id → list attempts still to fail
	maxQueryCost int
}

type fProject struct {
	id, name, status string
	root             *fFolder
}

type fFolder struct {
	id, name  string
	count     *int // objectCount; nil = items + sub-folders
	items     []*fItem
	subs      []*fFolder
	forbidden bool
}

type fItem struct {
	id, name, typ, ext string
	noData             bool   // tipRootModel / tipDrawing / tipConfigurationTable is null
	flaky              bool   // null in list pages, present when asked for by id
	nullRow            bool   // the list returns null for this row, with an error
	fieldErr           string // error reported on the data field, row still returned
	poison             bool   // the API fails on the data field and returns no row: a list page comes back with null results
}

func newFakeHub(t *testing.T, projects ...*fProject) *fakeHub {
	h := &fakeHub{
		t: t, name: "Test Hub", projects: projects,
		clock:   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		folders: map[string]*fFolder{}, items: map[string]*fItem{},
		listFails: map[string]int{},
	}
	var walk func(f *fFolder)
	walk = func(f *fFolder) {
		h.folders[f.id] = f
		for _, it := range f.items {
			h.items[it.id] = it
		}
		for _, s := range f.subs {
			walk(s)
		}
	}
	for _, p := range projects {
		p.root.id = p.id
		walk(p.root)
	}
	return h
}

// requester returns a requester wired to the fake, on a fake clock:
// sleeping advances time instead of waiting.
func (h *fakeHub) requester(concurrency, pointsPerMinute int) *requester {
	r := newRequester(func(context.Context, bool) (string, error) { return "tok", nil }, concurrency, pointsPerMinute)
	r.query = h.query
	r.now = func() time.Time {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.clock
	}
	r.sleep = func(ctx context.Context, d time.Duration) error {
		if d > 0 {
			h.mu.Lock()
			h.clock = h.clock.Add(d)
			h.mu.Unlock()
		}
		return ctx.Err()
	}
	return r
}

func (h *fakeHub) crawl(opts Options, concurrency, pointsPerMinute int) (*Result, error) {
	opts.HubID = "HUB"
	opts.Concurrency = concurrency
	return newCrawler(h.requester(concurrency, pointsPerMinute), opts).run(context.Background())
}

var aliasRe = regexp.MustCompile(`(\w+): (\w+)\((?:hubId: \$h, )?(?:folderId|projectId|itemId): \$(\w+)(?:, pagination: \{ limit: (\d+)(?:, cursor: \$(\w+))? \})?\)`)

type gqlErr struct {
	Message    string         `json:"message"`
	Path       []any          `json:"path,omitempty"`
	Extensions map[string]any `json:"extensions,omitempty"`
}

func (h *fakeHub) query(_ context.Context, _ string, q string, vars map[string]any) (*api.RawResponse, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.requests++
	// Every request takes a little time; without this a paced crawl on
	// the fake clock would never see the quota window move.
	h.clock = h.clock.Add(200 * time.Millisecond)

	if h.serverErrs > 0 {
		h.serverErrs--
		return &api.RawResponse{Status: 503, Body: []byte("upstream unavailable")}, nil
	}
	if h.throttleNext > 0 {
		h.throttleNext--
		return h.tooMany(0), nil
	}

	data := map[string]any{}
	var errs []gqlErr
	cost := 0

	if strings.Contains(q, "AuditProjects") {
		cost = 20
		offset := 0
		if c, ok := vars["c"].(string); ok {
			offset, _ = strconv.Atoi(c)
		}
		const page = 2 // small, so the project list paginates in tests
		var rows []any
		end := min(offset+page, len(h.projects))
		for _, p := range h.projects[offset:end] {
			rows = append(rows, map[string]any{"id": p.id, "name": p.name, "projectStatus": p.status})
		}
		var cursor any
		if end < len(h.projects) {
			cursor = strconv.Itoa(end)
		}
		data["hub"] = map[string]any{"name": h.name, "hubDataVersion": "2.0.0",
			"projects": map[string]any{"pagination": map[string]any{"cursor": cursor}, "results": rows}}
	}

	for _, m := range aliasRe.FindAllStringSubmatch(q, -1) {
		alias, field, idVar, limitStr, curVar := m[1], m[2], m[3], m[4], m[5]
		id, _ := vars[idVar].(string)
		limit, _ := strconv.Atoi(limitStr)
		offset := 0
		if curVar != "" {
			c, _ := vars[curVar].(string)
			offset, _ = strconv.Atoi(c)
		}
		full := strings.Contains(q, "tipRootModel")
		lean := !strings.Contains(q, "__typename") && !full // the re-count asks for ids only

		switch field {
		case "item":
			cost += 6
			it := h.items[id]
			if it == nil {
				data[alias] = nil
				errs = append(errs, gqlErr{Message: "not found", Path: []any{alias}})
				continue
			}
			if it.poison {
				data[alias] = nil
				errs = append(errs, gqlErr{Message: "Unknown Internal Server Error", Path: []any{alias, dataField(it)},
					Extensions: map[string]any{"code": "INTERNAL_SERVER_ERROR", "correlation_id": "corr-" + it.id}})
				continue
			}
			row, e := h.itemRow(it, false)
			data[alias] = row
			if e != "" {
				errs = append(errs, gqlErr{Message: e, Path: []any{alias, dataField(it)}, Extensions: map[string]any{"code": "INTERNAL_SERVER_ERROR"}})
			}

		case "folderByHubId":
			cost += 2
			f := h.folders[id]
			data[alias] = map[string]any{"objectCount": h.objectCount(f)}

		case "itemsByFolder", "itemsByProject":
			if full {
				cost += listBaseCost + itemCost*limit
			} else {
				cost += listBaseCost
			}
			f := h.folders[id]
			if h.failList(alias, id+"/items", f, &data, &errs) {
				continue
			}
			var rows []any
			end := min(offset+limit, len(f.items))
			poisoned := false
			for i, it := range f.items[offset:end] {
				if it.poison && full {
					poisoned = true
					errs = append(errs, gqlErr{Message: "Unknown Internal Server Error", Path: []any{alias, "results", float64(i), dataField(it)},
						Extensions: map[string]any{"code": "INTERNAL_SERVER_ERROR", "correlation_id": "corr-" + it.id}})
					continue
				}
				if !full && !lean {
					rows = append(rows, map[string]any{"__typename": it.typ, "id": it.id, "name": it.name, "extensionType": it.ext})
					continue
				}
				if it.nullRow {
					rows = append(rows, nil)
					errs = append(errs, gqlErr{Message: "Unknown Internal Server Error", Path: []any{alias, "results", float64(i)},
						Extensions: map[string]any{"code": "INTERNAL_SERVER_ERROR", "errorType": "UNKNOWN", "correlation_id": "corr-" + it.id}})
					continue
				}
				if lean {
					rows = append(rows, map[string]any{"id": it.id})
					continue
				}
				row, e := h.itemRow(it, true)
				rows = append(rows, row)
				if e != "" {
					errs = append(errs, gqlErr{Message: e, Path: []any{alias, "results", float64(i), dataField(it)},
						Extensions: map[string]any{"code": "INTERNAL_SERVER_ERROR"}})
				}
			}
			pg := page(rows, end, len(f.items))
			if poisoned {
				pg["results"] = nil
			}
			data[alias] = pg

		case "foldersByFolderInHub", "foldersByProject":
			cost += listBaseCost
			f := h.folders[id]
			if h.failList(alias, id+"/folders", f, &data, &errs) {
				continue
			}
			var rows []any
			end := min(offset+limit, len(f.subs))
			for _, s := range f.subs[offset:end] {
				if lean {
					rows = append(rows, map[string]any{"id": s.id})
				} else {
					rows = append(rows, map[string]any{"id": s.id, "name": s.name, "objectCount": h.objectCount(s)})
				}
			}
			data[alias] = page(rows, end, len(f.subs))

		default:
			h.t.Errorf("fake hub: unexpected field %q in query", field)
		}
	}

	if cost > h.maxQueryCost {
		h.maxQueryCost = cost
	}
	if cost > 1000 {
		body, _ := json.Marshal(map[string]any{"errors": []gqlErr{{
			Message:    fmt.Sprintf("Query point value %d exceeds maximum allowed query point value 1000.", cost),
			Extensions: map[string]any{"code": "GRAPHQL_VALIDATION_FAILED"}}}})
		return decode(400, body), nil
	}
	if h.quota > 0 {
		// A sliding-window counter, the way rate limiters commonly
		// approximate one: this minute's spend plus the previous
		// minute's, weighted by how much of it still overlaps. It
		// punishes bursts, as the real API was seen to.
		minute := h.clock.Unix() / 60
		switch {
		case minute == h.quotaMinute:
		case minute == h.quotaMinute+1:
			h.quotaPrev, h.quotaCur = h.quotaCur, 0
		default:
			h.quotaPrev, h.quotaCur = 0, 0
		}
		h.quotaMinute = minute
		frac := float64(h.clock.Unix()%60) / 60
		used := int(float64(h.quotaPrev)*(1-frac)) + h.quotaCur
		if used+cost > h.quota {
			return h.tooMany(h.quota - used), nil
		}
		h.quotaCur += cost
	}

	body, _ := json.Marshal(map[string]any{
		"data": data, "errors": errs,
		"extensions": map[string]any{"pointValue": map[string]any{"requestedQueryPointValue": cost}},
	})
	return decode(200, body), nil
}

// failList answers a list with an error when the folder is forbidden or
// scheduled to fail. key distinguishes the two lists of one container.
func (h *fakeHub) failList(alias, key string, f *fFolder, data *map[string]any, errs *[]gqlErr) bool {
	if f == nil {
		(*data)[alias] = nil
		*errs = append(*errs, gqlErr{Message: "not found", Path: []any{alias}})
		return true
	}
	if f.forbidden {
		(*data)[alias] = nil
		*errs = append(*errs, gqlErr{Message: "You do not have access to resource.", Path: []any{alias},
			Extensions: map[string]any{"code": "FORBIDDEN", "errorType": "UNKNOWN"}})
		return true
	}
	if h.listFails[key] > 0 {
		h.listFails[key]--
		(*data)[alias] = nil
		*errs = append(*errs, gqlErr{Message: "Unknown Internal Server Error", Path: []any{alias},
			Extensions: map[string]any{"code": "INTERNAL_SERVER_ERROR", "errorType": "UNKNOWN"}})
		return true
	}
	return false
}

func (h *fakeHub) tooMany(remaining int) *api.RawResponse {
	body, _ := json.Marshal(map[string]any{"errors": []gqlErr{{
		Message:    fmt.Sprintf("Query point value per minute quota exceeded with point value of 900 and remaining quota %d. Please try again later.", remaining),
		Extensions: map[string]any{"code": "GRAPHQL_VALIDATION_FAILED"}}}})
	return decode(429, body)
}

func (h *fakeHub) objectCount(f *fFolder) int {
	if f.count != nil {
		return *f.count
	}
	return len(f.items) + len(f.subs)
}

func dataField(it *fItem) string {
	switch it.typ {
	case "DrawingItem":
		return "tipDrawing"
	case "ConfiguredDesignItem":
		return "tipConfigurationTable"
	}
	return "tipRootModel"
}

// itemRow renders a document. inList is false when it is asked for by
// id, which is when a flaky document shows its data.
func (h *fakeHub) itemRow(it *fItem, inList bool) (map[string]any, string) {
	row := map[string]any{"__typename": it.typ, "id": it.id, "name": it.name, "extensionType": it.ext}
	if it.typ == "BasicItem" {
		return row, ""
	}
	if !inList {
		row["size"], row["lastModifiedOn"] = "0", "2019-05-29T17:48:11.000Z"
	}
	has := !it.noData && it.fieldErr == "" && !(it.flaky && inList)
	if has {
		row[dataField(it)] = map[string]any{"id": "data-" + it.id}
	} else {
		row[dataField(it)] = nil
	}
	return row, it.fieldErr
}

func page(rows []any, end, total int) map[string]any {
	var cursor any
	if end < total {
		cursor = strconv.Itoa(end)
	}
	if rows == nil {
		rows = []any{}
	}
	return map[string]any{"pagination": map[string]any{"cursor": cursor}, "results": rows}
}

// decode turns a response body into the RawResponse QueryRaw would
// have produced for it.
func decode(status int, body []byte) *api.RawResponse {
	out := &api.RawResponse{Status: status, Body: body}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message    string `json:"message"`
			Path       []any  `json:"path"`
			Extensions struct {
				Code          string `json:"code"`
				ErrorType     string `json:"errorType"`
				CorrelationID string `json:"correlation_id"`
			} `json:"extensions"`
		} `json:"errors"`
		Extensions struct {
			PointValue struct {
				Requested int `json:"requestedQueryPointValue"`
			} `json:"pointValue"`
		} `json:"extensions"`
	}
	if json.Unmarshal(body, &env) == nil {
		if len(env.Data) > 0 && string(env.Data) != "null" {
			out.Data = env.Data
		}
		out.Points = env.Extensions.PointValue.Requested
		for _, e := range env.Errors {
			out.Errors = append(out.Errors, api.RawError{Message: e.Message, Path: e.Path,
				Code: e.Extensions.Code, ErrorType: e.Extensions.ErrorType, CorrelationID: e.Extensions.CorrelationID})
		}
	}
	return out
}

// ---- builders ----------------------------------------------------------

func design(id string) *fItem {
	return &fItem{id: id, name: id + ".f3d", typ: "DesignItem", ext: "f3d"}
}

func designs(prefix string, n int) []*fItem {
	out := make([]*fItem, n)
	for i := range out {
		out[i] = design(fmt.Sprintf("%s-%03d", prefix, i))
	}
	return out
}

func folder(id string, items []*fItem, subs ...*fFolder) *fFolder {
	return &fFolder{id: id, name: id, items: items, subs: subs}
}

func project(id, status string, root *fFolder) *fProject {
	return &fProject{id: id, name: id, status: status, root: root}
}

func intp(n int) *int { return &n }
