package ui

import (
	"sync"
	"time"

	"github.com/schneik80/FusionDataCLI/api"
)

// listingTTL is how long a project list or a container's contents are
// served from memory. Going back to the parent folder, or into a folder
// just left, is a revisit within seconds; without this each one re-sends
// two requests for a list that was on screen a moment ago. It is kept
// short because, unlike a document's details, a listing changes when
// anyone on the hub adds or moves a file. [r] always asks again.
const listingTTL = 2 * time.Minute

// listingCache remembers recent listings by container. Load commands
// run on their own goroutines, hence the lock; the Model, a value type,
// shares one cache through the pointer.
type listingCache struct {
	mu      sync.Mutex
	entries map[string]listing
	now     func() time.Time
}

type listing struct {
	items []api.NavItem
	at    time.Time
}

func newListingCache() *listingCache {
	return &listingCache{entries: make(map[string]listing), now: time.Now}
}

// get returns a copy of a fresh listing. The UI tags and re-orders the
// rows it is given, so the cached slice is never handed out.
func (c *listingCache) get(key string) ([]api.NavItem, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	if c.now().Sub(e.at) > listingTTL {
		delete(c.entries, key)
		return nil, false
	}
	return append([]api.NavItem(nil), e.items...), true
}

func (c *listingCache) put(key string, items []api.NavItem) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = listing{items: append([]api.NavItem(nil), items...), at: c.now()}
}

// clear forgets everything: a refresh, or a different account.
func (c *listingCache) clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]listing)
}
