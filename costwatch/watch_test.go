package costwatch

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/schneik80/FusionDataCLI/api"
)

// fakeAPI stands in for the three API calls the watcher makes.
type fakeAPI struct {
	mu      sync.Mutex
	rows    []api.DesignRow
	designs map[string]*api.DesignCost
	writes  map[string][]float64 // component id -> costs written
	clears  []string             // component ids whose cost was cleared
}

func (f *fakeAPI) install(t *testing.T) {
	t.Helper()
	l, g, s, c := listDesigns, getDesign, setCost, clearCost
	t.Cleanup(func() { listDesigns, getDesign, setCost, clearCost = l, g, s, c })
	clearCost = func(_ context.Context, _, comp string) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.clears = append(f.clears, comp)
		return nil
	}
	listDesigns = func(context.Context, string, string, string) ([]api.DesignRow, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return append([]api.DesignRow(nil), f.rows...), nil
	}
	getDesign = func(_ context.Context, _, _, id string) (*api.DesignCost, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		d := *f.designs[id]
		return &d, nil
	}
	setCost = func(_ context.Context, _, comp string, cost float64) (float64, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.writes == nil {
			f.writes = map[string][]float64{}
		}
		f.writes[comp] = append(f.writes[comp], cost)
		for _, d := range f.designs {
			if d.ComponentID == comp {
				c := cost
				d.Cost = &c
			}
		}
		return cost, nil
	}
}

type fixedEstimator struct {
	price float64
	err   error
}

func (e fixedEstimator) Estimate(context.Context, Part) (Estimate, error) {
	return Estimate{Price: e.price, Note: "fixed"}, e.err
}

func testWatcher(o Options) *watcher {
	o.Tokens = func(context.Context, bool) (string, error) { return "tok", nil }
	if o.Concurrency == 0 {
		o.Concurrency = 4
	}
	return newWatcher(o)
}

// A stored cost on an assembly is removed only when asked, never in a
// dry run, and once per run even if the design keeps changing.
func TestPass_ClearAssemblies(t *testing.T) {
	f := &fakeAPI{
		rows: []api.DesignRow{{ID: "asm", Name: "Asm", ModifiedOn: "t1"}, {ID: "clean", Name: "Clean", ModifiedOn: "t1"}},
		designs: map[string]*api.DesignCost{
			"asm":   {ComponentID: "c-asm", Writable: true, HasChildren: true, Cost: cost(9)},
			"clean": {ComponentID: "c-clean", Writable: true, HasChildren: true},
		},
	}
	f.install(t)
	for _, o := range []Options{{}, {ClearAssemblies: true, DryRun: true}} {
		o.Estimator = fixedEstimator{price: 1}
		if err := testWatcher(o).pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.clears) != 0 {
		t.Fatalf("cleared without being asked or in a dry run: %v", f.clears)
	}
	w := testWatcher(Options{ClearAssemblies: true, Estimator: fixedEstimator{price: 1}})
	for _, at := range []string{"t1", "t2"} {
		f.rows[0].ModifiedOn = at
		if err := w.pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.clears) != 1 || f.clears[0] != "c-asm" || len(f.writes) != 0 {
		t.Fatalf("clears = %v, writes = %v; want one clear of c-asm and no writes", f.clears, f.writes)
	}
}

func cost(v float64) *float64 { return &v }

func TestPass_PricesOnlyWhatNeedsIt(t *testing.T) {
	f := &fakeAPI{
		rows: []api.DesignRow{{ID: "new", Name: "New", ModifiedOn: "t1"}, {ID: "has", Name: "Has", ModifiedOn: "t1"}, {ID: "ro", Name: "RO", ModifiedOn: "t1"}, {ID: "asm", Name: "Asm", ModifiedOn: "t1"}, {ID: "bare", Name: "Bare", ModifiedOn: "t1"}},
		designs: map[string]*api.DesignCost{
			"new":  {ComponentID: "c-new", Writable: true, Description: "M3 screw"},
			"has":  {ComponentID: "c-has", Writable: true, Cost: cost(5)},
			"ro":   {ComponentID: "c-ro", Writable: false},
			"asm":  {ComponentID: "c-asm", Writable: true, HasChildren: true, Description: "gearbox"},
			"bare": {},
		},
	}
	f.install(t)
	w := testWatcher(Options{Estimator: fixedEstimator{price: 2.5}})
	if err := w.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.writes["c-asm"]) != 0 {
		t.Fatalf("assembly was priced: %v", f.writes)
	}
	if len(f.writes) != 1 || len(f.writes["c-new"]) != 1 || f.writes["c-new"][0] != 2.5 {
		t.Fatalf("writes = %v, want only c-new at 2.5", f.writes)
	}
}

