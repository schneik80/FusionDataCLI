// Package audit walks every project, folder and document of a hub
// through the Manufacturing Data Model (MFGDM) API and reports what the
// API cannot describe: designs with no model behind them, rows it
// cannot read, folders whose listed contents do not add up.
//
// The crawl is built so that the report can be trusted:
//
//   - Nothing is dropped silently. Every GraphQL error is attributed to
//     the folder, page or row it belongs to. Whatever still cannot be
//     read after retrying is listed in the report, and the report says
//     outright whether it is complete.
//   - Throttling never costs data. The requester waits out HTTP 429,
//     slows all workers down, and retries; see requester.do.
//   - A flag is a second opinion. Every document flagged from a list
//     page is asked for again on its own before it is reported, and
//     every folder whose counts disagree is re-counted.
//
// It is also built to be cheap: listings are batched, many folders to
// one request, sized by each folder's object count to fit the API's
// 1000-point query budget.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/schneik80/FusionDataCLI/api"
)

// Kind classifies a document-level finding.
type Kind string

const (
	// KindNoModel: a DesignItem whose tipRootModel is null. The document
	// exists but has no MFGDM model: no properties, no assembly
	// relations, no where-used, no derivatives.
	KindNoModel Kind = "design-no-model"
	// KindNoDrawing: a DrawingItem whose tipDrawing is null.
	KindNoDrawing Kind = "drawing-no-data"
	// KindNoConfigTable: a ConfiguredDesignItem whose
	// tipConfigurationTable is null.
	KindNoConfigTable Kind = "configured-no-table"
	// KindFieldError: the row came back, but the API reported an error
	// on one of its fields.
	KindFieldError Kind = "field-error"
	// KindUnreadableRow: a list returned null in place of a document.
	// There is no id to report, only where it was.
	KindUnreadableRow Kind = "unreadable-row"
	// KindUnrecognised: a file with a Fusion extension that the API
	// returns as a plain BasicItem, not a design or drawing.
	KindUnrecognised Kind = "fusion-file-not-recognised"
)

// Finding is one document missing MFGDM data.
type Finding struct {
	Kind        Kind
	ItemID      string // empty for KindUnreadableRow
	Name        string
	Typename    string
	Ext         string
	ProjectID   string
	ProjectName string
	FolderID    string // empty when the document sits at the project root
	Path        string // "Project / Folder / Sub-folder"
	Detail      string // API error text, if any
	// Size (bytes) and Modified (RFC 3339) are read when the document
	// is rechecked; empty for findings that are not rechecked. They are
	// there to help explain the finding: a zero-byte design, say.
	Size, Modified string
	// Recheck is the outcome of asking for the document again on its
	// own: "confirmed", "not rechecked", or "recheck failed: …".
	Recheck string

	isolated bool // from a page listed without data fields; see task.lean
}

// FolderFinding is a folder whose own object count disagrees with what
// listing it returns.
type FolderFinding struct {
	FolderID    string
	ProjectID   string
	ProjectName string
	Path        string
	ObjectCount int // what the folder says it holds
	Items       int // documents listed
	Subfolders  int // sub-folders listed
	Note        string
}

// ReadFailure is something the crawl could not read. Any entry here
// means the report is incomplete for that container.
type ReadFailure struct {
	What        string // "documents of folder", "sub-folders of project", …
	ID          string
	ProjectName string
	Path        string
	Reason      string
}

// SkippedProject is a project that was not walked, and why.
type SkippedProject struct {
	ID, Name, Status, Reason string
}

// ProjectStats is the per-project tally.
type ProjectStats struct {
	ID, Name, Status string
	Folders          int
	Items            int
	Designs          int
	Drawings         int
	Configured       int
	Basic            int
	Missing          int // findings in this project, after rechecks
	FolderMismatches int
	Unread           int
}

// Result is everything the report is written from.
type Result struct {
	HubID, HubName, HubDataVersion string
	Started, Finished              time.Time
	ProjectFilter                  []string

	Projects []*ProjectStats
	Skipped  []SkippedProject

	Findings []Finding
	Folders  []FolderFinding
	Unread   []ReadFailure

	// ByType / ByExt count every document seen, keyed by __typename and
	// by "typename/extension".
	ByType map[string]int
	ByExt  map[string]int
	// DesignsByExt / DesignsNoModelByExt break designs down by file
	// extension, which is usually what explains a missing model.
	DesignsByExt        map[string]int
	DesignsNoModelByExt map[string]int

	TotalFolders   int
	TotalItems     int
	Duplicates     int // rows the API returned twice within one listing
	Flagged        int // documents flagged from list pages, before rechecks
	Cleared        int // flags withdrawn because the recheck found the data
	LeanPages      int // pages the API blanked, listed again without data fields
	Isolated       int // documents on those pages, each checked by id
	FoldersCleared int // folder mismatches withdrawn by the re-count

	Requests RequestStats
}

// Complete reports whether every container was read.
func (r *Result) Complete() bool { return len(r.Unread) == 0 }

// Options configures a crawl.
type Options struct {
	HubID string
	// Projects restricts the crawl to projects with these names or ids
	// (names compared case-insensitively). Empty means every project.
	Projects []string
	// IncludeInactive also walks projects whose status is not ACTIVE.
	// They usually refuse access, which is then reported as unread.
	IncludeInactive bool
	Concurrency     int
	// PointsPerMinute caps the query points sent per minute; 0 means
	// DefaultPointsPerMinute. It is lowered automatically if the API
	// throttles anyway.
	PointsPerMinute int
	// Progress, if set, is called from time to time with a snapshot.
	Progress func(Progress)
}

