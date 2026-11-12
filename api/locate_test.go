package api

import (
	"fmt"
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/schneik80/FusionDataCLI/internal/testutil"
)

// TestGetItemLocation_WalksFolderAncestry seeds a fake APS server that
// returns three folder levels then a null parent, and asserts that
// GetItemLocation returns the project metadata plus a root→leaf folder
// path (reversed from the leaf-first walk order).
func TestGetItemLocation_WalksFolderAncestry(t *testing.T) {
	var calls atomic.Int32
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		n := calls.Add(1)
		folder := func(id, name string, parent any) map[string]any {
			return map[string]any{"id": id, "name": name, "parentFolder": parent}
		}
		switch n {
		case 1:
			// One query: the project and the whole chain of parents.
			if !strings.Contains(req.Query, "LocateItem") {
				t.Errorf("call 1: expected LocateItem query, got %q", req.Query)
			}
			if got := strings.Count(req.Query, "parentFolder"); got != parentChainDepth {
				t.Errorf("query nests parentFolder %d deep, want %d", got, parentChainDepth)
			}
			return testutil.GraphQLResponse{Data: map[string]any{
				"item": map[string]any{
					"project": map[string]any{
						"id":   "P1",
						"name": "RobotLab",
						"hub":  map[string]any{"id": "H1"},
						"alternativeIdentifiers": map[string]any{
							"dataManagementAPIProjectId": "a.proj1",
						},
					},
					"parentFolder": folder("F-leaf", "Subassemblies",
						folder("F-mid", "Engineering",
							folder("F-root", "Top", nil))),
				},
			}}
		}
		t.Fatalf("unexpected extra call: %d (the chain ended inside the first answer)", n)
		return testutil.GraphQLResponse{}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetItemLocation(context.Background(), "tok", "H1", "I1")
	if err != nil {
		t.Fatalf("GetItemLocation: %v", err)
	}
	if got.HubID != "H1" || got.ProjectID != "P1" || got.ProjectName != "RobotLab" || got.ProjectAltID != "a.proj1" {
		t.Errorf("project metadata wrong: %+v", got)
	}
	want := []FolderRef{
		{ID: "F-root", Name: "Top"},
		{ID: "F-mid", Name: "Engineering"},
		{ID: "F-leaf", Name: "Subassemblies"},
	}
	if len(got.FolderPath) != len(want) {
		t.Fatalf("FolderPath len = %d, want %d (path=%+v)", len(got.FolderPath), len(want), got.FolderPath)
	}
	for i, w := range want {
		if got.FolderPath[i] != w {
			t.Errorf("FolderPath[%d] = %+v, want %+v", i, got.FolderPath[i], w)
		}
	}
}

// TestGetItemLocation_ProjectRootEmptyFolderPath confirms the empty-path
// case: an item that lives in the project root returns ItemLocation
// with an empty FolderPath (no walk queries fire).
func TestGetItemLocation_ProjectRootEmptyFolderPath(t *testing.T) {
	var calls atomic.Int32
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		calls.Add(1)
		return testutil.GraphQLResponse{Data: map[string]any{
			"item": map[string]any{
				"project": map[string]any{
					"id":                     "P1",
					"name":                   "Bare",
					"hub":                    map[string]any{"id": "H1"},
					"alternativeIdentifiers": map[string]any{"dataManagementAPIProjectId": "a.bare"},
				},
				"parentFolder": nil,
			},
		}}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetItemLocation(context.Background(), "tok", "H1", "I1")
	if err != nil {
		t.Fatalf("GetItemLocation: %v", err)
	}
	if calls.Load() != 1 {
		t.Errorf("call count = %d, want 1 (no folder walk for root item)", calls.Load())
	}
	if len(got.FolderPath) != 0 {
		t.Errorf("FolderPath = %+v, want empty", got.FolderPath)
	}
}

// TestGetItemLocation_DeepTreeContinues: a tree deeper than one query's
// chain is continued from the last folder the chain reached.
func TestGetItemLocation_DeepTreeContinues(t *testing.T) {
	var calls atomic.Int32
	chain := func(from, to int) any {
		var c any
		for i := to; i >= from; i-- {
			c = map[string]any{"id": fmt.Sprintf("F%d", i), "name": fmt.Sprintf("n%d", i), "parentFolder": c}
		}
		return c
	}
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		switch calls.Add(1) {
		case 1:
			return testutil.GraphQLResponse{Data: map[string]any{"item": map[string]any{
				"project":      map[string]any{"id": "P1", "name": "Deep", "hub": map[string]any{"id": "H1"}},
				"parentFolder": chain(1, parentChainDepth),
			}}}
		case 2:
			if got := req.Variables["folderId"]; got != fmt.Sprintf("F%d", parentChainDepth) {
				t.Errorf("continued from %v, want F%d", got, parentChainDepth)
			}
			return testutil.GraphQLResponse{Data: map[string]any{"folderByHubId": map[string]any{
				"parentFolder": chain(parentChainDepth+1, parentChainDepth+2),
			}}}
		}
		t.Fatalf("unexpected extra call")
		return testutil.GraphQLResponse{}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetItemLocation(context.Background(), "tok", "H1", "I1")
	if err != nil {
		t.Fatalf("GetItemLocation: %v", err)
	}
	if len(got.FolderPath) != parentChainDepth+2 || got.FolderPath[0].ID != fmt.Sprintf("F%d", parentChainDepth+2) || got.FolderPath[len(got.FolderPath)-1].ID != "F1" {
		t.Errorf("FolderPath = %+v, want %d folders from the root down to F1", got.FolderPath, parentChainDepth+2)
	}
}
