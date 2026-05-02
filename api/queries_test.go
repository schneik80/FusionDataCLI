package api

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/schneik80/FusionDataCLI/internal/testutil"
)

func TestNavItemFromTypename(t *testing.T) {
	cases := []struct {
		name            string
		typename        string
		wantKind        string
		wantIsContainer bool
	}{
		{name: "DesignItem", typename: "DesignItem", wantKind: "design", wantIsContainer: false},
		{name: "ConfiguredDesignItem", typename: "ConfiguredDesignItem", wantKind: "configured", wantIsContainer: false},
		{name: "DrawingItem", typename: "DrawingItem", wantKind: "drawing", wantIsContainer: false},
		{name: "Folder", typename: "Folder", wantKind: "folder", wantIsContainer: true},
		{name: "unknown typename", typename: "MysteryType", wantKind: "unknown", wantIsContainer: false},
		{name: "empty typename", typename: "", wantKind: "unknown", wantIsContainer: false},
	}

	const (
		id   = "urn:test:item:123"
		name = "Test Name"
	)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := navItemFromTypename(id, name, tc.typename)
			if got.ID != id {
				t.Errorf("ID = %q, want %q", got.ID, id)
			}
			if got.Name != name {
				t.Errorf("Name = %q, want %q", got.Name, name)
			}
			if got.Kind != tc.wantKind {
				t.Errorf("Kind = %q, want %q", got.Kind, tc.wantKind)
			}
			if got.IsContainer != tc.wantIsContainer {
				t.Errorf("IsContainer = %v, want %v", got.IsContainer, tc.wantIsContainer)
			}
		})
	}
}

func TestGetHubs_Pagination(t *testing.T) {
	var calls atomic.Int32
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		n := calls.Add(1)
		switch n {
		case 1:
			if _, ok := req.Variables["cursor"]; ok {
				t.Errorf("first call should not include cursor variable, got %v", req.Variables)
			}
			return testutil.GraphQLResponse{Data: map[string]any{
				"hubs": map[string]any{
					"pagination": map[string]any{"cursor": "PAGE2"},
					"results": []map[string]any{
						{
							"id":           "h1",
							"name":         "Hub1",
							"fusionWebUrl": "https://example/h1",
							"alternativeIdentifiers": map[string]any{
								"dataManagementAPIHubId": "ah1",
							},
						},
						{
							"id":           "h2",
							"name":         "Hub2",
							"fusionWebUrl": "https://example/h2",
							"alternativeIdentifiers": map[string]any{
								"dataManagementAPIHubId": "ah2",
							},
						},
					},
				},
			}}
		case 2:
			if got, _ := req.Variables["cursor"].(string); got != "PAGE2" {
				t.Errorf("second call cursor = %v, want \"PAGE2\"", req.Variables["cursor"])
			}
			return testutil.GraphQLResponse{Data: map[string]any{
				"hubs": map[string]any{
					"pagination": map[string]any{"cursor": ""},
					"results": []map[string]any{
						{
							"id":           "h3",
							"name":         "Hub3",
							"fusionWebUrl": "https://example/h3",
							"alternativeIdentifiers": map[string]any{
								"dataManagementAPIHubId": "ah3",
							},
						},
					},
				},
			}}
		default:
			t.Errorf("unexpected extra call #%d", n)
			return testutil.GraphQLResponse{Data: map[string]any{
				"hubs": map[string]any{
					"pagination": map[string]any{"cursor": ""},
					"results":    []map[string]any{},
				},
			}}
		}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetHubs(context.Background(), "tok")
	if err != nil {
		t.Fatalf("GetHubs: %v", err)
	}
	if got, want := calls.Load(), int32(2); got != want {
		t.Errorf("call count = %d, want %d", got, want)
	}

	wantIDs := []string{"h1", "h2", "h3"}
	if len(got) != len(wantIDs) {
		t.Fatalf("len = %d, want %d (items=%+v)", len(got), len(wantIDs), got)
	}
	for i, want := range wantIDs {
		if got[i].ID != want {
			t.Errorf("hubs[%d].ID = %q, want %q", i, got[i].ID, want)
		}
		if got[i].Kind != "hub" {
			t.Errorf("hubs[%d].Kind = %q, want \"hub\"", i, got[i].Kind)
		}
		if !got[i].IsContainer {
			t.Errorf("hubs[%d].IsContainer = false, want true", i)
		}
	}
}