// Progress is a point-in-time view of a running crawl.
type Progress struct {
	Phase      string
	Containers int // projects + folders discovered
	Done       int // of those, fully listed
	Items      int
	Flagged    int
	Requests   int
	Throttled  int
	Limit      int // current concurrency
	Points     int // query points spent so far
	Budget     int // current points-per-minute budget
}

// Query-point arithmetic, measured against the live API: a list costs
// 11 points whatever its page size, and each document asked for adds 5
// once its model is selected — charged on the page size requested, not
// on what comes back. The API rejects a single query over 1000 points
// and meters the total per minute (see requester).
const (
	pointBudget     = 900
	listBaseCost    = 11
	itemCost        = 5
	maxItemLimit    = 170 // 11 + 5×170 = 861
	minItemLimit    = 1
	projectsCost    = 271 // one page of 50 projects, as the API prices it
	recheckItemCost = 15  // one document asked for by id
	recountCost     = 40
	// A project root carries no object count to size its page by, and
	// most hold few documents: start small and let nextLimit grow it.
	rootItemLimit = 20
	// overrunLimit is the page size once a folder has returned more
	// documents than its object count said it held.
	overrunLimit    = 25
	folderLimit     = 100
	maxAliases      = 24
	maxTaskTries    = 4
	recheckBatch    = 60 // 60 × 15 = 900 points
	recountLimit    = 200
	maxRecountPages = 500
)

// fusionExts are the extensions that should surface as a design or
// drawing, never as a BasicItem. Drawing templates (f2t) and archives
// (f3z) are deliberately absent: the API returns every one of those as
// a BasicItem, so that is how it models them, not data it has lost.
var fusionExts = map[string]bool{"f3d": true, "f2d": true}

const leanSelection = `__typename id name extensionType`

const itemSelection = leanSelection + `
				... on DesignItem { tipRootModel { id } }
				... on DrawingItem { tipDrawing { id } }
				... on ConfiguredDesignItem { tipConfigurationTable { id } }`

type taskKind int

const (
	taskItems taskKind = iota
	taskFolders
)

// container is a project root or a folder being listed.
type container struct {
	isProject   bool
	id          string
	path        string
	project     *ProjectStats
	objectCount int
	hasCount    bool

	items, subfolders        int
	itemsDone, foldersDone   bool
	itemsFailed, foldersFail bool
	seen                     map[string]bool
}

func (c *container) label() string {
	if c.isProject {
		return "project"
	}
	return "folder"
}

type task struct {
	c      *container
	kind   taskKind
	cursor string
	limit  int
	tries  int
	// lean asks for the rows without their data fields. The data field
	// of a broken document can blank the whole page (see apply), so the
	// page is listed lean and each document is then checked by id.
	lean bool
}

// solo reports whether the task must travel in a request of its own.
func (t *task) solo() bool { return t.tries > 0 || t.lean }

func (t *task) cost() int {
	if t.kind == taskFolders || t.lean {
		return listBaseCost
	}
	return listBaseCost + itemCost*t.limit
}

func (t *task) what() string {
	if t.kind == taskFolders {
		return "sub-folders of " + t.c.label()
	}
	return "documents of " + t.c.label()
}

// crawler holds the state of one run. Everything below mu is guarded by
// it; workers only touch state inside apply.
type crawler struct {
	opts Options
	req  *requester
	res  *Result

	mu         sync.Mutex
	cond       *sync.Cond
	queue      []*task
	active     int // batches in flight
	containers int
	done       int
	phase      string
	candidates []Finding    // flagged, awaiting recheck
	mismatched []*container // folders to re-count
}

// Run crawls the hub and returns the result. The error is non-nil only
// when the crawl could not start or was cancelled; partial failures are
// in Result.Unread.
func Run(ctx context.Context, token TokenSource, opts Options) (*Result, error) {
	if opts.HubID == "" {
		return nil, fmt.Errorf("audit: no hub id")
	}
	if opts.Concurrency < 1 {
		opts.Concurrency = 4
	}
	return newCrawler(newRequester(token, opts.Concurrency, opts.PointsPerMinute), opts).run(ctx)
}

func newCrawler(req *requester, opts Options) *crawler {
	c := &crawler{
		opts: opts,
		req:  req,
		res: &Result{
			HubID:               opts.HubID,
			ProjectFilter:       opts.Projects,
			ByType:              map[string]int{},
			ByExt:               map[string]int{},
			DesignsByExt:        map[string]int{},
			DesignsNoModelByExt: map[string]int{},
		},
		phase: "projects",
	}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *crawler) run(ctx context.Context) (*Result, error) {
	c.res.Started = c.req.now()

	stopProgress := c.startProgress(ctx)
	defer stopProgress()

	projects, err := c.listProjects(ctx)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.phase = "listing"
	for _, p := range projects {
		root := &container{isProject: true, id: p.ID, path: p.Name, project: p, seen: map[string]bool{}}
		c.containers++
		c.queue = append(c.queue,
			&task{c: root, kind: taskFolders, limit: folderLimit},
			&task{c: root, kind: taskItems, limit: rootItemLimit})
	}
	c.mu.Unlock()

	// One worker per slot of maximum concurrency; the requester decides
	// how many may actually be on the wire.
	var wg sync.WaitGroup
	for i := 0; i < c.opts.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.worker(ctx)
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	c.setPhase("rechecking flagged documents")
	c.recheckItems(ctx)
	c.setPhase("re-counting folders")
	c.recountFolders(ctx)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	c.finalize()
	return c.res, nil
}

func (c *crawler) setPhase(p string) {
	c.mu.Lock()
	c.phase = p
	c.mu.Unlock()
}

