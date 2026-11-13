package api

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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

// relationsAnswer answers a WhereUsedIsDirect request: holds maps a
// parent's model id to the design ids at its first level.
func relationsAnswer(req testutil.GraphQLRequest, holds map[string][]string) testutil.GraphQLResponse {
	data := map[string]any{}
	for n := 0; ; n++ {
		model, ok := req.Variables[fmt.Sprintf("m%d", n)].(string)
		if !ok {
			break
		}
		results := []map[string]any{}
		for _, id := range holds[model] {
			results = append(results, map[string]any{"toModel": map[string]any{"designItem": map[string]any{"id": id}}})
		}
		data[fmt.Sprintf("p%d", n)] = map[string]any{"assemblyRelations": map[string]any{
			"pagination": map[string]any{"cursor": nil}, "results": results}}
	}
	return testutil.GraphQLResponse{Data: data}
}

func TestGetWhereUsed_DecodesItems(t *testing.T) {
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		if strings.Contains(req.Query, "assemblyRelations(") {
			return relationsAnswer(req, map[string][]string{"m-robot": {"I1"}})
		}
		if !strings.Contains(req.Query, "whereUsed(pagination") {
			t.Errorf("query missing whereUsed field: %q", req.Query)
		}
		// The design's drawings have their own tab and its parents'
		// parents are not shown, so neither is asked for here.
		if strings.Contains(req.Query, "drawingItems") || strings.Count(req.Query, "whereUsed(") != 1 {
			t.Errorf("row query asks for more than the where-used rows: %q", req.Query)
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
								"id":           "m-robot",
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
						{"__typename": "ConfiguredDesignItem", "id": "cfg", "name": "Configured"},
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
	// The drawing is not a row. The two that cannot be checked are
	// listed after the confirmed parent, unconfirmed.
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3; got %+v", len(got), got)
	}
	wantDesign := ComponentRef{
		ID: "di-robot", Name: "Robot", PartNumber: "RB-1", PartDesc: "The robot", Material: "Aluminum",
		DesignItemID: "di-robot", DesignItemName: "Robot", FusionWebURL: "https://x/robot", Kind: "design",
		Relation: RelationDirect,
	}
	if got[0] != wantDesign {
		t.Errorf("design row = %+v, want %+v", got[0], wantDesign)
	}
	if got[1].ID != "di-bare" || got[1].Kind != "design" || got[1].PartNumber != "" || got[1].Relation != "" {
		t.Errorf("bare design row decoded wrong: %+v", got[1])
	}
	if got[2].ID != "cfg" || got[2].Kind != "configured" || got[2].Relation != "" {
		t.Errorf("configured row decoded wrong: %+v", got[2])
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

func whereUsedRows(refs []ComponentRef) string {
	var rows []string
	for _, r := range refs {
		rows = append(rows, r.ID+"="+r.Relation)
	}
	return strings.Join(rows, ",")
}

// TestGetWhereUsed_DirectParentsOnly: the API's whereUsed lists every
// assembly above the design, drawings, and documents that no longer use
// it. Only the designs that hold it at their own first level are
// returned — plus any whose references could not be read, unconfirmed.
//
//	top        reaches I1 only through "parent"      → dropped
//	top-dwg    drawing of "top"                      → dropped
//	own-dwg    drawing of I1 itself                  → dropped (Drawings tab)
//	parent     holds I1 directly
//	parent-p2  holds I1 directly, found on the second page of its references
//	stale      its current version references nothing → dropped
//	broken     its references cannot be read          → kept, unconfirmed
func TestGetWhereUsed_DirectParentsOnly(t *testing.T) {
	fakeQuota(t)
	design := func(id string) map[string]any {
		return map[string]any{"__typename": "DesignItem", "id": id, "name": id,
			"tipRootModel": map[string]any{"id": "m-" + id}}
	}
	drawing := func(id string) map[string]any {
		return map[string]any{"__typename": "DrawingItem", "id": id, "name": id}
	}
	rels := func(cursor any, designIDs ...string) map[string]any {
		results := []map[string]any{}
		for _, id := range designIDs {
			results = append(results, map[string]any{"toModel": map[string]any{"designItem": map[string]any{"id": id}}})
		}
		// A relation with no readable target must not break the check.
		results = append(results, map[string]any{"toModel": nil})
		return map[string]any{
			"assemblyRelations": map[string]any{"pagination": map[string]any{"cursor": cursor}, "results": results},
		}
	}
	var relationRequests int
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		if strings.Contains(req.Query, "whereUsed(") {
			return testutil.GraphQLResponse{Data: map[string]any{"item": map[string]any{
				"whereUsed": map[string]any{
					"pagination": map[string]any{"cursor": nil},
					"results": []map[string]any{
						design("top"), drawing("top-dwg"), design("broken"), design("parent"),
						drawing("own-dwg"), design("parent-p2"), design("stale"),
					},
				}}}}
		}
		relationRequests++
		if !strings.Contains(req.Query, "assemblyRelations(scope: INTERNAL_PLUS_DIRECT_EXTERNAL") {
			t.Errorf("relation check must use the direct-external scope: %q", req.Query)
		}
		// The parents arrive aliased p0, p1, … with variables m0, m1, …
		// and, from the second page on, c0, c1, ….
		data := map[string]any{}
		for n := 0; ; n++ {
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
				if req.Variables[fmt.Sprintf("c%d", n)] == "P2" {
					data[alias] = rels(nil, "I1")
				} else {
					data[alias] = rels("P2", "other")
				}
			case "m-stale":
				data[alias] = map[string]any{"assemblyRelations": map[string]any{
					"pagination": map[string]any{"cursor": nil}, "results": []map[string]any{}}}
			case "m-broken":
				data[alias] = map[string]any{"assemblyRelations": nil}
			default:
				t.Errorf("unexpected relation check: %v", req.Variables)
			}
		}
		return testutil.GraphQLResponse{Data: data}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetWhereUsed(context.Background(), "tok", "H1", "I1")
	if err != nil {
		t.Fatalf("GetWhereUsed: %v", err)
	}
	if rows := whereUsedRows(got); rows != "parent=direct,parent-p2=direct,broken=" {
		t.Errorf("rows = %s\nwant   parent=direct,parent-p2=direct,broken=", rows)
	}
	// All five designs in one request, and one more for the parent with
	// a second page of references.
	if relationRequests != 2 {
		t.Errorf("relation requests = %d, want 2", relationRequests)
	}
}

// TestGetWhereUsed_OnlyDrawings: a design used nowhere but drawn makes
// no relation request and has no rows.
func TestGetWhereUsed_OnlyDrawings(t *testing.T) {
	calls := 0
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		calls++
		return testutil.GraphQLResponse{Data: map[string]any{"item": map[string]any{
			"whereUsed": map[string]any{
				"pagination": map[string]any{"cursor": nil},
				"results":    []map[string]any{{"__typename": "DrawingItem", "id": "dw-1", "name": "dw-1"}},
			}}}}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetWhereUsed(context.Background(), "tok", "H1", "I1")
	if err != nil {
		t.Fatalf("GetWhereUsed: %v", err)
	}
	if len(got) != 0 || calls != 1 {
		t.Errorf("rows = %+v calls = %d, want no rows from one request", got, calls)
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

	got, err := GetWhereUsed(context.Background(), "tok", "H1", "I1")
	if err != nil {
		t.Fatalf("GetWhereUsed: %v", err)
	}
	if relationRequests != 1 {
		t.Errorf("relation requests = %d, want 1 for three parents", relationRequests)
	}
	if r := whereUsedRows(got); r != "b=direct" {
		t.Errorf("rows = %s", r)
	}
}

// relationServer answers a where-used list of three parents and hands
// every WhereUsedIsDirect request to relations.
func relationServer(t *testing.T, relations func(req testutil.GraphQLRequest) testutil.GraphQLResponse) {
	t.Helper()
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
		return relations(req)
	})
	swapEndpoint(t, srv.URL)
}

