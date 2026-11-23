// Package costwatch watches one project for newly saved designs and
// writes an Estimated Cost to each one's component.
package costwatch

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/schneik80/FusionDataCLI/api"
)

// TokenSource returns a usable access token; force asks for a new one
// after the API rejected the last.
type TokenSource func(ctx context.Context, force bool) (string, error)

// Part is what an Estimator prices.
type Part struct {
	Name        string
	Description string
	PartNumber  string
	Material    string
}

// Estimate is a price for one part and where it came from.
type Estimate struct {
	Price float64 // US dollars
	Note  string  // one line: the source, or why it is a guess
}

// Estimator prices one part.
type Estimator interface {
	Estimate(ctx context.Context, p Part) (Estimate, error)
}

// Event reports what happened to one design in one pass.
type Event struct {
	Time   time.Time
	Design string
	Action string // "priced", "would price", "skipped", "failed"
	Price  float64
	Detail string
}

// Options configure a watch.
type Options struct {
	HubID       string
	ProjectID   string
	Interval    time.Duration // time between passes
	Concurrency int           // designs priced and written at once
	Once        bool          // one pass, then return
	Reprice     bool          // also price designs that already have a cost
	DryRun      bool          // estimate, but write nothing
	// ClearAssemblies removes an Estimated Cost stored on a design that
	// has child components, so that its cost is the roll-up again.
	ClearAssemblies bool
	Estimator       Estimator
	Tokens          TokenSource
	OnEvent         func(Event)
	OnPass          func(designs, changed int)
}

// api calls, swapped in tests.
var (
	listDesigns = api.ListProjectDesigns
	getDesign   = api.GetDesignCost
	setCost     = api.SetEstimatedCost
	clearCost   = api.ClearEstimatedCost
)

// Run polls the project until ctx ends (or once, with Options.Once).
//
// A design is looked at when it is new or its save time changed since
// the last pass. Designs with child components are never priced: their
// cost is the roll-up of their children's. A part is priced when it has
// no Estimated Cost, or when
// its description differs from the one its cost was estimated from. A
// write of the cost can itself move the save time; the next pass then
// finds the same description and leaves the design alone.
func Run(ctx context.Context, o Options) error {
	if o.Concurrency < 1 {
		o.Concurrency = 1
	}
	w := newWatcher(o)
	for {
		if err := w.pass(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if o.Once {
				return err
			}
			w.emit(Event{Design: "(project)", Action: "failed", Detail: err.Error()})
		}
		if o.Once {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(o.Interval):
		}
	}
}

type watcher struct {
	o      Options
	mu     sync.Mutex
	seen   map[string]string // item id -> save time last handled
	priced map[string]string // item id -> description its cost stands for
	// cleared holds the assemblies whose stored cost this run removed. A
	// clear is tried once per design: if a cost still reads back after
	// it (a roll-up, say), clearing again on every save would loop.
	cleared map[string]bool
}

func newWatcher(o Options) *watcher {
	return &watcher{o: o, seen: map[string]string{}, priced: map[string]string{}, cleared: map[string]bool{}}
}

func (w *watcher) emit(e Event) {
	if w.o.OnEvent == nil {
		return
	}
	e.Time = time.Now()
	w.mu.Lock()
	defer w.mu.Unlock()
	w.o.OnEvent(e)
}

func (w *watcher) pass(ctx context.Context) error {
	token, err := w.o.Tokens(ctx, false)
	if err != nil {
		return err
	}
	rows, err := listDesigns(ctx, token, w.o.HubID, w.o.ProjectID)
	if err != nil {
		return err
	}
	var changed []api.DesignRow
	w.mu.Lock()
	for _, r := range rows {
		if at, ok := w.seen[r.ID]; !ok || at != r.ModifiedOn {
			changed = append(changed, r)
		}
	}
	w.mu.Unlock()
	if w.o.OnPass != nil {
		w.o.OnPass(len(rows), len(changed))
	}

	sem := make(chan struct{}, w.o.Concurrency)
	var wg sync.WaitGroup
	for _, r := range changed {
		wg.Add(1)
		sem <- struct{}{}
		go func(r api.DesignRow) {
			defer wg.Done()
			defer func() { <-sem }()
			if w.handle(ctx, r) {
				w.mu.Lock()
				w.seen[r.ID] = r.ModifiedOn
				w.mu.Unlock()
			}
		}(r)
	}
	wg.Wait()
	return nil
}

// handle prices one design if it needs it. It reports whether the
// design is settled; an unsettled one is tried again on the next pass.
func (w *watcher) handle(ctx context.Context, r api.DesignRow) bool {
	fail := func(err error) bool {
		if ctx.Err() == nil {
			w.emit(Event{Design: r.Name, Action: "failed", Detail: err.Error()})
		}
		return false
	}
	token, err := w.o.Tokens(ctx, false)
	if err != nil {
		return fail(err)
	}
	d, err := getDesign(ctx, token, w.o.HubID, r.ID)
	if err != nil {
		return fail(err)
	}
	switch {
	case d.ComponentID == "":
		w.emit(Event{Design: r.Name, Action: "skipped", Detail: "no model data"})
		return true
	case d.HasChildren:
		// An assembly's cost is the roll-up of its children's costs;
		// only leaf parts get a cost written.
		e := Event{Design: r.Name, Action: "skipped", Detail: "assembly; cost rolls up from its children"}
		if d.Cost == nil {
			w.emit(e)
			return true
		}
		e.Price = *d.Cost
		w.mu.Lock()
		done := w.cleared[r.ID]
		w.mu.Unlock()
		switch {
		case !w.o.ClearAssemblies || done:
			w.emit(e)
		case !d.Writable:
			e.Detail = "assembly with a stored cost; read-only for this user"
			w.emit(e)
		case w.o.DryRun:
			e.Action, e.Detail = "would clear", "assembly; stored cost would be removed"
			w.emit(e)
		default:
			if err := clearCost(ctx, token, d.ComponentID); err != nil {
				return fail(err)
			}
			w.mu.Lock()
			w.cleared[r.ID] = true
			w.mu.Unlock()
			e.Action, e.Detail = "cleared", "assembly; stored cost removed"
			w.emit(e)
		}
		return true
	case !d.Writable:
		w.emit(Event{Design: r.Name, Action: "skipped", Detail: "read-only for this user"})
		return true
	}

	w.mu.Lock()
	was, known := w.priced[r.ID]
	w.mu.Unlock()
	switch {
	case known && was == d.Description:
		// Our own write, or a save that left the description alone.
		return true
	case !known && d.Cost != nil && !w.o.Reprice:
		w.mu.Lock()
		w.priced[r.ID] = d.Description
		w.mu.Unlock()
		w.emit(Event{Design: r.Name, Action: "skipped", Price: *d.Cost, Detail: "already has a cost"})
		return true
	}

	est, err := w.o.Estimator.Estimate(ctx, Part{Name: d.Name, Description: d.Description, PartNumber: d.PartNumber, Material: d.Material})
	if err != nil {
		return fail(err)
	}
	if w.o.DryRun {
		w.emit(Event{Design: r.Name, Action: "would price", Price: est.Price, Detail: est.Note})
		return true
	}
	written, err := setCost(ctx, token, d.ComponentID, est.Price)
	if err != nil {
		return fail(err)
	}
	if written != est.Price {
		est.Note = fmt.Sprintf("%s (API stored %v)", est.Note, written)
	}
	w.mu.Lock()
	w.priced[r.ID] = d.Description
	w.mu.Unlock()
	w.emit(Event{Design: r.Name, Action: "priced", Price: written, Detail: est.Note})
	return true
}
