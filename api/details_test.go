package api

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/schneik80/FusionDataCLI/internal/testutil"
)

func TestParseTime(t *testing.T) {
	mustParse := func(layout, s string) time.Time {
		t.Helper()
		v, err := time.Parse(layout, s)
		if err != nil {
			t.Fatalf("setup: parsing %q with %q: %v", s, layout, err)
		}
		return v
	}

	cases := []struct {
		name     string
		input    string
		wantZero bool
		want     time.Time
	}{
		{
			name:     "empty string returns zero time",
			input:    "",
			wantZero: true,
		},
		{
			name:  "RFC3339 UTC",
			input: "2024-01-15T10:30:45Z",
			want:  mustParse(time.RFC3339, "2024-01-15T10:30:45Z"),
		},
		{
			name:  "fractional Z fallback",
			input: "2024-01-15T10:30:45.123Z",
			want:  mustParse("2006-01-02T15:04:05.000Z", "2024-01-15T10:30:45.123Z"),
		},
		{
			name:  "RFC3339 with positive offset",
			input: "2024-01-15T10:30:45+02:00",
			want:  mustParse(time.RFC3339, "2024-01-15T10:30:45+02:00"),
		},
		{
			name:     "garbage returns zero time",
			input:    "garbage",
			wantZero: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseTime(tc.input)
			if tc.wantZero {
				if !got.IsZero() {
					t.Errorf("parseTime(%q) = %v, want zero time", tc.input, got)
				}
				return
			}
			if !got.Equal(tc.want) {
				t.Errorf("parseTime(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestGetItemDetails_AllFields(t *testing.T) {
	raw, err := os.ReadFile("testdata/details_design.json")
	if err != nil {
		t.Fatalf("reading fixture: %v", err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshaling fixture: %v", err)
	}

	var sawHubID, sawItemID bool
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		if v, ok := req.Variables["hubId"].(string); ok && v == "h1" {
			sawHubID = true
		} else {
			t.Errorf("Variables[hubId] = %v, want \"h1\"", req.Variables["hubId"])
		}
		if v, ok := req.Variables["itemId"].(string); ok && v == "item-1" {
			sawItemID = true
		} else {
			t.Errorf("Variables[itemId] = %v, want \"item-1\"", req.Variables["itemId"])
		}
		return testutil.GraphQLResponse{Data: data}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetItemDetails(context.Background(), "tok", "h1", "item-1")
	if err != nil {
		t.Fatalf("GetItemDetails: %v", err)
	}
	if !sawHubID || !sawItemID {
		t.Errorf("handler missed an assertion: hubId=%v itemId=%v", sawHubID, sawItemID)
	}

	if got.ID != "urn:item:abc" {
		t.Errorf("ID = %q, want %q", got.ID, "urn:item:abc")
	}
	if got.Name != "Widget A" {
		t.Errorf("Name = %q, want %q", got.Name, "Widget A")
	}
	if got.Typename != "DesignItem" {
		t.Errorf("Typename = %q, want %q", got.Typename, "DesignItem")
	}
	if got.Size != "12345678" {
		t.Errorf("Size = %q, want %q", got.Size, "12345678")
	}
	if got.MimeType != "application/vnd.autodesk.fusion360" {
		t.Errorf("MimeType = %q, want %q", got.MimeType, "application/vnd.autodesk.fusion360")
	}
	if got.ExtensionType != "Fusion360" {
		t.Errorf("ExtensionType = %q, want %q", got.ExtensionType, "Fusion360")
	}
	if got.FusionWebURL != "https://fusion.example/widget-a" {
		t.Errorf("FusionWebURL = %q, want %q", got.FusionWebURL, "https://fusion.example/widget-a")
	}
	if got.VersionNumber != 3 {
		t.Errorf("VersionNumber = %d, want 3", got.VersionNumber)
	}

	wantCreated := time.Date(2024, 1, 15, 10, 30, 45, 0, time.UTC)
	if !got.CreatedOn.Equal(wantCreated) {
		t.Errorf("CreatedOn = %v, want %v", got.CreatedOn, wantCreated)
	}
	wantModified := time.Date(2024, 2, 20, 14, 0, 0, 0, time.UTC)
	if !got.ModifiedOn.Equal(wantModified) {
		t.Errorf("ModifiedOn = %v, want %v", got.ModifiedOn, wantModified)
	}

	if got.CreatedBy != "Ada Lovelace" {
		t.Errorf("CreatedBy = %q, want %q", got.CreatedBy, "Ada Lovelace")
	}
	if got.ModifiedBy != "Grace Hopper" {
		t.Errorf("ModifiedBy = %q, want %q", got.ModifiedBy, "Grace Hopper")
	}
	if got.PartNumber != "WGT-001" {
		t.Errorf("PartNumber = %q, want %q", got.PartNumber, "WGT-001")
	}
	if got.PartDesc != "The widget A" {
		t.Errorf("PartDesc = %q, want %q", got.PartDesc, "The widget A")
	}
	if got.Material != "Aluminum 6061" {
		t.Errorf("Material = %q, want %q", got.Material, "Aluminum 6061")
	}
	if !got.IsMilestone {
		t.Errorf("IsMilestone = false, want true")
	}
	if got.RootModelID != "urn:model:xyz" {
		t.Errorf("RootModelID = %q, want %q", got.RootModelID, "urn:model:xyz")
	}

	if len(got.Versions) != 3 {
		t.Fatalf("len(Versions) = %d, want 3", len(got.Versions))
	}
	// History arrives newest first; the milestone entry is not a version,
	// and the saves are numbered oldest = 1.
	if got.Versions[0].Number != 3 {
		t.Errorf("Versions[0].Number = %d, want 3", got.Versions[0].Number)
	}
	if got.Versions[1].Number != 2 {
		t.Errorf("Versions[1].Number = %d, want 2", got.Versions[1].Number)
	}
	if got.Versions[2].Number != 1 {
		t.Errorf("Versions[2].Number = %d, want 1", got.Versions[2].Number)
	}
	if got.Versions[0].Comment != "third edit" {
		t.Errorf("Versions[0].Comment = %q, want %q", got.Versions[0].Comment, "third edit")
	}
	if got.Versions[0].CreatedBy != "Grace Hopper" {
		t.Errorf("Versions[0].CreatedBy = %q, want %q", got.Versions[0].CreatedBy, "Grace Hopper")
	}
	wantSaved := time.Date(2024, 2, 20, 14, 0, 0, 0, time.UTC)
	if !got.Versions[0].CreatedOn.Equal(wantSaved) {
		t.Errorf("Versions[0].CreatedOn = %v, want %v", got.Versions[0].CreatedOn, wantSaved)
	}
}

// The history filter is per item type: a DesignItem asked for
// DRAWING_WRITTEN answers BAD_REQUEST and nulls the whole item.
func TestGetItemDetails_HistoryFilterIsPerItemType(t *testing.T) {
	var query string
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		query = req.Query
		return testutil.GraphQLResponse{Data: map[string]any{"item": map[string]any{"__typename": "BasicItem", "id": "x"}}}
	})
	swapEndpoint(t, srv.URL)

	if _, err := GetItemDetails(context.Background(), "tok", "h1", "x"); err != nil {
		t.Fatalf("GetItemDetails: %v", err)
	}
	flat := strings.Join(strings.Fields(query), " ")
	for _, want := range []string{
		"... on DesignItem { fusionWebUrl tipRootModel {",
		"history(input: { filterTypes: [MODEL_WRITTEN, VERSION_CREATED] }",
		"... on DrawingItem { fusionWebUrl history(input: { filterTypes: [DRAWING_WRITTEN] }",
		"... on BasicItem { history(input: { filterTypes: [BASIC_ITEM_WRITTEN] }",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("query missing %q\n%s", want, flat)
		}
	}
	for _, gone := range []string{"itemVersions", "tipVersion", "tipRootComponentVersion"} {
		if strings.Contains(flat, gone) {
			t.Errorf("query still selects v2 field %q", gone)
		}
	}
}

// A design with more saves than one history page keeps walking the
// cursor, and the version count covers every page.
func TestGetItemDetails_PaginatesHistory(t *testing.T) {
	save := func(desc string) map[string]any {
		return map[string]any{"__typename": "ModelWrittenHistoryChange", "timestamp": "2024-01-01T00:00:00Z", "description": desc}
	}
	calls := 0
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		calls++
		switch calls {
		case 1:
			if _, has := req.Variables["cursor"]; has {
				t.Errorf("first call should not carry a cursor: %v", req.Variables)
			}
			return testutil.GraphQLResponse{Data: map[string]any{"item": map[string]any{
				"__typename": "DesignItem", "id": "d",
				"history": map[string]any{
					"pagination": map[string]any{"cursor": "P2"},
					"results":    []map[string]any{save("c"), save("b")},
				},
			}}}
		case 2:
			if req.Variables["cursor"] != "P2" {
				t.Errorf("second call cursor = %v, want P2", req.Variables["cursor"])
			}
			if strings.Contains(req.Query, "tipRootModel") {
				t.Errorf("history follow-up should not re-fetch details: %q", req.Query)
			}
			return testutil.GraphQLResponse{Data: map[string]any{"item": map[string]any{
				"history": map[string]any{
					"pagination": map[string]any{"cursor": nil},
					"results":    []map[string]any{save("a")},
				},
			}}}
		}
		t.Fatalf("unexpected extra call: %d", calls)
		return testutil.GraphQLResponse{}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetItemDetails(context.Background(), "tok", "h1", "d")
	if err != nil {
		t.Fatalf("GetItemDetails: %v", err)
	}
	if got.VersionNumber != 3 || len(got.Versions) != 3 {
		t.Fatalf("VersionNumber = %d, len(Versions) = %d, want 3 / 3", got.VersionNumber, len(got.Versions))
	}
	if got.Versions[0].Comment != "c" || got.Versions[0].Number != 3 || got.Versions[2].Comment != "a" || got.Versions[2].Number != 1 {
		t.Errorf("versions misnumbered across pages: %+v", got.Versions)
	}
	if got.IsMilestone {
		t.Errorf("IsMilestone = true, want false when the newest entry is a save")
	}
}

func TestGetItemDetails_DrawingItem_NoRootModel(t *testing.T) {
	data := map[string]any{
		"item": map[string]any{
			"__typename":     "DrawingItem",
			"id":             "urn:item:dwg",
			"name":           "Sheet 1",
			"size":           "0",
			"mimeType":       "application/dwg",
			"extensionType": "DrawingItem",
			"createdOn":      "2024-03-01T09:00:00Z",
			"createdBy":      map[string]any{"firstName": "X", "lastName": "Y"},
			"lastModifiedOn": "2024-03-02T09:00:00Z",
			"lastModifiedBy": map[string]any{"firstName": "X", "lastName": "Y"},
			"fusionWebUrl":   "https://example/dwg",
			"history": map[string]any{
				"pagination": map[string]any{"cursor": nil},
				"results": []map[string]any{
					{"__typename": "DrawingItemWrittenHistoryChange", "timestamp": "2024-03-02T09:00:00Z", "description": "User Saved"},
					{"__typename": "DrawingItemWrittenHistoryChange", "timestamp": "2024-03-01T09:00:00Z", "description": "Item created"},
				},
			},
		},
	}

	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		return testutil.GraphQLResponse{Data: data}
	})
	swapEndpoint(t, srv.URL)

	got, err := GetItemDetails(context.Background(), "tok", "h1", "item-dwg")
	if err != nil {
		t.Fatalf("GetItemDetails: %v", err)
	}
	if got.Typename != "DrawingItem" {
		t.Errorf("Typename = %q, want %q", got.Typename, "DrawingItem")
	}
	if got.RootModelID != "" {
		t.Errorf("RootModelID = %q, want empty", got.RootModelID)
	}
	if got.PartNumber != "" {
		t.Errorf("PartNumber = %q, want empty", got.PartNumber)
	}
	if got.Material != "" {
		t.Errorf("Material = %q, want empty", got.Material)
	}
	if got.IsMilestone {
		t.Errorf("IsMilestone = true, want false")
	}
	if got.VersionNumber != 2 {
		t.Errorf("VersionNumber = %d, want 2 (one per DRAWING_WRITTEN entry)", got.VersionNumber)
	}
}

func TestApiUser_FullName(t *testing.T) {
	cases := []struct {
		name  string
		first string
		last  string
		want  string
	}{
		{name: "both empty", first: "", last: "", want: ""},
		{name: "first only", first: "Ada", last: "", want: "Ada"},
		{name: "last only", first: "", last: "Lovelace", want: "Lovelace"},
		{name: "both present", first: "Ada", last: "Lovelace", want: "Ada Lovelace"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := apiUser{First: tc.first, Last: tc.last}
			got := u.fullName()
			if got != tc.want {
				t.Errorf("apiUser{%q,%q}.fullName() = %q, want %q", tc.first, tc.last, got, tc.want)
			}
		})
	}
}
