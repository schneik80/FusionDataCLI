package audit

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/schneik80/FusionDataCLI/api"
)

// sampleHub is a hub with one of everything the crawl has to get right.
func sampleHub(t *testing.T) *fakeHub {
	noModel := &fItem{id: "ipt-1", name: "Bracket.ipt", typ: "DesignItem", ext: "ipt", noData: true}
	flaky := design("flaky-1")
	flaky.flaky = true
	nullRow := &fItem{id: "ghost", nullRow: true}
	errored := design("err-1")
	errored.fieldErr = "model service unavailable"
	plain := &fItem{id: "basic-f3d", name: "Orphan.f3d", typ: "BasicItem", ext: "f3d"}
	pdf := &fItem{id: "pdf-1", name: "Spec.pdf", typ: "BasicItem", ext: "pdf"}
	dwgOK := &fItem{id: "dwg-ok", name: "Sheet.f2d", typ: "DrawingItem", ext: "f2d"}
	dwgBad := &fItem{id: "dwg-bad", name: "Lost.f2d", typ: "DrawingItem", ext: "f2d", noData: true}
	cfg := &fItem{id: "cfg-bad", name: "Config.f3d", typ: "ConfiguredDesignItem", ext: "f3d", noData: true}

	big := folder("big", designs("big", 400)) // several pages at the largest page size
	big.items[123].noData = true
	deep := folder("deep", []*fItem{design("deep-1"), noModel})
	short := folder("short", designs("short", 4))
	short.count = intp(5) // one object the listing does not return
	secret := folder("secret", designs("secret", 3))
	secret.forbidden = true
	empty := folder("empty", nil)

	return newFakeHub(t,
		project("Alpha", "ACTIVE", folder("", []*fItem{design("root-1"), flaky, nullRow, plain, pdf},
			folder("mixed", []*fItem{errored, dwgOK, dwgBad, cfg}, deep),
			big, short, secret, empty)),
		project("Beta", "ACTIVE", folder("", designs("beta", 30))),
		project("Old", "INACTIVE", folder("", designs("old", 2))),
	)
}

func findingKeys(r *Result) []string {
	var out []string
	for _, f := range r.Findings {
		id := f.ItemID
		if id == "" {
			id = "(no id)"
		}
		out = append(out, fmt.Sprintf("%s %s @ %s [%s]", f.Kind, id, f.Path, f.Recheck))
	}
	sort.Strings(out)
	return out
}