// TestGetWhereUsed_UnreadableParentCostsNoExtraRequests: a
// document with damaged data answers DATA_HAS_ERROR on its references —
// labelled UNKNOWN, at a shallow path, the shape of flakiness — beside
// the other parents' answers. That used to be retried three times and
// then asked again one parent at a time: 3 + 3 requests here, 3 + 25
// for a full batch. It is one request; the damaged parent alone is
// listed unconfirmed.
func TestGetWhereUsed_UnreadableParentCostsNoExtraRequests(t *testing.T) {
	fakeQuota(t)
	var relationRequests int
	relationServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		relationRequests++
		return testutil.GraphQLResponse{RawBody: `{"data":{
			"p0":{"assemblyRelations":{"pagination":{"cursor":null},"results":[{"toModel":{"designItem":{"id":"I1"}}}]}},
			"p1":{"assemblyRelations":null},
			"p2":{"assemblyRelations":{"pagination":{"cursor":null},"results":[{"toModel":{"designItem":{"id":"other"}}}]}}},
			"errors":[{"message":"DATA_LOSS: Data has error (file save snapshot)","path":["p1","assemblyRelations"],"extensions":{"code":"DATA_HAS_ERROR","errorType":"UNKNOWN","service":"mfgdm-core"}}]}`}
	})

	got, err := GetWhereUsed(context.Background(), "tok", "H1", "I1")
	if err != nil {
		t.Fatalf("GetWhereUsed: %v", err)
	}
	if relationRequests != 1 {
		t.Errorf("relation requests = %d, want 1", relationRequests)
	}
	if r := whereUsedRows(got); r != "a=direct,b=" {
		t.Errorf("rows = %s, want a=direct,b=", r)
	}
}

