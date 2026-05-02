package api

import "testing"

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