func (c *crawler) startProgress(ctx context.Context) (stop func()) {
	if c.opts.Progress == nil {
		return func() {}
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				c.opts.Progress(c.snapshot())
			}
		}
	}()
	return func() {
		close(done)
		wg.Wait()
		c.opts.Progress(c.snapshot())
	}
}

func (c *crawler) snapshot() Progress {
	st := c.req.Stats()
	c.mu.Lock()
	defer c.mu.Unlock()
	return Progress{
		Phase:      c.phase,
		Containers: c.containers,
		Done:       c.done,
		Items:      c.res.TotalItems,
		Flagged:    c.res.Flagged,
		Requests:   st.Requests,
		Throttled:  st.Throttled,
		Limit:      st.FinalConcurrency,
		Points:     st.Points,
		Budget:     st.PointsPerMinute,
	}
}

// ---------------------------------------------------------------------------
// Projects
// ---------------------------------------------------------------------------

func (c *crawler) listProjects(ctx context.Context) ([]*ProjectStats, error) {
	want := map[string]bool{}
	for _, p := range c.opts.Projects {
		want[strings.ToLower(strings.TrimSpace(p))] = true
	}
	matched := map[string]bool{}

	var out []*ProjectStats
	cursor := ""
	for page := 0; ; page++ {
		paging := "limit: 50"
		vars := map[string]any{"h": c.opts.HubID}
		decl := "$h: ID!"
		if cursor != "" {
			paging += ", cursor: $c"
			decl += ", $c: String!"
			vars["c"] = cursor
		}
		q := fmt.Sprintf(`query AuditProjects(%s) {
			hub(hubId: $h) {
				name hubDataVersion
				projects(pagination: { %s }) {
					pagination { cursor }
					results { id name projectStatus }
				}
			}
		}`, decl, paging)

		var hub struct {
			Hub *struct {
				Name     string `json:"name"`
				Version  string `json:"hubDataVersion"`
				Projects *struct {
					Pagination struct {
						Cursor string `json:"cursor"`
					} `json:"pagination"`
					Results []*struct {
						ID     string `json:"id"`
						Name   string `json:"name"`
						Status string `json:"projectStatus"`
					} `json:"results"`
				} `json:"projects"`
			} `json:"hub"`
		}
		// The project list is the root of everything: an error here is
		// retried, and if it will not go away the crawl stops rather
		// than report on part of a hub as if it were all of it.
		var lastErr error
		ok := false
		for try := 0; try < maxTaskTries && !ok; try++ {
			if try > 0 {
				if err := c.req.sleep(ctx, c.req.backoff(try)); err != nil {
					return nil, err
				}
			}
			resp, err := c.req.do(ctx, q, vars, projectsCost)
			if err != nil {
				return nil, fmt.Errorf("listing projects: %w", err)
			}
			if len(resp.Errors) > 0 {
				lastErr = fmt.Errorf("%s", describe(resp.Errors[0]))
				continue
			}
			hub.Hub = nil
			if err := json.Unmarshal(resp.Data, &hub); err != nil || hub.Hub == nil || hub.Hub.Projects == nil {
				lastErr = fmt.Errorf("unexpected response: %s", snippet(resp.Body))
				continue
			}
			ok = true
		}
		if !ok {
			return nil, fmt.Errorf("listing projects of hub %s: %w", c.opts.HubID, lastErr)
		}

		c.res.HubName, c.res.HubDataVersion = hub.Hub.Name, hub.Hub.Version
		for _, p := range hub.Hub.Projects.Results {
			if p == nil {
				c.res.Unread = append(c.res.Unread, ReadFailure{
					What: "project row", Reason: "the project list returned null in place of a project",
				})
				continue
			}
			if len(want) > 0 {
				if !want[strings.ToLower(p.Name)] && !want[strings.ToLower(p.ID)] {
					continue
				}
				matched[strings.ToLower(p.Name)] = true
				matched[strings.ToLower(p.ID)] = true
			}
			if !strings.EqualFold(p.Status, "ACTIVE") && !c.opts.IncludeInactive {
				c.res.Skipped = append(c.res.Skipped, SkippedProject{
					ID: p.ID, Name: p.Name, Status: p.Status,
					Reason: "project is not active; the API refuses its contents",
				})
				continue
			}
			out = append(out, &ProjectStats{ID: p.ID, Name: p.Name, Status: p.Status})
		}
		cursor = hub.Hub.Projects.Pagination.Cursor
		if cursor == "" {
			break
		}
		if page > 1000 {
			return nil, fmt.Errorf("listing projects: cursor did not end after %d pages", page)
		}
	}

	for _, name := range c.opts.Projects {
		if !matched[strings.ToLower(strings.TrimSpace(name))] {
			return nil, fmt.Errorf("no project named %q in hub %q", name, c.res.HubName)
		}
	}
	c.res.Projects = out
	return out, nil
}

// ---------------------------------------------------------------------------
// Batched listing
// ---------------------------------------------------------------------------

func (c *crawler) worker(ctx context.Context) {
	for {
		batch := c.next(ctx)
		if batch == nil {
			return
		}
		cost := 0
		for _, t := range batch {
			cost += t.cost()
		}
		resp, err := c.req.do(ctx, buildListQuery(batch), listVars(c.opts.HubID, batch), cost)
		c.mu.Lock()
		c.apply(batch, resp, err)
		c.active--
		c.mu.Unlock()
		c.cond.Broadcast()
	}
}