// A cost write moves the design's save time. The next pass must look at
// the design again and leave it alone, or the watcher would loop.
func TestPass_OwnWriteDoesNotLoop(t *testing.T) {
	f := &fakeAPI{
		rows:    []api.DesignRow{{ID: "a", Name: "A", ModifiedOn: "t1"}},
		designs: map[string]*api.DesignCost{"a": {ComponentID: "c-a", Writable: true, Description: "bracket"}},
	}
	f.install(t)
	w := testWatcher(Options{Estimator: fixedEstimator{price: 9}})
	for _, at := range []string{"t1", "t2", "t2"} {
		f.rows[0].ModifiedOn = at
		if err := w.pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(f.writes["c-a"]); n != 1 {
		t.Fatalf("wrote %d times, want 1", n)
	}
}

func TestPass_NewDescriptionIsRepriced(t *testing.T) {
	f := &fakeAPI{
		rows:    []api.DesignRow{{ID: "a", Name: "A", ModifiedOn: "t1"}},
		designs: map[string]*api.DesignCost{"a": {ComponentID: "c-a", Writable: true, Description: "bracket"}},
	}
	f.install(t)
	w := testWatcher(Options{Estimator: fixedEstimator{price: 9}})
	if err := w.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.rows[0].ModifiedOn = "t2"
	f.designs["a"].Description = "steel bracket"
	if err := w.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(f.writes["c-a"]); n != 2 {
		t.Fatalf("wrote %d times, want 2", n)
	}
}

func TestPass_RepriceAndDryRun(t *testing.T) {
	f := &fakeAPI{
		rows:    []api.DesignRow{{ID: "has", Name: "Has", ModifiedOn: "t1"}},
		designs: map[string]*api.DesignCost{"has": {ComponentID: "c-has", Writable: true, Cost: cost(5)}},
	}
	f.install(t)
	var events []Event
	w := testWatcher(Options{Estimator: fixedEstimator{price: 7}, Reprice: true, DryRun: true, OnEvent: func(e Event) { events = append(events, e) }})
	if err := w.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.writes) != 0 {
		t.Fatalf("dry run wrote %v", f.writes)
	}
	if len(events) != 1 || events[0].Action != "would price" || events[0].Price != 7 {
		t.Fatalf("events = %+v", events)
	}

	w = testWatcher(Options{Estimator: fixedEstimator{price: 7}, Reprice: true})
	if err := w.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.writes["c-has"]; len(got) != 1 || got[0] != 7 {
		t.Fatalf("writes = %v, want one at 7", f.writes)
	}
}

// A design whose estimate failed is tried again on the next pass.
func TestPass_FailureIsRetried(t *testing.T) {
	f := &fakeAPI{
		rows:    []api.DesignRow{{ID: "a", Name: "A", ModifiedOn: "t1"}},
		designs: map[string]*api.DesignCost{"a": {ComponentID: "c-a", Writable: true}},
	}
	f.install(t)
	w := testWatcher(Options{Estimator: fixedEstimator{err: errors.New("boom")}})
	if err := w.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	w.o.Estimator = fixedEstimator{price: 3}
	if err := w.pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(f.writes["c-a"]); n != 1 {
		t.Fatalf("wrote %d times, want 1", n)
	}
}

func TestParseEstimate(t *testing.T) {
	cases := []struct {
		text  string
		price float64
		note  string
		bad   bool
	}{
		{"Found it.\nPRICE_USD: 12.5\nSOURCE: McMaster-Carr 91290A111", 12.5, "Claude: McMaster-Carr 91290A111", false},
		{"PRICE_USD: $1,234.567\nSOURCE: estimate from similar castings", 1234.57, "Claude: estimate from similar castings", false},
		{"first PRICE_USD: 1\nthen\nPRICE_USD: 2", 2, "Claude web search", false},
		{"I could not find a price.", 0, "", true},
		{"PRICE_USD: 0\nSOURCE: none", 0, "", true},
	}
	for _, c := range cases {
		est, err := parseEstimate(c.text)
		if (err != nil) != c.bad {
			t.Errorf("%q: err = %v, want error %v", c.text, err, c.bad)
			continue
		}
		if !c.bad && (est.Price != c.price || est.Note != c.note) {
			t.Errorf("%q: got %+v, want %v / %q", c.text, est, c.price, c.note)
		}
	}
}

func TestWithRandomFallback(t *testing.T) {
	est, err := WithRandomFallback(fixedEstimator{err: errors.New("key rejected")}).Estimate(context.Background(), Part{})
	if err != nil || est.Price < 0.5 || est.Price > 250 {
		t.Fatalf("got %+v, %v", est, err)
	}
	if est.Note != "random guess; key rejected" {
		t.Fatalf("note = %q", est.Note)
	}
}