func TestGetProjects_FiltersInactive(t *testing.T) {
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		if got, _ := req.Variables["hubId"].(string); got != "h1" {
			t.Errorf("hubId = %v, want \"h1\"", req.Variables["hubId"])
		}
		return testutil.GraphQLResponse{Data: map[string]any{
			"hub": map[string]any{
				"projects": map[string]any{
					"pagination": map[string]any{"cursor": ""},
					"results": []map[string]any{
						{
							"id":            "p-active-lower",
							"name":          "ActiveLower",
							"projectStatus": "active",
							"projectType":   "FUSION",
							"alternativeIdentifiers": map[string]any{
								"dataManagementAPIProjectId": "ap1",
							},
						},
						{
							"id":            "p-inactive-upper",
							"name":          "InactiveUpper",
							"projectStatus": "INACTIVE",
							"projectType":   "FUSION",
							"alternativeIdentifiers": map[string]any{
								"dataManagementAPIProjectId": "ap2",
							},
						},
						{
							"id":            "p-inactive-mixed",
							"name":          "InactiveMixed",
							"projectStatus": "Inactive",
							"projectType":   "FUSION",
							"alternativeIdentifiers": map[string]any{
								"dataManagementAPIProjectId": "ap3",
							},
						},
						{
							"id":            "p-active-cap",
							"name":          "ActiveCap",
							"projectStatus": "Active",
							"projectType":   "FUSION",
							"alternativeIdentifiers": map[string]any{
								"dataManagementAPIProjectId": "ap4",
							},
						},
					},
				},
			},
		}}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetProjects(context.Background(), "tok", "h1")
	if err != nil {
		t.Fatalf("GetProjects: %v", err)
	}
	wantNames := []string{"ActiveLower", "ActiveCap"}
	if len(got) != len(wantNames) {
		t.Fatalf("len = %d, want %d (items=%+v)", len(got), len(wantNames), got)
	}
	for i, want := range wantNames {
		if got[i].Name != want {
			t.Errorf("projects[%d].Name = %q, want %q", i, got[i].Name, want)
		}
		if got[i].Kind != "project" {
			t.Errorf("projects[%d].Kind = %q, want \"project\"", i, got[i].Kind)
		}
		if !got[i].IsContainer {
			t.Errorf("projects[%d].IsContainer = false, want true", i)
		}
	}
}

func TestGetItems_TypenameMapping(t *testing.T) {
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		if got, _ := req.Variables["hubId"].(string); got != "h1" {
			t.Errorf("hubId = %v, want \"h1\"", req.Variables["hubId"])
		}
		if got, _ := req.Variables["folderId"].(string); got != "f1" {
			t.Errorf("folderId = %v, want \"f1\"", req.Variables["folderId"])
		}
		return testutil.GraphQLResponse{Data: map[string]any{
			"itemsByFolder": map[string]any{
				"pagination": map[string]any{"cursor": ""},
				"results": []map[string]any{
					{"__typename": "DesignItem", "id": "i1", "name": "Design"},
					{"__typename": "ConfiguredDesignItem", "id": "i2", "name": "Configured"},
					{"__typename": "DrawingItem", "id": "i3", "name": "Drawing"},
					{"__typename": "Folder", "id": "i4", "name": "SubFolder"},
					{"__typename": "MysteryItem", "id": "i5", "name": "Mystery"},
				},
			},
		}}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetItems(context.Background(), "tok", "h1", "f1")
	if err != nil {
		t.Fatalf("GetItems: %v", err)
	}

	want := []struct {
		id          string
		kind        string
		isContainer bool
	}{
		{"i1", "design", false},
		{"i2", "configured", false},
		{"i3", "drawing", false},
		{"i4", "folder", true},
		{"i5", "unknown", false},
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (items=%+v)", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].ID != w.id {
			t.Errorf("items[%d].ID = %q, want %q", i, got[i].ID, w.id)
		}
		if got[i].Kind != w.kind {
			t.Errorf("items[%d].Kind = %q, want %q", i, got[i].Kind, w.kind)
		}
		if got[i].IsContainer != w.isContainer {
			t.Errorf("items[%d].IsContainer = %v, want %v", i, got[i].IsContainer, w.isContainer)
		}
	}
}