// TestGetWhereUsed_FlakyBatchIsNotSplit: a batch that kept
// failing transiently is not asked again parent by parent — that would
// be the same failure once more for each of them.
func TestGetWhereUsed_FlakyBatchIsNotSplit(t *testing.T) {
	fakeQuota(t)
	prev := retryBackoffs
	retryBackoffs = []time.Duration{0, 0, 0}
	t.Cleanup(func() { retryBackoffs = prev })
	var relationRequests int
	relationServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		relationRequests++
		return testutil.GraphQLResponse{RawBody: `{"data":null,"errors":[{"message":"Unknown Internal Server Error","extensions":{"errorType":"UNKNOWN"}}]}`}
	})

	got, err := GetWhereUsed(context.Background(), "tok", "H1", "I1")
	if err != nil {
		t.Fatalf("GetWhereUsed: %v", err)
	}
	if relationRequests != len(retryBackoffs) {
		t.Errorf("relation requests = %d, want %d (one batch, retried; no per-parent requests)", relationRequests, len(retryBackoffs))
	}
	// Nothing could be checked, so nothing is hidden: every design row
	// is listed, unconfirmed.
	if r := whereUsedRows(got); r != "a=,b=,c=" {
		t.Errorf("rows = %s, want a=,b=,c=", r)
	}
}

// TestGetWhereUsed_RefusedBatchIsAskedPerParent: a refusal is
// an answer about one parent (a model that no longer resolves nulls its
// root field), so the others are still asked for, one at a time.
func TestGetWhereUsed_RefusedBatchIsAskedPerParent(t *testing.T) {
	fakeQuota(t)
	var relationRequests atomic.Int32
	relationServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		relationRequests.Add(1)
		if len(req.Variables) > 1 || req.Variables["m0"] == "m-b" {
			return testutil.GraphQLResponse{RawBody: `{"data":{"p0":null},"errors":[{"message":"Model not found","path":["p0"],"extensions":{"code":"NOT_FOUND"}}]}`}
		}
		return testutil.GraphQLResponse{Data: map[string]any{"p0": map[string]any{"assemblyRelations": map[string]any{
			"pagination": map[string]any{"cursor": nil},
			"results":    []map[string]any{{"toModel": map[string]any{"designItem": map[string]any{"id": "I1"}}}},
		}}}}
	})

	got, err := GetWhereUsed(context.Background(), "tok", "H1", "I1")
	if err != nil {
		t.Fatalf("GetWhereUsed: %v", err)
	}
	if relationRequests.Load() != 4 {
		t.Errorf("relation requests = %d, want 4 (the batch, then each of three parents)", relationRequests.Load())
	}
	if r := whereUsedRows(got); r != "a=direct,c=direct,b=" {
		t.Errorf("rows = %s, want a=direct,c=direct,b=", r)
	}
}