// next blocks until there is work and returns a batch that fits the
// point budget, or nil when the crawl is finished or cancelled.
func (c *crawler) next(ctx context.Context) []*task {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.queue) == 0 {
		if c.active == 0 || ctx.Err() != nil {
			c.cond.Broadcast()
			return nil
		}
		c.cond.Wait()
	}
	if ctx.Err() != nil {
		return nil
	}

	// A task that has already failed once travels alone, so whatever is
	// wrong with it cannot take other folders' answers down with it.
	if c.queue[0].solo() {
		t := c.queue[0]
		c.queue = c.queue[1:]
		c.active++
		return []*task{t}
	}
	var batch, rest []*task
	budget := pointBudget
	for _, t := range c.queue {
		if !t.solo() && len(batch) < maxAliases && t.cost() <= budget {
			batch = append(batch, t)
			budget -= t.cost()
		} else {
			rest = append(rest, t)
		}
	}
	c.queue = rest
	c.active++
	return batch
}

func buildListQuery(batch []*task) string {
	var decl, body strings.Builder
	needHub := false
	for i, t := range batch {
		fmt.Fprintf(&decl, ", $id%d: ID!", i)
		paging := fmt.Sprintf("limit: %d", t.limit)
		if t.cursor != "" {
			fmt.Fprintf(&decl, ", $c%d: String!", i)
			paging += fmt.Sprintf(", cursor: $c%d", i)
		}
		var field, sel string
		switch {
		case t.kind == taskItems && t.c.isProject:
			field = fmt.Sprintf("itemsByProject(projectId: $id%d", i)
		case t.kind == taskItems:
			field = fmt.Sprintf("itemsByFolder(hubId: $h, folderId: $id%d", i)
			needHub = true
		case t.c.isProject:
			field = fmt.Sprintf("foldersByProject(projectId: $id%d", i)
		default:
			field = fmt.Sprintf("foldersByFolderInHub(hubId: $h, folderId: $id%d", i)
			needHub = true
		}
		switch {
		case t.kind == taskItems && t.lean:
			sel = leanSelection
		case t.kind == taskItems:
			sel = itemSelection
		default:
			sel = "id name objectCount"
		}
		fmt.Fprintf(&body, "\n\t\t\tt%d: %s, pagination: { %s }) {\n\t\t\t\tpagination { cursor }\n\t\t\t\tresults { %s }\n\t\t\t}", i, field, paging, sel)
	}
	d := strings.TrimPrefix(decl.String(), ", ")
	if needHub {
		d = "$h: ID!, " + d
	}
	return fmt.Sprintf("query AuditList(%s) {%s\n\t\t}", d, body.String())
}

func listVars(hubID string, batch []*task) map[string]any {
	vars := map[string]any{}
	for i, t := range batch {
		vars[fmt.Sprintf("id%d", i)] = t.c.id
		if t.cursor != "" {
			vars[fmt.Sprintf("c%d", i)] = t.cursor
		}
		if !t.c.isProject {
			vars["h"] = hubID
		}
	}
	return vars
}

type listPage struct {
	Pagination *struct {
		Cursor string `json:"cursor"`
	} `json:"pagination"`
	Results []json.RawMessage `json:"results"`
}

