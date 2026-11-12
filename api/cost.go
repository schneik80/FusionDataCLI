package api

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// Pre-flight pricing. A query's point value is fixed by its text — the
// fields it selects and the page sizes written into it — and not by
// what comes back (docs/throttling.md), so the cost of a request is
// known before it is sent: exactly, once the API has answered the same
// text once, and from the table below until then. gqlQuery uses the
// price to hold a request that would overdraw the minute instead of
// sending it into an HTTP 429.

// unknownOpCost is charged for a query the table does not know and the
// API has not priced yet.
const unknownOpCost = 100

// maxLearnedCosts bounds the learned prices. Query texts are a small
// fixed set, apart from the aliased batches whose text varies with the
// number of rows; the bound only guards against that growing forever.
const maxLearnedCosts = 512

// opSeeds prices a query by its operation name before the API has
// answered it: the measured cost of the query as written, or a function
// of the text for a query whose size varies per call. A seed only has
// to be close: the API's own number replaces it after the first answer.
var opSeeds = map[string]func(q string) int{
	"GetHubs":                  fixed(11),
	"GetHubsNext":              fixed(11),
	"GetProjects":              fixed(316),
	"GetProjectsNext":          fixed(316),
	"GetFolders":               fixed(11),
	"GetFoldersNext":           fixed(11),
	"GetSubFolders":            fixed(11),
	"GetSubFoldersNext":        fixed(11),
	"GetItems":                 fixed(11),
	"GetItemsNext":             fixed(11),
	"GetProjectItems":          fixed(11),
	"GetProjectItemsNext":      fixed(11),
	"ClassifyContents":         perRow(11, 11),
	"GetItemDetails":           fixed(48),
	"GetItemHistoryNext":       fixed(67),
	"GetOccurrences":           fixed(27),
	"GetOccurrencesNext":       fixed(27),
	"GetWhereUsed":             fixed(327),
	"GetWhereUsedNext":         fixed(800),
	"WhereUsedIsDirect":        perAlias("model(", 12),
	"GetDrawingsForDesign":     fixed(76),
	"GetDrawingsForDesignNext": fixed(316),
	"LocateItem":               fixed(19),
	"GetFolderParent":          fixed(11),
}

func fixed(points int) func(string) int { return func(string) int { return points } }

var limitRE = regexp.MustCompile(`limit:\s*(\d+)`)

// perRow prices a list by the first page size written in its text.
func perRow(base, each int) func(string) int {
	return func(q string) int {
		n := 0
		if m := limitRE.FindStringSubmatch(q); m != nil {
			n, _ = strconv.Atoi(m[1])
		}
		return base + each*n
	}
}

// perAlias prices a query built from one aliased root field per row.
func perAlias(field string, each int) func(string) int {
	return func(q string) int { return each * strings.Count(q, field) }
}

var opNameRE = regexp.MustCompile(`^\s*query\s+([A-Za-z_][A-Za-z0-9_]*)`)

// operationName returns the name a query declares, or "".
func operationName(q string) string {
	if m := opNameRE.FindStringSubmatch(q); m != nil {
		return m[1]
	}
	return ""
}

type costBook struct {
	mu      sync.Mutex
	learned map[string]int // query text → points the API reported
}

var costs = &costBook{learned: make(map[string]int)}

// estimate is the price of q before it is sent.
func (b *costBook) estimate(q string) int {
	b.mu.Lock()
	points, ok := b.learned[q]
	b.mu.Unlock()
	if ok {
		return points
	}
	if seed, ok := opSeeds[operationName(q)]; ok {
		if points := seed(q); points > 0 {
			return points
		}
	}
	return unknownOpCost
}

// observe records what the API charged for q.
func (b *costBook) observe(q string, points int) {
	if points <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.learned[q]; !ok && len(b.learned) >= maxLearnedCosts {
		b.learned = make(map[string]int)
	}
	b.learned[q] = points
}
