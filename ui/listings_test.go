package ui

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/schneik80/FusionDataCLI/api"
	"github.com/schneik80/FusionDataCLI/internal/testutil"
)

// TestListingCache: a listing is served again within the TTL, as a copy,
// and not after it or after clear.
func TestListingCache(t *testing.T) {
	c := newListingCache()
	now := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return now }

	c.put("folder:h:f", []api.NavItem{{ID: "a", Kind: "design"}})
	got, ok := c.get("folder:h:f")
	if !ok || len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("get = %v, %v; want the listing", got, ok)
	}
	got[0].Subtype = "assembly" // the UI tags its rows in place
	if again, _ := c.get("folder:h:f"); again[0].Subtype != "" {
		t.Error("a caller's change reached the cached listing")
	}
	if _, ok := c.get("folder:h:other"); ok {
		t.Error("a different container was served")
	}

	now = now.Add(listingTTL + time.Second)
	if _, ok := c.get("folder:h:f"); ok {
		t.Error("a listing was served after its TTL")
	}

	c.put("folder:h:f", nil)
	c.clear()
	if _, ok := c.get("folder:h:f"); ok {
		t.Error("a listing survived clear")
	}

	var none *listingCache // a Model built without one still works
	none.put("k", nil)
	none.clear()
	if _, ok := none.get("k"); ok {
		t.Error("a nil cache served a listing")
	}
}

// TestLoadItemsCmd_RevisitAsksOnce: opening a folder twice within the
// TTL sends its two requests once; after a refresh it asks again.
func TestLoadItemsCmd_RevisitAsksOnce(t *testing.T) {
	var requests atomic.Int32
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		requests.Add(1)
		list := map[string]any{"pagination": map[string]any{"cursor": nil}, "results": []map[string]any{}}
		return testutil.GraphQLResponse{Data: map[string]any{"itemsByFolder": list, "foldersByFolderInHub": list}}
	})
	t.Cleanup(api.SetGraphqlEndpointForTesting(srv.URL))

	cache := newListingCache()
	for i := 0; i < 2; i++ {
		msg, ok := loadItemsCmd(cache, "tok", "hub-1", "folder-1")().(contentsLoadedMsg)
		if !ok || msg.containerID != "folder-1" {
			t.Fatalf("load %d = %#v, want the folder's contents", i, msg)
		}
	}
	if requests.Load() != 2 {
		t.Errorf("requests = %d, want 2 (documents and sub-folders, once)", requests.Load())
	}
	cache.clear()
	loadItemsCmd(cache, "tok", "hub-1", "folder-1")()
	if requests.Load() != 4 {
		t.Errorf("requests after a refresh = %d, want 4", requests.Load())
	}
}