type itemRow struct {
	Typename string `json:"__typename"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	Ext      string `json:"extensionType"`
	Model    *struct {
		ID string `json:"id"`
	} `json:"tipRootModel"`
	Drawing *struct {
		ID string `json:"id"`
	} `json:"tipDrawing"`
	Config *struct {
		ID string `json:"id"`
	} `json:"tipConfigurationTable"`
	// Only asked for on a recheck.
	Size     string `json:"size"`
	Modified string `json:"lastModifiedOn"`
}

type folderRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ObjectCount *int   `json:"objectCount"`
}

// apply folds one batch's response into the crawl state. Called with
// c.mu held.
func (c *crawler) apply(batch []*task, resp *api.RawResponse, err error) {
	if err != nil {
		// The requester already retried at length. These listings are
		// lost, and the report has to say so.
		for _, t := range batch {
			c.fail(t, err.Error())
		}
		return
	}

	// Sort the errors: by alias for list-level ones, by alias and row
	// for the rest. An error with no usable path condemns the request.
	listErr := map[int]api.RawError{}
	rowErrs := map[int]map[int][]api.RawError{}
	var requestErr *api.RawError
	for i := range resp.Errors {
		e := resp.Errors[i]
		alias, row, ok := locate(e.Path)
		switch {
		case !ok || alias >= len(batch):
			if requestErr == nil {
				requestErr = &e
			}
		case row < 0:
			listErr[alias] = e
		default:
			if rowErrs[alias] == nil {
				rowErrs[alias] = map[int][]api.RawError{}
			}
			rowErrs[alias][row] = append(rowErrs[alias][row], e)
		}
	}

	var data map[string]json.RawMessage
	if len(resp.Data) > 0 {
		_ = json.Unmarshal(resp.Data, &data)
	}

	for i, t := range batch {
		raw, present := data[fmt.Sprintf("t%d", i)]
		var page listPage
		usable := present && string(raw) != "null" && json.Unmarshal(raw, &page) == nil

		if e, bad := listErr[i]; bad || !usable {
			reason := "the API returned no list"
			switch {
			case bad:
				reason = describe(e)
			case requestErr != nil:
				reason = describe(*requestErr)
			}
			// A refusal is an answer; asking again will not change it.
			if bad && strings.EqualFold(e.Code, "FORBIDDEN") {
				c.fail(t, reason)
				continue
			}
			c.retry(t, reason, requestErr != nil)
			continue
		}

		// The data fields are non-nullable in places: when the API fails
		// on one (seen on tipConfigurationTable of a configured design),
		// it does not null the row, it nulls the whole results list, and
		// the page looks like an empty folder. Such a page says nothing
		// about what the folder holds. List it again without the data
		// fields, and let every document on it be checked by id, where a
		// failure is confined to the one document.
		if t.kind == taskItems && !t.lean && blanked(page.Results, rowErrs[i]) {
			t.lean = true
			c.res.LeanPages++
			c.queue = append(c.queue, t)
			continue
		}

		if t.kind == taskItems {
			c.applyItems(t, &page, rowErrs[i])
		} else {
			c.applyFolders(t, &page, rowErrs[i])
		}
	}
}

// blanked reports whether a page of documents lost rows to an error:
// the API reports an error inside a row that the results do not hold.
func blanked(results []json.RawMessage, rowErrs map[int][]api.RawError) bool {
	for row := range rowErrs {
		if row >= len(results) {
			return true
		}
	}
	return false
}

// locate reads an error path produced by a list query: the alias index
// and, for an error inside a result row, the row index (otherwise -1).
func locate(path []any) (alias, row int, ok bool) {
	if len(path) == 0 {
		return 0, 0, false
	}
	name, isStr := path[0].(string)
	if !isStr || len(name) < 2 || name[0] != 't' {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(name[1:], "%d", &alias); err != nil {
		return 0, 0, false
	}
	if len(path) >= 3 && path[1] == "results" {
		if f, isNum := path[2].(float64); isNum {
			return alias, int(f), true
		}
	}
	return alias, -1, true
}

func describe(e api.RawError) string {
	s := e.Message
	if e.Code != "" {
		s += " [" + e.Code + "]"
	}
	if e.CorrelationID != "" {
		s += " (correlation id " + e.CorrelationID + ")"
	}
	return s
}

// retry puts a failed listing back, to run alone next time. shrink
// asks for a smaller page: the failure was the request as a whole, most
// likely its point cost.
func (c *crawler) retry(t *task, reason string, shrink bool) {
	t.tries++
	if t.tries >= maxTaskTries {
		c.fail(t, reason)
		return
	}
	if shrink && t.kind == taskItems && t.limit > minItemLimit {
		t.limit = max(minItemLimit, t.limit/2)
	}
	c.queue = append(c.queue, t)
}

// fail records a listing that could not be read and closes it.
func (c *crawler) fail(t *task, reason string) {
	c.res.Unread = append(c.res.Unread, ReadFailure{
		What: t.what(), ID: t.c.id, ProjectName: t.c.project.Name, Path: t.c.path, Reason: reason,
	})
	t.c.project.Unread++
	if t.kind == taskItems {
		t.c.itemsFailed = true
	} else {
		t.c.foldersFail = true
	}
	c.finish(t)
}

// finish marks one of a container's two listings as over and, once
// both are, compares the totals with the folder's own object count.
func (c *crawler) finish(t *task) {
	cn := t.c
	if t.kind == taskItems {
		cn.itemsDone = true
	} else {
		cn.foldersDone = true
	}
	if !cn.itemsDone || !cn.foldersDone {
		return
	}
	c.done++
	cn.seen = nil
	if cn.hasCount && !cn.itemsFailed && !cn.foldersFail && cn.items+cn.subfolders != cn.objectCount {
		c.mismatched = append(c.mismatched, cn)
	}
}

func (c *crawler) applyItems(t *task, page *listPage, rowErrs map[int][]api.RawError) {
	// A null row has no id, so it cannot be rechecked later the way a
	// flagged document can. Ask for the page once more before
	// accepting that the row is unreadable.
	if t.tries == 0 {
		for _, raw := range page.Results {
			if string(raw) == "null" {
				t.tries++
				c.queue = append(c.queue, t)
				return
			}
		}
	}

	cn := t.c
	for idx, raw := range page.Results {
		var row itemRow
		if string(raw) == "null" || json.Unmarshal(raw, &row) != nil || row.ID == "" {
			detail := "the list returned null in place of a document"
			if errs := rowErrs[idx]; len(errs) > 0 {
				detail = describe(errs[0])
			}
			cn.items++
			c.res.TotalItems++
			cn.project.Items++
			c.res.ByType["(unreadable)"]++
			c.res.Findings = append(c.res.Findings, c.finding(cn, KindUnreadableRow, itemRow{}, detail, "not rechecked (no id to ask for)"))
			continue
		}
		if cn.seen[row.ID] {
			c.res.Duplicates++
			continue
		}
		cn.seen[row.ID] = true
		cn.items++
		c.count(cn, row)

		kind, detail := classify(row, rowErrs[idx])
		if kind == "" {
			continue
		}
		f := c.finding(cn, kind, row, detail, "")
		if kind == KindUnrecognised {
			c.res.Flagged++
			// Nothing about a BasicItem can change on a second look.
			f.Recheck = "not rechecked"
			c.res.Findings = append(c.res.Findings, f)
			continue
		}
		if t.lean {
			// Not a flag: the page carried no data fields, so every
			// document on it is asked for by id to find out.
			f.isolated = true
			c.res.Isolated++
		} else {
			c.res.Flagged++
		}
		c.candidates = append(c.candidates, f)
	}

	if cur := cursorOf(page); cur != "" {
		c.queue = append(c.queue, &task{c: cn, kind: taskItems, cursor: cur, limit: nextLimit(cn)})
		return
	}
	c.finish(t)
}

// nextLimit sizes a container's next page of documents. Points are
// charged on the page size asked for, so the aim is a page that is just
// big enough: what the object count says is left, or — with no count,
// or once it has proved too low — the biggest page for a list that is
// evidently long, and a modest one for an overrun.
func nextLimit(cn *container) int {
	if !cn.hasCount {
		return maxItemLimit
	}
	if left := cn.objectCount - cn.items; left > 0 {
		return min(left, maxItemLimit)
	}
	return overrunLimit
}

func cursorOf(p *listPage) string {
	if p.Pagination == nil {
		return ""
	}
	return p.Pagination.Cursor
}

// classify decides whether a document row is missing MFGDM data.
func classify(row itemRow, errs []api.RawError) (Kind, string) {
	detail := ""
	if len(errs) > 0 {
		detail = describe(errs[0])
	}
	ext := strings.ToLower(row.Ext)
	switch row.Typename {
	case "DesignItem":
		if row.Model == nil {
			return KindNoModel, detail
		}
	case "DrawingItem":
		if row.Drawing == nil {
			return KindNoDrawing, detail
		}
	case "ConfiguredDesignItem":
		if row.Config == nil {
			return KindNoConfigTable, detail
		}
	case "BasicItem":
		if fusionExts[ext] {
			return KindUnrecognised, detail
		}
	}
	if len(errs) > 0 {
		return KindFieldError, detail
	}
	return "", ""
}

func (c *crawler) count(cn *container, row itemRow) {
	ext := strings.ToLower(row.Ext)
	if ext == "" {
		ext = "(none)"
	}
	c.res.TotalItems++
	c.res.ByType[row.Typename]++
	c.res.ByExt[row.Typename+"/"+ext]++
	p := cn.project
	p.Items++
	switch row.Typename {
	case "DesignItem":
		p.Designs++
		c.res.DesignsByExt[ext]++
	case "DrawingItem":
		p.Drawings++
	case "ConfiguredDesignItem":
		p.Configured++
	case "BasicItem":
		p.Basic++
	}
}

func (c *crawler) finding(cn *container, kind Kind, row itemRow, detail, recheck string) Finding {
	f := Finding{
		Kind: kind, ItemID: row.ID, Name: row.Name, Typename: row.Typename, Ext: strings.ToLower(row.Ext),
		ProjectID: cn.project.ID, ProjectName: cn.project.Name, Path: cn.path,
		Detail: detail, Recheck: recheck,
	}
	if !cn.isProject {
		f.FolderID = cn.id
	}
	return f
}

func (c *crawler) applyFolders(t *task, page *listPage, rowErrs map[int][]api.RawError) {
	if t.tries == 0 {
		for _, raw := range page.Results {
			if string(raw) == "null" {
				t.tries++
				c.queue = append(c.queue, t)
				return
			}
		}
	}

	cn := t.c
	for idx, raw := range page.Results {
		var row folderRow
		if string(raw) == "null" || json.Unmarshal(raw, &row) != nil || row.ID == "" {
			reason := "the folder list returned null in place of a sub-folder; its contents were not walked"
			if errs := rowErrs[idx]; len(errs) > 0 {
				reason = describe(errs[0])
			}
			cn.subfolders++
			cn.project.Unread++
			c.res.Unread = append(c.res.Unread, ReadFailure{
				What: "sub-folder row in " + cn.label(), ID: cn.id, ProjectName: cn.project.Name, Path: cn.path, Reason: reason,
			})
			continue
		}
		if cn.seen["folder:"+row.ID] {
			c.res.Duplicates++
			continue
		}
		cn.seen["folder:"+row.ID] = true
		cn.subfolders++
		cn.project.Folders++
		c.res.TotalFolders++
		c.containers++

		child := &container{id: row.ID, path: cn.path + " / " + row.Name, project: cn.project, seen: map[string]bool{}}
		limit := rootItemLimit
		if row.ObjectCount != nil {
			child.hasCount, child.objectCount = true, *row.ObjectCount
			// Size the page to the folder. Points are charged on the
			// page size asked for, so a snug page is what makes the
			// crawl cheap: small folders then pack many to a request
			// and large ones take the biggest page a query may carry.
			// An empty folder is still asked, for one row — the count
			// is a hint, not proof, and the cursor covers any excess.
			limit = min(max(*row.ObjectCount, minItemLimit), maxItemLimit)
		}
		c.queue = append(c.queue,
			&task{c: child, kind: taskFolders, limit: folderLimit},
			&task{c: child, kind: taskItems, limit: limit})
	}

	if cur := cursorOf(page); cur != "" {
		c.queue = append(c.queue, &task{c: cn, kind: taskFolders, cursor: cur, limit: t.limit})
		return
	}
	c.finish(t)
}

// ---------------------------------------------------------------------------
// Second opinions
// ---------------------------------------------------------------------------

// recheckItems asks for every flagged document again, by id, and keeps
// only the flags that hold. A list page is one large query in which a
// transient failure can null a single field; a document asked for on
// its own either has the data or does not.
func (c *crawler) recheckItems(ctx context.Context) {
	c.mu.Lock()
	cands := c.candidates
	c.candidates = nil
	c.mu.Unlock()
	if len(cands) == 0 {
		return
	}

	var batches [][]int
	for i := 0; i < len(cands); i += recheckBatch {
		var b []int
		for j := i; j < min(i+recheckBatch, len(cands)); j++ {
			b = append(b, j)
		}
		batches = append(batches, b)
	}

	verdict := make([]string, len(cands)) // "", "cleared", or the Recheck text
	var wg sync.WaitGroup
	work := make(chan []int)
	for i := 0; i < c.opts.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range work {
				c.recheckBatch(ctx, cands, b, verdict)
			}
		}()
	}
	for _, b := range batches {
		work <- b
	}
	close(work)
	wg.Wait()

	c.mu.Lock()
	defer c.mu.Unlock()
	for i, f := range cands {
		if verdict[i] == "cleared" {
			if !f.isolated {
				c.res.Cleared++
			}
			continue
		}
		if f.isolated {
			c.res.Flagged++
		}
		f.Recheck = verdict[i]
		if f.Recheck == "" {
			f.Recheck = "recheck failed: no answer"
		}
		c.res.Findings = append(c.res.Findings, f)
	}
}

func (c *crawler) recheckBatch(ctx context.Context, cands []Finding, idxs []int, verdict []string) {
	if ctx.Err() != nil {
		return
	}
	var decl, body strings.Builder
	vars := map[string]any{"h": c.opts.HubID}
	for n, i := range idxs {
		fmt.Fprintf(&decl, ", $i%d: ID!", n)
		fmt.Fprintf(&body, "\n\t\t\tv%d: item(hubId: $h, itemId: $i%d) { size lastModifiedOn %s }", n, n, itemSelection)
		vars[fmt.Sprintf("i%d", n)] = cands[i].ItemID
	}
	q := fmt.Sprintf("query AuditRecheck($h: ID!%s) {%s\n\t\t}", decl.String(), body.String())

	resp, err := c.req.do(ctx, q, vars, recheckItemCost*len(idxs))
	if err != nil {
		for _, i := range idxs {
			verdict[i] = "recheck failed: " + err.Error()
		}
		return
	}

	errs := map[int][]api.RawError{}
	whole := false
	for _, e := range resp.Errors {
		name := ""
		if len(e.Path) > 0 {
			name, _ = e.Path[0].(string)
		}
		var n int
		if len(name) > 1 && name[0] == 'v' {
			if _, err := fmt.Sscanf(name[1:], "%d", &n); err == nil && n < len(idxs) {
				errs[n] = append(errs[n], e)
				continue
			}
		}
		whole = true
	}
	// The request as a whole was refused (too many points, most
	// likely). Halve it; a single document that still fails is
	// reported as such.
	if whole && len(resp.Data) == 0 {
		if len(idxs) == 1 {
			verdict[idxs[0]] = "recheck failed: " + describe(resp.Errors[0])
			return
		}
		mid := len(idxs) / 2
		c.recheckBatch(ctx, cands, idxs[:mid], verdict)
		c.recheckBatch(ctx, cands, idxs[mid:], verdict)
		return
	}

	var data map[string]json.RawMessage
	_ = json.Unmarshal(resp.Data, &data)
	for n, i := range idxs {
		raw := data[fmt.Sprintf("v%d", n)]
		var row itemRow
		if len(raw) == 0 || string(raw) == "null" || json.Unmarshal(raw, &row) != nil {
			// The API failing on the data field itself is the answer:
			// the document is there and its data cannot be had.
			if kind := fieldKind(errs[n]); kind != "" {
				verdict[i] = "confirmed"
				cands[i].Kind = kind
				cands[i].Detail = describe(errs[n][0])
				continue
			}
			reason := "the API returned nothing for this id"
			if len(errs[n]) > 0 {
				reason = describe(errs[n][0])
			}
			verdict[i] = "recheck failed: " + reason
			continue
		}
		kind, detail := classify(row, errs[n])
		if kind == "" {
			verdict[i] = "cleared"
			continue
		}
		verdict[i] = "confirmed"
		cands[i].Size, cands[i].Modified = row.Size, row.Modified
		if detail != "" && cands[i].Detail == "" {
			cands[i].Detail = detail
		}
		cands[i].Kind = kind
	}
}

// fieldKind names the finding for a document the API would not return
// because it failed on the document's data field.
func fieldKind(errs []api.RawError) Kind {
	for _, e := range errs {
		if len(e.Path) < 2 {
			continue
		}
		switch e.Path[len(e.Path)-1] {
		case "tipRootModel":
			return KindNoModel
		case "tipDrawing":
			return KindNoDrawing
		case "tipConfigurationTable":
			return KindNoConfigTable
		}
	}
	return ""
}

// recountFolders lists again, with the cheapest possible query, every
// folder whose listed contents did not add up to its object count, and
// re-reads the count itself. Only a disagreement that survives is
// reported. A re-count that differs from the first pass is reported
// too, as a read failure: one of the two listings lost rows.
func (c *crawler) recountFolders(ctx context.Context) {
	c.mu.Lock()
	folders := c.mismatched
	c.mismatched = nil
	c.mu.Unlock()
	if len(folders) == 0 {
		return
	}

	var wg sync.WaitGroup
	work := make(chan *container)
	for i := 0; i < c.opts.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for cn := range work {
				c.recountFolder(ctx, cn)
			}
		}()
	}
	for _, cn := range folders {
		work <- cn
	}
	close(work)
	wg.Wait()
}

func (c *crawler) recountFolder(ctx context.Context, cn *container) {
	if ctx.Err() != nil {
		return
	}
	ff := FolderFinding{
		FolderID: cn.id, ProjectID: cn.project.ID, ProjectName: cn.project.Name, Path: cn.path,
		ObjectCount: cn.objectCount, Items: cn.items, Subfolders: cn.subfolders,
	}
	report := func(note string) {
		ff.Note = note
		c.mu.Lock()
		c.res.Folders = append(c.res.Folders, ff)
		cn.project.FolderMismatches++
		c.mu.Unlock()
	}

	count, items, subs, err := c.recount(ctx, cn.id)
	if err != nil {
		report("re-count failed: " + err.Error())
		return
	}
	if items != cn.items || subs != cn.subfolders {
		c.mu.Lock()
		c.res.Unread = append(c.res.Unread, ReadFailure{
			What: "contents of folder", ID: cn.id, ProjectName: cn.project.Name, Path: cn.path,
			Reason: fmt.Sprintf("two listings disagree: first pass %d documents + %d sub-folders, re-count %d + %d (the folder may have changed during the run)",
				cn.items, cn.subfolders, items, subs),
		})
		cn.project.Unread++
		c.mu.Unlock()
	}
	ff.ObjectCount, ff.Items, ff.Subfolders = count, items, subs
	if items+subs == count {
		c.mu.Lock()
		c.res.FoldersCleared++
		c.mu.Unlock()
		return
	}
	report("confirmed by re-count")
}

// recount returns a folder's current object count and how many
// documents and sub-folders listing it yields, ids only.
func (c *crawler) recount(ctx context.Context, folderID string) (count, items, subs int, err error) {
	seenItems, seenSubs := map[string]bool{}, map[string]bool{}
	itemCur, subCur := "", ""
	itemsDone, subsDone := false, false
	for page := 0; !itemsDone || !subsDone; page++ {
		if page >= maxRecountPages {
			return 0, 0, 0, fmt.Errorf("cursor did not end after %d pages", page)
		}
		decl := "$h: ID!, $f: ID!"
		vars := map[string]any{"h": c.opts.HubID, "f": folderID}
		body := ""
		if page == 0 {
			body += "\n\t\t\tfolder: folderByHubId(hubId: $h, folderId: $f) { objectCount }"
		}
		if !itemsDone {
			paging := fmt.Sprintf("limit: %d", recountLimit)
			if itemCur != "" {
				decl += ", $ic: String!"
				vars["ic"] = itemCur
				paging += ", cursor: $ic"
			}
			body += fmt.Sprintf("\n\t\t\titems: itemsByFolder(hubId: $h, folderId: $f, pagination: { %s }) { pagination { cursor } results { id } }", paging)
		}
		if !subsDone {
			paging := fmt.Sprintf("limit: %d", recountLimit)
			if subCur != "" {
				decl += ", $sc: String!"
				vars["sc"] = subCur
				paging += ", cursor: $sc"
			}
			body += fmt.Sprintf("\n\t\t\tsubs: foldersByFolderInHub(hubId: $h, folderId: $f, pagination: { %s }) { pagination { cursor } results { id } }", paging)
		}
		q := fmt.Sprintf("query AuditRecount(%s) {%s\n\t\t}", decl, body)

		var data struct {
			Folder *struct {
				ObjectCount *int `json:"objectCount"`
			} `json:"folder"`
			Items *idPage `json:"items"`
			Subs  *idPage `json:"subs"`
		}
		var lastErr error
		ok := false
		for try := 0; try < maxTaskTries && !ok; try++ {
			if try > 0 {
				if err := c.req.sleep(ctx, c.req.backoff(try)); err != nil {
					return 0, 0, 0, err
				}
			}
			resp, err := c.req.do(ctx, q, vars, recountCost)
			if err != nil {
				return 0, 0, 0, err
			}
			// A re-count has to be exact: any error at all, even on
			// one row, means this page cannot be counted.
			if len(resp.Errors) > 0 {
				lastErr = fmt.Errorf("%s", describe(resp.Errors[0]))
				continue
			}
			data.Folder, data.Items, data.Subs = nil, nil, nil
			if err := json.Unmarshal(resp.Data, &data); err != nil {
				lastErr = err
				continue
			}
			if (page == 0 && (data.Folder == nil || data.Folder.ObjectCount == nil)) ||
				(!itemsDone && data.Items == nil) || (!subsDone && data.Subs == nil) {
				lastErr = fmt.Errorf("unexpected response: %s", snippet(resp.Body))
				continue
			}
			ok = true
		}
		if !ok {
			return 0, 0, 0, lastErr
		}

		if page == 0 {
			count = *data.Folder.ObjectCount
		}
		if !itemsDone {
			for _, r := range data.Items.Results {
				if r == nil {
					return 0, 0, 0, fmt.Errorf("the list returned null in place of a document")
				}
				seenItems[r.ID] = true
			}
			itemCur = data.Items.Pagination.Cursor
			itemsDone = itemCur == ""
		}
		if !subsDone {
			for _, r := range data.Subs.Results {
				if r == nil {
					return 0, 0, 0, fmt.Errorf("the list returned null in place of a sub-folder")
				}
				seenSubs[r.ID] = true
			}
			subCur = data.Subs.Pagination.Cursor
			subsDone = subCur == ""
		}
	}
	return count, len(seenItems), len(seenSubs), nil
}

type idPage struct {
	Pagination struct {
		Cursor string `json:"cursor"`
	} `json:"pagination"`
	Results []*struct {
		ID string `json:"id"`
	} `json:"results"`
}

// ---------------------------------------------------------------------------
// Totals
// ---------------------------------------------------------------------------

func (c *crawler) finalize() {
	r := c.res
	byProject := map[string]*ProjectStats{}
	for _, p := range r.Projects {
		byProject[p.ID] = p
	}
	for _, f := range r.Findings {
		if p := byProject[f.ProjectID]; p != nil {
			p.Missing++
		}
		if f.Kind == KindNoModel {
			ext := f.Ext
			if ext == "" {
				ext = "(none)"
			}
			r.DesignsNoModelByExt[ext]++
		}
	}

	sort.SliceStable(r.Projects, func(i, j int) bool {
		return strings.ToLower(r.Projects[i].Name) < strings.ToLower(r.Projects[j].Name)
	})
	sort.SliceStable(r.Findings, func(i, j int) bool {
		a, b := r.Findings[i], r.Findings[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Path != b.Path {
			return strings.ToLower(a.Path) < strings.ToLower(b.Path)
		}
		if a.Name != b.Name {
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
		return a.ItemID < b.ItemID
	})
	sort.SliceStable(r.Folders, func(i, j int) bool {
		return strings.ToLower(r.Folders[i].Path) < strings.ToLower(r.Folders[j].Path)
	})
	sort.SliceStable(r.Unread, func(i, j int) bool {
		return strings.ToLower(r.Unread[i].Path) < strings.ToLower(r.Unread[j].Path)
	})

	r.Requests = c.req.Stats()
	r.Finished = c.req.now()
}
