package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/schneik80/FusionDataCLI/internal/testutil"
)

// prop wraps a string the way the v3 Property type does.
func prop(v string) map[string]any { return map[string]any{"value": v} }

func TestGetOccurrences_DecodesAndPaginates(t *testing.T) {
	calls := 0
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		calls++
		// depth: 1 is what keeps this to direct children — the API
		// default is the whole tree.
		if !strings.Contains(req.Query, "assemblyRelations(depth: 1, pagination") {
			t.Errorf("query missing depth-1 assemblyRelations: %q", req.Query)
		}
		if got, _ := req.Variables["modelId"].(string); got != "M1" {
			t.Errorf("modelId variable = %v, want \"M1\"", req.Variables["modelId"])
		}
		switch calls {
		case 1:
			if _, has := req.Variables["cursor"]; has {
				t.Errorf("first call should not carry a cursor: %v", req.Variables)
			}
			return testutil.GraphQLResponse{Data: map[string]any{
				"model": map[string]any{
					"assemblyRelations": map[string]any{
						"pagination": map[string]any{"cursor": "P2"},
						"results": []map[string]any{
							{
								"id": "rel1",
								"toModel": map[string]any{
									"id":           "mA",
									"name":         prop("BoltA"),
									"materialName": prop("Steel"),
									"component": map[string]any{
										"partNumber":  prop("PN-1"),
										"description": prop("M3 bolt"),
									},
									"designItem": map[string]any{
										"id":           "diA",
										"name":         "BoltA.f3d",
										"fusionWebUrl": "https://x/a",
									},
								},
							},
						},
					},
				},
			}}
		case 2:
			if req.Variables["cursor"] != "P2" {
				t.Errorf("second call cursor = %v, want P2", req.Variables["cursor"])
			}
			return testutil.GraphQLResponse{Data: map[string]any{
				"model": map[string]any{
					"assemblyRelations": map[string]any{
						"pagination": map[string]any{"cursor": nil},
						"results": []map[string]any{
							// Sparse row: every nested object is nullable.
							{"id": "rel2", "toModel": map[string]any{"id": "mB", "name": prop("Washer")}},
							// A relation whose target model is gone is skipped.
							{"id": "rel3", "toModel": nil},
						},
					},
				},
			}}
		}
		t.Fatalf("unexpected extra call: %d", calls)
		return testutil.GraphQLResponse{}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetOccurrences(context.Background(), "tok", "M1")
	if err != nil {
		t.Fatalf("GetOccurrences: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	want := ComponentRef{
		ID: "mA", Name: "BoltA", PartNumber: "PN-1", PartDesc: "M3 bolt", Material: "Steel",
		DesignItemID: "diA", DesignItemName: "BoltA.f3d", FusionWebURL: "https://x/a",
	}
	if got[0] != want {
		t.Errorf("first ref = %+v, want %+v", got[0], want)
	}
	if got[1] != (ComponentRef{ID: "mB", Name: "Washer"}) {
		t.Errorf("second ref decoded wrong: %+v", got[1])
	}
}

func TestGetWhereUsed_DecodesItems(t *testing.T) {
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		if !strings.Contains(req.Query, "whereUsed(pagination") {
			t.Errorf("query missing whereUsed field: %q", req.Query)
		}
		// v3 where-used hangs off the item, not a component version.
		if req.Variables["hubId"] != "H1" || req.Variables["itemId"] != "I1" {
			t.Errorf("variables = %v, want hubId=H1 itemId=I1", req.Variables)
		}
		return testutil.GraphQLResponse{Data: map[string]any{
			"item": map[string]any{
				"whereUsed": map[string]any{
					"pagination": map[string]any{"cursor": nil},
					"results": []map[string]any{
						{
							"__typename":   "DesignItem",
							"id":           "di-robot",
							"name":         "Robot",
							"fusionWebUrl": "https://x/robot",
							"tipRootModel": map[string]any{
								"materialName": prop("Aluminum"),
								"component": map[string]any{
									"partNumber":  prop("RB-1"),
									"description": prop("The robot"),
								},
							},
						},
						{
							"__typename":   "DrawingItem",
							"id":           "dw-robot",
							"name":         "Robot Drawing",
							"fusionWebUrl": "https://x/robot-dwg",
						},
						// Design whose root model is unreadable.
						{"__typename": "DesignItem", "id": "di-bare", "name": "Bare", "tipRootModel": nil},
					},
				},
			},
		}}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetWhereUsed(context.Background(), "tok", "H1", "I1")
	if err != nil {
		t.Fatalf("GetWhereUsed: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3; got %+v", len(got), got)
	}
	wantDesign := ComponentRef{
		ID: "di-robot", Name: "Robot", PartNumber: "RB-1", PartDesc: "The robot", Material: "Aluminum",
		DesignItemID: "di-robot", DesignItemName: "Robot", FusionWebURL: "https://x/robot", Kind: "design",
	}
	if got[0] != wantDesign {
		t.Errorf("design row = %+v, want %+v", got[0], wantDesign)
	}
	// Drawings are where-used results too; Kind is how the UI tells.
	wantDrawing := ComponentRef{
		ID: "dw-robot", Name: "Robot Drawing",
		DesignItemID: "dw-robot", DesignItemName: "Robot Drawing", FusionWebURL: "https://x/robot-dwg", Kind: "drawing",
	}
	if got[1] != wantDrawing {
		t.Errorf("drawing row = %+v, want %+v", got[1], wantDrawing)
	}
	if got[2].ID != "di-bare" || got[2].Kind != "design" || got[2].PartNumber != "" {
		t.Errorf("bare design row decoded wrong: %+v", got[2])
	}
}

func TestGetWhereUsed_PaginatesAndDedupes(t *testing.T) {
	row := func(id string) map[string]any {
		return map[string]any{"__typename": "DesignItem", "id": id, "name": id}
	}
	calls := 0
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		calls++
		results := []map[string]any{row("a"), row("b")}
		cursor := any("P2")
		if calls == 2 {
			if req.Variables["cursor"] != "P2" {
				t.Errorf("second call cursor = %v, want P2", req.Variables["cursor"])
			}
			// "b" repeats across the page boundary.
			results, cursor = []map[string]any{row("b"), row("c")}, nil
		}
		if calls > 2 {
			t.Fatalf("unexpected extra call: %d", calls)
		}
		return testutil.GraphQLResponse{Data: map[string]any{
			"item": map[string]any{"whereUsed": map[string]any{
				"pagination": map[string]any{"cursor": cursor},
				"results":    results,
			}},
		}}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetWhereUsed(context.Background(), "tok", "H1", "I1")
	if err != nil {
		t.Fatalf("GetWhereUsed: %v", err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "a,b,c" {
		t.Errorf("ids = %v, want [a b c] (repeat across pages collapsed, order kept)", ids)
	}
}

// whereUsedFixture answers both halves of a Where Used load for item
// I1: the row query, and the batched direct/indirect check.
//
//	top        used only by a drawing → root; reaches I1 through "parent"
//	top-dwg    drawing of "top"       → indirect
//	broken     its relation check fails
//	parent     holds I1 directly
//	own-dwg    drawing of I1 itself   → direct (it is in I1's drawingItems)
//	parent-p2  holds I1 directly, found on the second page of relations
//	stale      listed by whereUsed, but its current version references nothing
func whereUsedFixture(t *testing.T, batchSizes *[]int) func(testutil.GraphQLRequest) testutil.GraphQLResponse {
	// parents is the row's own where-used: the typenames it holds and
	// whether the page was cut short (a cursor is left).
	design := func(id, modelID string, more bool, parents ...string) map[string]any {
		results := []map[string]any{}
		for _, tn := range parents {
			results = append(results, map[string]any{"__typename": tn})
		}
		var cursor any
		if more {
			cursor = "MORE"
		}
		return map[string]any{"__typename": "DesignItem", "id": id, "name": id,
			"tipRootModel": map[string]any{"id": modelID},
			"whereUsed":    map[string]any{"pagination": map[string]any{"cursor": cursor}, "results": results}}
	}
	drawing := func(id string) map[string]any {
		return map[string]any{"__typename": "DrawingItem", "id": id, "name": id}
	}
	// rels is one parent's page of first-level references.
	rels := func(cursor any, designIDs ...string) map[string]any {
		var results []map[string]any
		for _, id := range designIDs {
			results = append(results, map[string]any{"toModel": map[string]any{"designItem": map[string]any{"id": id}}})
		}
		// A relation with no readable target must not break the check.
		results = append(results, map[string]any{"toModel": nil})
		return map[string]any{
			"assemblyRelations": map[string]any{"pagination": map[string]any{"cursor": cursor}, "results": results},
		}
	}
	var mu sync.Mutex
	return func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		if strings.Contains(req.Query, "whereUsed(") {
			// Drawing relations come from the design's own drawings
			// list, never from asking each drawing for its model.
			if !strings.Contains(req.Query, "drawingItems(") || strings.Contains(req.Query, "tipDrawing") {
				t.Errorf("row query should read drawingItems and not tipDrawing: %q", req.Query)
			}
			return testutil.GraphQLResponse{Data: map[string]any{"item": map[string]any{
				"drawingItems": map[string]any{
					"pagination": map[string]any{"cursor": nil},
					"results":    []map[string]any{{"id": "own-dwg"}},
				},
				"whereUsed": map[string]any{
					"pagination": map[string]any{"cursor": nil},
					"results": []map[string]any{
						design("top", "m-top", false, "DrawingItem"),
						drawing("top-dwg"),
						design("broken", "m-broken", false),
						design("parent", "m-parent", false, "DrawingItem", "DesignItem"),
						drawing("own-dwg"),
						// Only drawings so far, but the list was cut short.
						design("parent-p2", "m-parent-p2", true, "DrawingItem"),
						design("stale", "m-stale", false),
					},
				}}}}
		}
		if !strings.Contains(req.Query, "assemblyRelations(scope: INTERNAL_PLUS_DIRECT_EXTERNAL") {
			t.Errorf("relation check must use the direct-external scope: %q", req.Query)
		}
		// The parents arrive aliased p0, p1, … with variables m0, m1, …
		// and, from the second page on, c0, c1, ….
		data := map[string]any{}
		n := 0
		for ; ; n++ {
			model, ok := req.Variables[fmt.Sprintf("m%d", n)]
			if !ok {
				break
			}
			alias := fmt.Sprintf("p%d", n)
			switch model {
			case "m-top":
				data[alias] = rels(nil, "parent", "other")
			case "m-parent":
				data[alias] = rels(nil, "other", "I1")
			case "m-parent-p2":
				// The match is on the second page.
				if req.Variables[fmt.Sprintf("c%d", n)] == "P2" {
					data[alias] = rels(nil, "I1")
				} else {
					data[alias] = rels("P2", "other")
				}
			case "m-stale":
				data[alias] = map[string]any{"assemblyRelations": map[string]any{
					"pagination": map[string]any{"cursor": nil}, "results": []map[string]any{}}}
			case "m-broken":
				// One unreadable parent fails the whole request.
				mu.Lock()
				*batchSizes = append(*batchSizes, -1)
				mu.Unlock()
				return testutil.GraphQLResponse{Errors: []string{"boom"}}
			default:
				t.Errorf("unexpected relation check: %v", req.Variables)
			}
		}
		mu.Lock()
		*batchSizes = append(*batchSizes, n)
		mu.Unlock()
		return testutil.GraphQLResponse{Data: data}
	}
}

func whereUsedRows(refs []ComponentRef) string {
	var rows []string
	for _, r := range refs {
		row := r.ID + "=" + r.Relation
		if r.Root {
			row += "(root)"
		}
		rows = append(rows, row)
	}
	return strings.Join(rows, ",")
}

// TestGetWhereUsed_RowsFirst: the row query alone tags drawings (from
// the design's own drawings list) and roots, and leaves design rows
// untagged for GetWhereUsedRelations — without making that request.
func TestGetWhereUsed_RowsFirst(t *testing.T) {
	var batchSizes []int
	srv := testutil.GraphQLServer(t, whereUsedFixture(t, &batchSizes))
	swapEndpoint(t, srv.URL)

	got, err := GetWhereUsed(context.Background(), "tok", "H1", "I1")
	if err != nil {
		t.Fatalf("GetWhereUsed: %v", err)
	}
	// Root: no design among the row's own parents, and only when that
	// list is complete ("parent-p2" was cut short).
	want := "own-dwg=direct,top=(root),broken=(root),parent=,parent-p2=,stale=(root),top-dwg=indirect"
	if rows := whereUsedRows(got); rows != want {
		t.Errorf("rows = %s\nwant   %s", rows, want)
	}
	if len(batchSizes) != 0 {
		t.Errorf("GetWhereUsed made %d relation requests, want none", len(batchSizes))
	}
	if !NeedsWhereUsedRelations(got) {
		t.Error("NeedsWhereUsedRelations = false with untagged design rows")
	}
}

// TestGetWhereUsedRelations_LabelsDirectAndIndirect: v3 whereUsed mixes
// immediate parents with assemblies further up the tree. Each design
// row is checked against its own first-level references and the list
// comes back direct first, without modifying the rows passed in.
func TestGetWhereUsedRelations_LabelsDirectAndIndirect(t *testing.T) {
	var batchSizes []int
	srv := testutil.GraphQLServer(t, whereUsedFixture(t, &batchSizes))
	swapEndpoint(t, srv.URL)

	rows, err := GetWhereUsed(context.Background(), "tok", "H1", "I1")
	if err != nil {
		t.Fatalf("GetWhereUsed: %v", err)
	}
	before := whereUsedRows(rows)
	got := GetWhereUsedRelations(context.Background(), "tok", "I1", rows)

	// Direct, then undetermined (the failed check, and the parent whose
	// current version references nothing), then indirect.
	want := "own-dwg=direct,parent=direct,parent-p2=direct,broken=(root),stale=,top=indirect(root),top-dwg=indirect"
	if r := whereUsedRows(got); r != want {
		t.Errorf("rows = %s\nwant   %s", r, want)
	}
	if whereUsedRows(rows) != before {
		t.Errorf("input rows were modified: %s", whereUsedRows(rows))
	}

	// All five parents go out in one request. It fails on the broken
	// parent, so each is retried alone (4 answers + 1 failure), and the
	// parent with a second page takes one more request.
	sort.Ints(batchSizes)
	if got := fmt.Sprint(batchSizes); got != "[-1 -1 1 1 1 1 1]" {
		t.Errorf("relation requests = %s, want one failed batch, one failed single, five singles", got)
	}
}

// TestGetWhereUsed_DrawingsUndeterminedWhenOwnListTruncated: with more
// own drawings than one page, a drawing missing from that page may
// still be the design's own, so it is not called indirect.
func TestGetWhereUsed_DrawingsUndeterminedWhenOwnListTruncated(t *testing.T) {
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		return testutil.GraphQLResponse{Data: map[string]any{"item": map[string]any{
			"drawingItems": map[string]any{
				"pagination": map[string]any{"cursor": "MORE"},
				"results":    []map[string]any{{"id": "dw-1"}},
			},
			"whereUsed": map[string]any{
				"pagination": map[string]any{"cursor": nil},
				"results": []map[string]any{
					{"__typename": "DrawingItem", "id": "dw-1", "name": "dw-1"},
					{"__typename": "DrawingItem", "id": "dw-99", "name": "dw-99"},
				},
			}}}}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetWhereUsed(context.Background(), "tok", "H1", "I1")
	if err != nil {
		t.Fatalf("GetWhereUsed: %v", err)
	}
	if rows := whereUsedRows(got); rows != "dw-1=direct,dw-99=" {
		t.Errorf("rows = %s, want dw-1=direct,dw-99=", rows)
	}
}

func TestGetDrawingSource_DecodesTipModel(t *testing.T) {
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		if !strings.Contains(req.Query, "tipDrawing {") {
			t.Errorf("query missing tipDrawing field: %q", req.Query)
		}
		if req.Variables["hubId"] != "H1" || req.Variables["itemId"] != "DW1" {
			t.Errorf("variables = %v, want hubId=H1 itemId=DW1", req.Variables)
		}
		return testutil.GraphQLResponse{Data: map[string]any{
			"item": map[string]any{
				"tipDrawing": map[string]any{
					"model": map[string]any{
						"id":           "m-src",
						"name":         prop("Bracket"),
						"materialName": prop("Steel"),
						"component": map[string]any{
							"partNumber":  prop("BR-1"),
							"description": prop("Mounting bracket"),
						},
						"designItem": map[string]any{
							"id":           "di-src",
							"name":         "Bracket.f3d",
							"fusionWebUrl": "https://x/bracket",
						},
					},
				},
			},
		}}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetDrawingSource(context.Background(), "tok", "H1", "DW1")
	if err != nil {
		t.Fatalf("GetDrawingSource: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (drawings reference one source)", len(got))
	}
	want := ComponentRef{
		ID: "m-src", Name: "Bracket", PartNumber: "BR-1", PartDesc: "Mounting bracket", Material: "Steel",
		DesignItemID: "di-src", DesignItemName: "Bracket.f3d", FusionWebURL: "https://x/bracket",
	}
	if got[0] != want {
		t.Errorf("ref = %+v, want %+v", got[0], want)
	}
}

func TestGetDrawingSource_NoTipReturnsEmpty(t *testing.T) {
	for name, item := range map[string]map[string]any{
		"no tip drawing":       {"tipDrawing": nil},
		"tip without a model":  {"tipDrawing": map[string]any{"model": nil}},
		"not a drawing at all": {},
	} {
		t.Run(name, func(t *testing.T) {
			srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
				return testutil.GraphQLResponse{Data: map[string]any{"item": item}}
			})
			swapEndpoint(t, srv.URL)

			got, err := GetDrawingSource(context.Background(), "tok", "H1", "DW1")
			if err != nil {
				t.Fatalf("GetDrawingSource: %v", err)
			}
			if len(got) != 0 {
				t.Errorf("len = %d, want 0; got %+v", len(got), got)
			}
		})
	}
}