func TestCrawl_FindsEverythingAndNothingElse(t *testing.T) {
	h := sampleHub(t)
	r, err := h.crawl(Options{}, 4, 0)
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}

	want := []string{
		"configured-no-table cfg-bad @ Alpha / mixed [confirmed]",
		"design-no-model big-123 @ Alpha / big [confirmed]",
		"design-no-model err-1 @ Alpha / mixed [confirmed]",
		"design-no-model ipt-1 @ Alpha / mixed / deep [confirmed]",
		"drawing-no-data dwg-bad @ Alpha / mixed [confirmed]",
		"fusion-file-not-recognised basic-f3d @ Alpha [not rechecked]",
		"unreadable-row (no id) @ Alpha [not rechecked (no id to ask for)]",
	}
	if got := findingKeys(r); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("findings:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// The flaky design had no model in the list page and has one when
	// asked for by id: flagged, then cleared, never reported.
	if r.Cleared != 1 {
		t.Errorf("Cleared = %d, want 1 (the flaky design)", r.Cleared)
	}
	// An API error on the model is kept as the detail.
	for _, f := range r.Findings {
		if f.ItemID == "err-1" && !strings.Contains(f.Detail, "model service unavailable") {
			t.Errorf("err-1 detail = %q, want the API error", f.Detail)
		}
		if f.Kind == KindUnreadableRow && !strings.Contains(f.Detail, "corr-ghost") {
			t.Errorf("unreadable row detail = %q, want the correlation id", f.Detail)
		}
	}

	// Every document is counted exactly once: 5 at Alpha's root, 4 in
	// mixed, 2 in deep, 400 in big, 4 in short, 30 in Beta. The
	// forbidden folder's 3 are unread, the inactive project's 2 skipped.
	if r.TotalItems != 445 {
		t.Errorf("TotalItems = %d, want 445", r.TotalItems)
	}
	if r.TotalFolders != 6 {
		t.Errorf("TotalFolders = %d, want 6", r.TotalFolders)
	}
	if r.Duplicates != 0 {
		t.Errorf("Duplicates = %d, want 0", r.Duplicates)
	}

	// The folder that claims 5 objects and lists 4.
	if len(r.Folders) != 1 || r.Folders[0].FolderID != "short" || r.Folders[0].ObjectCount != 5 || r.Folders[0].Items != 4 {
		t.Errorf("folder findings = %+v, want one for \"short\" (5 claimed, 4 listed)", r.Folders)
	}
	if r.Folders[0].Note != "confirmed by re-count" {
		t.Errorf("folder note = %q", r.Folders[0].Note)
	}

	// The forbidden folder: both of its lists are reported, and the
	// report is marked incomplete.
	if r.Complete() {
		t.Error("Complete() = true with a forbidden folder")
	}
	if len(r.Unread) != 2 {
		t.Fatalf("Unread = %+v, want the two lists of the forbidden folder", r.Unread)
	}
	for _, u := range r.Unread {
		if u.ID != "secret" || !strings.Contains(u.Reason, "FORBIDDEN") {
			t.Errorf("unread = %+v", u)
		}
	}

	if len(r.Skipped) != 1 || r.Skipped[0].Name != "Old" {
		t.Errorf("Skipped = %+v, want the inactive project", r.Skipped)
	}
	if len(r.Projects) != 2 {
		t.Fatalf("Projects = %d, want 2", len(r.Projects))
	}
	alpha := r.Projects[0]
	if alpha.Name != "Alpha" || alpha.Items != 415 || alpha.Missing != 7 || alpha.Folders != 6 || alpha.FolderMismatches != 1 || alpha.Unread != 2 {
		t.Errorf("Alpha stats = %+v", *alpha)
	}
	if r.DesignsNoModelByExt["ipt"] != 1 || r.DesignsNoModelByExt["f3d"] != 2 {
		t.Errorf("DesignsNoModelByExt = %v", r.DesignsNoModelByExt)
	}
	if h.maxQueryCost > 1000 {
		t.Errorf("a query cost %d points; the API rejects anything over 1000", h.maxQueryCost)
	}
}

// TestCrawl_ThrottlingLosesNothing: the same hub, crawled while the API
// throttles and fails, must yield exactly the same report.
func TestCrawl_ThrottlingLosesNothing(t *testing.T) {
	clean, err := sampleHub(t).crawl(Options{}, 4, 0)
	if err != nil {
		t.Fatalf("clean crawl: %v", err)
	}

	h := sampleHub(t)
	h.throttleNext = 7
	h.serverErrs = 3
	h.quota = 3000 // far below the requester's default budget: constant 429s until it adapts
	rough, err := h.crawl(Options{}, 4, 0)
	if err != nil {
		t.Fatalf("throttled crawl: %v", err)
	}

	if a, b := strings.Join(findingKeys(clean), "\n"), strings.Join(findingKeys(rough), "\n"); a != b {
		t.Errorf("findings differ under throttling:\n%s\n--- vs clean ---\n%s", b, a)
	}
	if rough.TotalItems != clean.TotalItems || rough.TotalFolders != clean.TotalFolders || len(rough.Folders) != len(clean.Folders) || len(rough.Unread) != len(clean.Unread) {
		t.Errorf("totals differ: throttled %d docs / %d folders / %d mismatches / %d unread, clean %d / %d / %d / %d",
			rough.TotalItems, rough.TotalFolders, len(rough.Folders), len(rough.Unread),
			clean.TotalItems, clean.TotalFolders, len(clean.Folders), len(clean.Unread))
	}
	st := rough.Requests
	if st.Throttled < 7 {
		t.Errorf("Throttled = %d, want at least the 7 injected", st.Throttled)
	}
	if st.ServerErrors != 3 {
		t.Errorf("ServerErrors = %d, want 3", st.ServerErrors)
	}
	if st.GaveUp != 0 {
		t.Errorf("GaveUp = %d, want 0", st.GaveUp)
	}
	if st.PointsPerMinute >= DefaultPointsPerMinute {
		t.Errorf("point budget not lowered after throttling: %d", st.PointsPerMinute)
	}
	if st.MinConcurrency != 1 {
		t.Errorf("MinConcurrency = %d, want 1 after repeated throttling", st.MinConcurrency)
	}
}

