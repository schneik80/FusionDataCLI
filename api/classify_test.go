package api

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/schneik80/FusionDataCLI/internal/testutil"
)

func classifyPage(cursor string, rows ...map[string]any) testutil.GraphQLResponse {
	return testutil.GraphQLResponse{Data: map[string]any{
		"list": map[string]any{
			"pagination": map[string]any{"cursor": cursor},
			"results":    rows,
		},
	}}
}

func designRow(id, model string, children int) map[string]any {
	rel := make([]map[string]any, children)
	for i := range rel {
		rel[i] = map[string]any{"id": "rel"}
	}
	return map[string]any{"__typename": "DesignItem", "id": id,
		"tipRootModel": map[string]any{"id": model, "assemblyRelations": map[string]any{"results": rel}}}
}

// TestClassifyContents_Folder: one request tags a page of a folder's
// designs. Rows that are not designs, designs with no model, and
// designs whose relations could not be read are left out — never
// called a part.
func TestClassifyContents_Folder(t *testing.T) {
	calls := 0
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		calls++
		// depth: 1 keeps the probe to direct children; without it the
		// API walks the whole tree to answer a yes/no question.
		for _, want := range []string{"itemsByFolder(hubId: $hubId, folderId: $id", "limit: 7", "assemblyRelations(depth: 1, pagination: { limit: 1 })"} {
			if !strings.Contains(req.Query, want) {
				t.Errorf("query missing %q:\n%s", want, req.Query)
			}
		}
		if strings.Contains(req.Query, "cursor") && !strings.Contains(req.Query, "pagination { cursor }") {
			t.Errorf("first page must not send a cursor:\n%s", req.Query)
		}
		if req.Variables["hubId"] != "h1" || req.Variables["id"] != "f1" {
			t.Errorf("variables = %v", req.Variables)
		}
		return classifyPage("next-1",
			// limit is a page-size hint in v3: asking for 1 relation can
			// return more. Any result at all means assembly.
			designRow("asm", "m-asm", 2),
			designRow("part", "m-part", 0),
			map[string]any{"__typename": "DesignItem", "id": "no-model", "tipRootModel": nil},
			map[string]any{"__typename": "DesignItem", "id": "no-rel", "tipRootModel": map[string]any{"id": "m-x", "assemblyRelations": nil}},
			map[string]any{"__typename": "DrawingItem", "id": "dwg"},
		)
	})
	swapEndpoint(t, srv.URL)

	got, next, err := ClassifyContents(context.Background(), "tok", "h1", "f1", false, 7, "")
	if err != nil {
		t.Fatalf("ClassifyContents: %v", err)
	}
	want := []Classification{
		{ItemID: "asm", RootModelID: "m-asm", IsAssembly: true},
		{ItemID: "part", RootModelID: "m-part", IsAssembly: false},
	}
	if !reflect.DeepEqual(got, want) || next != "next-1" || calls != 1 {
		t.Errorf("got %+v next=%q calls=%d, want %+v next-1 in 1 call", got, next, calls, want)
	}
}

// TestClassifyContents_ProjectAndCursor: a project root is listed with
// itemsByProject, a cursor continues a previous page, and the page size
// is capped so one query stays under the point limit.
func TestClassifyContents_ProjectAndCursor(t *testing.T) {
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		for _, want := range []string{"itemsByProject(projectId: $id", "cursor: $cursor, limit: 50"} {
			if !strings.Contains(req.Query, want) {
				t.Errorf("query missing %q:\n%s", want, req.Query)
			}
		}
		if strings.Contains(req.Query, "$hubId") {
			t.Errorf("project query must not declare $hubId:\n%s", req.Query)
		}
		if req.Variables["id"] != "p1" || req.Variables["cursor"] != "c1" {
			t.Errorf("variables = %v", req.Variables)
		}
		return classifyPage("", designRow("d", "m", 0))
	})
	swapEndpoint(t, srv.URL)

	got, next, err := ClassifyContents(context.Background(), "tok", "h1", "p1", true, 500, "c1")
	if err != nil || len(got) != 1 || next != "" {
		t.Errorf("got %+v next=%q err=%v", got, next, err)
	}
}

func TestClassifyContents_Errors(t *testing.T) {
	if _, _, err := ClassifyContents(context.Background(), "tok", "h", "", false, 10, ""); err == nil {
		t.Error("empty container id: want an error")
	}
	srv := testutil.GraphQLServer(t, func(testutil.GraphQLRequest) testutil.GraphQLResponse {
		return testutil.GraphQLResponse{Errors: []string{"boom"}}
	})
	swapEndpoint(t, srv.URL)
	if _, _, err := ClassifyContents(context.Background(), "tok", "h", "f", false, 10, ""); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v, want the GraphQL error", err)
	}
}
