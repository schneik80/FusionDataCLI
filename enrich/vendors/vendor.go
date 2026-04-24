// Package vendors provides deep-link suggestions (and, where allowed, scraped
// results) for fastener vendors. The enrichment tool uses these to help the
// user locate a real vendor SKU for a parsed Fusion fastener, without
// automating any purchasing decision.
//
// Each vendor implements a Search method that takes a normalized fastener
// and returns zero or more Candidates. A Candidate may be:
//
//   - A partial suggestion with only a URL — the user clicks through to
//     the vendor's search page, filtered by the spec.
//   - A fully-populated suggestion with part number, price, and a direct
//     product URL. Only use this when the vendor's ToS permits it AND
//     scraping succeeds.
//
// The registry is static; add new vendors by writing another file in this
// package and appending to Registered() below.
package vendors

import (
	"context"

	"github.com/schneik80/FusionDataCLI/enrich"
)

// Candidate is one suggestion for a fastener from one vendor.
//
// For link-only suggestions (the default and safest mode), only Vendor +
// URL + MatchReasons are set; the consumer (UI) shows a clickable entry that
// takes the user to the vendor's page. For scraped/API candidates, the
// remaining fields are populated and Score is used to rank multiple hits.
type Candidate struct {
	Vendor       string   // e.g. "Bolt Depot"
	Manufacturer string   // brand if known, often blank for house brands
	PartNumber   string   // vendor SKU, empty for link-only candidates
	URL          string   // deep-link or product URL
	Price        float64  // unit price; 0 for link-only
	Currency     string   // ISO currency code, e.g. "USD"
	Score        float64  // 0..1 confidence; link-only suggestions use 0.5
	MatchReasons []string // e.g. ["std:exact","thread:exact","length:exact"]
}

// Vendor is one catalog source. Implementations should never panic; return
// (nil, nil) if the fastener is outside this vendor's coverage (e.g. imperial
// against a metric-only catalog).
type Vendor interface {
	Name() string
	Search(ctx context.Context, f *enrich.NormalizedFastener) ([]Candidate, error)
}

// Registered returns the built-in vendor list. Only Fastenal for now —
// the tool's UX is tuned for the user to search Fastenal manually, copy
// the SKU, and paste it back into the review form for auto-fill.
func Registered() []Vendor {
	return []Vendor{&Fastenal{}}
}

// SearchAll runs every registered vendor's Search concurrently and merges
// results. Vendor errors are swallowed — a single bad vendor should not
// take out the whole panel. Per-vendor errors are available by calling
// each Vendor directly.
func SearchAll(ctx context.Context, f *enrich.NormalizedFastener) []Candidate {
	vendors := Registered()
	type result struct{ cands []Candidate }
	ch := make(chan result, len(vendors))

	for _, v := range vendors {
		v := v
		go func() {
			cands, _ := v.Search(ctx, f)
			ch <- result{cands: cands}
		}()
	}

	var all []Candidate
	for range vendors {
		all = append(all, (<-ch).cands...)
	}
	return all
}