// TestCrawl_PacesUnderTheQuota: with a budget below the API's quota the
// crawl must never be throttled at all.
func TestCrawl_PacesUnderTheQuota(t *testing.T) {
	h := newFakeHub(t, project("Big", "ACTIVE", folder("", nil,
		folder("a", designs("a", 1500)), folder("b", designs("b", 1500)), folder("c", designs("c", 1500)))))
	h.quota = 10000
	start := h.clock
	r, err := h.crawl(Options{}, 4, 8000)
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	if r.TotalItems != 4500 {
		t.Errorf("TotalItems = %d, want 4500", r.TotalItems)
	}
	if r.Requests.Throttled != 0 {
		t.Errorf("Throttled = %d, want 0: the budget is below the quota", r.Requests.Throttled)
	}
	// 4500 documents at 5 points each is ~22,500 points: more than two
	// minutes' budget, so the crawl has to have waited.
	if elapsed := h.clock.Sub(start); elapsed < 2*time.Minute {
		t.Errorf("crawl took %s of fake time; it cannot have paced itself", elapsed)
	}
	if r.Requests.PacingWait == 0 {
		t.Error("PacingWait = 0")
	}
}

// TestCrawl_TransientListFailureIsRetried: a list that fails twice and
// then answers is read in full and leaves nothing unread.
func TestCrawl_TransientListFailureIsRetried(t *testing.T) {
	h := newFakeHub(t, project("P", "ACTIVE", folder("", nil, folder("wobbly", designs("w", 12), folder("inner", designs("in", 3))))))
	h.listFails["wobbly/items"] = 2
	h.listFails["wobbly/folders"] = 1
	r, err := h.crawl(Options{}, 2, 0)
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	if !r.Complete() || r.TotalItems != 15 || r.TotalFolders != 2 {
		t.Errorf("after transient failures: complete=%v items=%d folders=%d unread=%+v", r.Complete(), r.TotalItems, r.TotalFolders, r.Unread)
	}
}

// TestCrawl_PersistentListFailureIsReported: a list that never answers
// is reported as unread — and the rest of the hub is still walked.
func TestCrawl_PersistentListFailureIsReported(t *testing.T) {
	h := newFakeHub(t, project("P", "ACTIVE", folder("", nil, folder("dead", designs("d", 5)), folder("fine", designs("f", 5)))))
	h.listFails["dead/items"] = 1000
	r, err := h.crawl(Options{}, 2, 0)
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	if r.Complete() || len(r.Unread) != 1 || r.Unread[0].ID != "dead" || r.Unread[0].What != "documents of folder" {
		t.Errorf("Unread = %+v, want the documents of \"dead\"", r.Unread)
	}
	if r.TotalItems != 5 {
		t.Errorf("TotalItems = %d, want the 5 in \"fine\"", r.TotalItems)
	}
	// A folder whose documents could not be read must not also be
	// reported as a count mismatch: that would be a second, wrong claim.
	if len(r.Folders) != 0 {
		t.Errorf("Folders = %+v, want none", r.Folders)
	}
}

// TestCrawl_BrokenDocumentDoesNotHideItsPage: the API answers a page
// holding a document whose data field it fails on with null results —
// an empty folder, to look at. The crawl must still count every
// document on the page and report the broken one, at a project root
// (which has no object count to give the loss away) as well as in a
// folder.
func TestCrawl_BrokenDocumentDoesNotHideItsPage(t *testing.T) {
	broken := func(id string) *fItem {
		return &fItem{id: id, name: id, typ: "ConfiguredDesignItem", ext: "f3d", poison: true}
	}
	root := append(designs("r", 6), broken("bad-root"))
	root = append(root, &fItem{id: "r-empty", name: "r-empty", typ: "DesignItem", ext: "f3d", noData: true})
	sub := append([]*fItem{broken("bad-sub")}, designs("s", 4)...)
	h := newFakeHub(t, project("P", "ACTIVE", folder("", root, folder("cfg", sub))))

	r, err := h.crawl(Options{}, 2, 0)
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	if !r.Complete() || len(r.Folders) != 0 {
		t.Errorf("complete=%v unread=%+v folders=%+v, want a complete report with no mismatch", r.Complete(), r.Unread, r.Folders)
	}
	if r.TotalItems != 13 {
		t.Errorf("TotalItems = %d, want 13", r.TotalItems)
	}
	got := map[string]Kind{}
	for _, f := range r.Findings {
		got[f.ItemID] = f.Kind
		if f.Recheck != "confirmed" {
			t.Errorf("%s: Recheck = %q, want confirmed", f.ItemID, f.Recheck)
		}
	}
	want := map[string]Kind{"bad-root": KindNoConfigTable, "bad-sub": KindNoConfigTable, "r-empty": KindNoModel}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("findings = %v, want %v", got, want)
	}
	for _, f := range r.Findings {
		if f.Kind == KindNoConfigTable && !strings.Contains(f.Detail, "Internal Server Error") {
			t.Errorf("%s: Detail = %q, want the API error", f.ItemID, f.Detail)
		}
	}
	// Documents looked up one by one are not flags, and finding them
	// healthy is not a withdrawal.
	if r.LeanPages != 2 || r.Isolated != 13 || r.Cleared != 0 || r.Flagged != 3 {
		t.Errorf("LeanPages=%d Isolated=%d Cleared=%d Flagged=%d, want 2, 13, 0, 3", r.LeanPages, r.Isolated, r.Cleared, r.Flagged)
	}
}