func TestGetDrawingsForDesign_PaginatesSortsAndDedupes(t *testing.T) {
	drawing := func(id, name, modified, first string) map[string]any {
		return map[string]any{
			"id": id, "name": name, "fusionWebUrl": "https://x/" + id,
			"lastModifiedOn": modified,
			"lastModifiedBy": map[string]any{"firstName": first, "lastName": "User"},
		}
	}
	page := 0
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		page++
		// One hop from the design item — no walk over design versions.
		if !strings.Contains(req.Query, "drawingItems(pagination") || strings.Contains(req.Query, "versions(") {
			t.Errorf("query shape unexpected: %q", req.Query)
		}
		if req.Variables["hubId"] != "H1" || req.Variables["itemId"] != "D1" {
			t.Errorf("variables = %v, want hubId=H1 itemId=D1", req.Variables)
		}
		switch page {
		case 1:
			if _, has := req.Variables["cursor"]; has {
				t.Errorf("first call should not include cursor variable; got %v", req.Variables)
			}
			return testutil.GraphQLResponse{Data: map[string]any{
				"item": map[string]any{"drawingItems": map[string]any{
					"pagination": map[string]any{"cursor": "PAGE2"},
					"results": []map[string]any{
						drawing("dw-old", "Section A-A", "2024-01-01T00:00:00Z", "Old"),
						drawing("dw-new", "Top View", "2024-06-01T00:00:00Z", "New"),
					},
				}},
			}}
		case 2:
			if req.Variables["cursor"] != "PAGE2" {
				t.Errorf("second call cursor = %v, want PAGE2", req.Variables["cursor"])
			}
			return testutil.GraphQLResponse{Data: map[string]any{
				"item": map[string]any{"drawingItems": map[string]any{
					"pagination": map[string]any{"cursor": nil},
					"results": []map[string]any{
						drawing("dw-mid", "Detail B", "2024-03-01T00:00:00Z", "Mid"),
						// Repeat across the page boundary.
						drawing("dw-new", "Top View", "2024-06-01T00:00:00Z", "New"),
					},
				}},
			}}
		}
		t.Fatalf("unexpected extra call: %d", page)
		return testutil.GraphQLResponse{}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetDrawingsForDesign(context.Background(), "tok", "H1", "D1")
	if err != nil {
		t.Fatalf("GetDrawingsForDesign: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (repeat collapsed); got %+v", len(got), got)
	}
	var order []string
	for _, d := range got {
		order = append(order, d.DrawingItemID)
	}
	if strings.Join(order, ",") != "dw-new,dw-mid,dw-old" {
		t.Errorf("order = %v, want newest-modified first", order)
	}
	if got[0].Name != "Top View" || got[0].ModifiedBy != "New User" || got[0].FusionWebURL != "https://x/dw-new" {
		t.Errorf("first row decoded wrong: %+v", got[0])
	}
}