func TestCrawl_ProjectFilter(t *testing.T) {
	h := sampleHub(t)
	r, err := h.crawl(Options{Projects: []string{"beta"}}, 2, 0)
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	if len(r.Projects) != 1 || r.Projects[0].Name != "Beta" || r.TotalItems != 30 || len(r.Skipped) != 0 {
		t.Errorf("filtered crawl: projects=%d items=%d skipped=%d", len(r.Projects), r.TotalItems, len(r.Skipped))
	}

	if _, err := sampleHub(t).crawl(Options{Projects: []string{"Nope"}}, 2, 0); err == nil || !strings.Contains(err.Error(), "Nope") {
		t.Errorf("unknown project: err = %v, want it named", err)
	}
}

func TestCrawl_IncludeInactive(t *testing.T) {
	r, err := sampleHub(t).crawl(Options{IncludeInactive: true}, 2, 0)
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	if len(r.Skipped) != 0 || len(r.Projects) != 3 || r.TotalItems != 447 {
		t.Errorf("skipped=%d projects=%d items=%d, want 0 / 3 / 447", len(r.Skipped), len(r.Projects), r.TotalItems)
	}
}

// TestCrawl_OversizedQueryIsSplit: if a batch is refused as too costly,
// its lists are retried alone with smaller pages, not dropped.
func TestCrawl_OversizedQueryIsSplit(t *testing.T) {
	h := newFakeHub(t, project("P", "ACTIVE", folder("", nil, folder("f", designs("f", 300)))))
	req := h.requester(2, 0)
	inner := req.query
	refused := 0
	// Pretend the API prices this account's queries higher: refuse any
	// page over 100 rows.
	req.query = func(ctx context.Context, tok, q string, vars map[string]any) (*api.RawResponse, error) {
		if strings.Contains(q, "limit: 170") {
			refused++
			return decode(400, []byte(`{"errors":[{"message":"Query point value 1011 exceeds maximum allowed query point value 1000.","extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]}`)), nil
		}
		return inner(ctx, tok, q, vars)
	}
	r, err := newCrawler(req, Options{HubID: "HUB", Concurrency: 2}).run(context.Background())
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	if refused == 0 {
		t.Fatal("the oversized query was never sent; the test proves nothing")
	}
	if !r.Complete() || r.TotalItems != 300 {
		t.Errorf("complete=%v items=%d unread=%+v, want all 300 read", r.Complete(), r.TotalItems, r.Unread)
	}
}

func TestLocate(t *testing.T) {
	cases := []struct {
		path       []any
		alias, row int
		ok         bool
	}{
		{[]any{"t3"}, 3, -1, true},
		{[]any{"t12", "results"}, 12, -1, true},
		{[]any{"t0", "results", float64(7)}, 0, 7, true},
		{[]any{"t0", "results", float64(7), "tipRootModel"}, 0, 7, true},
		{[]any{"t1", "pagination", "cursor"}, 1, -1, true},
		{nil, 0, 0, false},
		{[]any{"hub"}, 0, 0, false},
		{[]any{float64(1)}, 0, 0, false},
	}
	for _, c := range cases {
		alias, row, ok := locate(c.path)
		if ok != c.ok || (ok && (alias != c.alias || row != c.row)) {
			t.Errorf("locate(%v) = %d, %d, %v; want %d, %d, %v", c.path, alias, row, ok, c.alias, c.row, c.ok)
		}
	}
}