func TestGetDrawingsForDesign_NotADesign(t *testing.T) {
	// The fragment doesn't apply to a non-design item, so the field is
	// simply absent — that is an empty tab, not an error.
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		return testutil.GraphQLResponse{Data: map[string]any{"item": map[string]any{}}}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetDrawingsForDesign(context.Background(), "tok", "H1", "D1")
	if err != nil {
		t.Fatalf("GetDrawingsForDesign: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestPropValue_String(t *testing.T) {
	var nilProp *propValue
	cases := []struct {
		name string
		in   *propValue
		want string
	}{
		{"nil property", nilProp, ""},
		{"null value", &propValue{}, ""},
		{"string", &propValue{Value: "Steel"}, "Steel"},
		{"non-string scalar is rendered, not dropped", &propValue{Value: float64(42)}, "42"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestGetWhereUsed_BatchesRelationChecks: every parent is checked in a
// single aliased request, not one request each.
func TestGetWhereUsed_BatchesRelationChecks(t *testing.T) {
	var relationRequests int
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		if strings.Contains(req.Query, "whereUsed(") {
			var results []map[string]any
			for _, id := range []string{"a", "b", "c"} {
				results = append(results, map[string]any{"__typename": "DesignItem", "id": id, "name": id,
					"tipRootModel": map[string]any{"id": "m-" + id}})
			}
			return testutil.GraphQLResponse{Data: map[string]any{"item": map[string]any{"whereUsed": map[string]any{
				"pagination": map[string]any{"cursor": nil}, "results": results,
			}}}}
		}
		relationRequests++
		data := map[string]any{}
		for i, model := range []string{"m-a", "m-b", "m-c"} {
			if req.Variables[fmt.Sprintf("m%d", i)] != model {
				t.Errorf("variable m%d = %v, want %s", i, req.Variables[fmt.Sprintf("m%d", i)], model)
			}
			target := "other"
			if model == "m-b" {
				target = "I1"
			}
			data[fmt.Sprintf("p%d", i)] = map[string]any{"assemblyRelations": map[string]any{
				"pagination": map[string]any{"cursor": nil},
				"results":    []map[string]any{{"toModel": map[string]any{"designItem": map[string]any{"id": target}}}},
			}}
		}
		return testutil.GraphQLResponse{Data: data}
	})
	swapEndpoint(t, srv.URL)

	rows, err := GetWhereUsed(context.Background(), "tok", "H1", "I1")
	if err != nil {
		t.Fatalf("GetWhereUsed: %v", err)
	}
	got := GetWhereUsedRelations(context.Background(), "tok", "I1", rows)
	if relationRequests != 1 {
		t.Errorf("relation requests = %d, want 1 for three parents", relationRequests)
	}
	if r := whereUsedRows(got); r != "b=direct,a=indirect,c=indirect" {
		t.Errorf("rows = %s", r)
	}
}
