package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ItemDetails holds the rich metadata for a single item fetched from the API.
type ItemDetails struct {
	ID            string
	Name          string
	Typename      string // DesignItem | DrawingItem | ConfiguredDesignItem | BasicItem
	Size          string
	MimeType      string
	ExtensionType string
	FusionWebURL  string
	CreatedOn     time.Time
	CreatedBy     string
	ModifiedOn    time.Time
	ModifiedBy    string
	VersionNumber int
	// Design-specific (DesignItem / ConfiguredDesignItem)
	PartNumber  string
	PartDesc    string
	Material    string
	IsMilestone bool
	// RootModelID is the id of the design's tipRootModel — the key for
	// the Uses tab and the STEP derivative request. Empty for anything
	// that is not a DesignItem.
	RootModelID string
	// Version history (most recent first)
	Versions []VersionSummary
}

// VersionSummary is one entry in the version history list.
type VersionSummary struct {
	Number    int
	CreatedOn time.Time
	CreatedBy string
	Comment   string // history description, e.g. "User Saved" (may be empty)
}

// historyPageSize is the page size for the version-history selection.
const historyPageSize = 50

// maxHistoryPages bounds the history walk. 40 pages is 2000 saves — far
// past any real design — so hitting it means a cursor that never ends.
const maxHistoryPages = 40

// historySelection renders the per-type history fragments. v3 has no
// itemVersions list and no version numbers: an item's versions are the
// "written" entries of its history, and each concrete item type accepts
// only its own change types in filterTypes (asking a DesignItem for
// DRAWING_WRITTEN is a BAD_REQUEST that nulls the whole item), so the
// filter has to be spelled out per fragment.
//
// VERSION_CREATED rides along on designs so a milestone at the tip can
// be detected; those entries are not counted as versions.
func historySelection(pagination string, withDetails bool) string {
	const fields = `{
						pagination { cursor }
						results { __typename timestamp description author { firstName lastName } }
					}`
	hist := func(types string) string {
		return fmt.Sprintf("history(input: { filterTypes: [%s] }, pagination: { %s }) %s", types, pagination, fields)
	}
	design, drawing, configured := "", "", ""
	if withDetails {
		design = `
					fusionWebUrl
					tipRootModel {
						id
						materialName { value }
						component { partNumber { value } description { value } }
					}`
		drawing = "\n\t\t\t\t\tfusionWebUrl"
		configured = "\n\t\t\t\t\tfusionWebUrl"
	}
	return fmt.Sprintf(`
				... on DesignItem {%s
					%s
				}
				... on DrawingItem {%s
					%s
				}
				... on ConfiguredDesignItem {%s
					%s
				}
				... on BasicItem {
					%s
				}`,
		design, hist("MODEL_WRITTEN, VERSION_CREATED"),
		drawing, hist("DRAWING_WRITTEN"),
		configured, hist("MODEL_WRITTEN, VERSION_CREATED"),
		hist("BASIC_ITEM_WRITTEN"))
}

type historyEntry struct {
	Typename    string  `json:"__typename"`
	Timestamp   string  `json:"timestamp"`
	Description string  `json:"description"`
	Author      apiUser `json:"author"`
}

type historyPage struct {
	Pagination struct {
		Cursor string `json:"cursor"`
	} `json:"pagination"`
	Results []historyEntry `json:"results"`
}

// GetItemDetails fetches rich metadata for a single item plus its version list.
func GetItemDetails(ctx context.Context, token, hubID, itemID string) (*ItemDetails, error) {
	q := `
		query GetItemDetails($hubId: ID!, $itemId: ID!) {
			item(hubId: $hubId, itemId: $itemId) {
				__typename
				id
				name
				size
				mimeType
				extensionType
				createdOn
				createdBy  { firstName lastName }
				lastModifiedOn
				lastModifiedBy { firstName lastName }` +
		historySelection(fmt.Sprintf("limit: %d", historyPageSize), true) + `
			}
		}`
	qNext := `
		query GetItemHistoryNext($hubId: ID!, $itemId: ID!, $cursor: String!) {
			item(hubId: $hubId, itemId: $itemId) {` +
		historySelection(fmt.Sprintf("cursor: $cursor, limit: %d", historyPageSize), false) + `
			}
		}`

	vars := map[string]any{"hubId": hubID, "itemId": itemID}
	data, err := gqlQuery(ctx, token, q, vars)
	if err != nil {
		return nil, fmt.Errorf("item details: %w", err)
	}

	var raw struct {
		Item struct {
			Typename      string  `json:"__typename"`
			ID            string  `json:"id"`
			Name          string  `json:"name"`
			Size          string  `json:"size"`
			MimeType      string  `json:"mimeType"`
			ExtensionType string  `json:"extensionType"`
			FusionWebURL  string  `json:"fusionWebUrl"`
			CreatedOn     string  `json:"createdOn"`
			CreatedBy     apiUser `json:"createdBy"`
			ModifiedOn    string  `json:"lastModifiedOn"`
			ModifiedBy    apiUser `json:"lastModifiedBy"`
			TipRootModel  *struct {
				ID        string     `json:"id"`
				Material  *propValue `json:"materialName"`
				Component *struct {
					PartNumber  *propValue `json:"partNumber"`
					Description *propValue `json:"description"`
				} `json:"component"`
			} `json:"tipRootModel"`
			History historyPage `json:"history"`
		} `json:"item"`
	}

	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("item details decode: %w", err)
	}

	d := &ItemDetails{
		ID:            raw.Item.ID,
		Name:          raw.Item.Name,
		Typename:      raw.Item.Typename,
		Size:          raw.Item.Size,
		MimeType:      raw.Item.MimeType,
		ExtensionType: raw.Item.ExtensionType,
		FusionWebURL:  raw.Item.FusionWebURL,
		CreatedOn:     parseTime(raw.Item.CreatedOn),
		CreatedBy:     raw.Item.CreatedBy.fullName(),
		ModifiedOn:    parseTime(raw.Item.ModifiedOn),
		ModifiedBy:    raw.Item.ModifiedBy.fullName(),
	}
	if m := raw.Item.TipRootModel; m != nil {
		d.RootModelID = m.ID
		d.Material = m.Material.String()
		if m.Component != nil {
			d.PartNumber = m.Component.PartNumber.String()
			d.PartDesc = m.Component.Description.String()
		}
	}

	// Walk the rest of the history. Entries arrive newest first.
	entries := raw.Item.History.Results
	cursor := raw.Item.History.Pagination.Cursor
	for page := 1; cursor != "" && page < maxHistoryPages; page++ {
		nextVars := map[string]any{"hubId": hubID, "itemId": itemID, "cursor": cursor}
		data, err := gqlQuery(ctx, token, qNext, nextVars)
		if err != nil {
			return nil, fmt.Errorf("item history: %w", err)
		}
		var next struct {
			Item struct {
				History historyPage `json:"history"`
			} `json:"item"`
		}
		if err := json.Unmarshal(data, &next); err != nil {
			return nil, fmt.Errorf("item history decode: %w", err)
		}
		entries = append(entries, next.Item.History.Results...)
		cursor = next.Item.History.Pagination.Cursor
	}

	// A milestone ("Create version" in Fusion) at the tip shows up as a
	// VERSION_CREATED entry newer than the last save.
	if len(entries) > 0 && entries[0].Typename == "VersionCreatedHistoryChange" {
		d.IsMilestone = true
	}

	// Versions are the saves. Number them oldest = 1, which reproduces
	// the v2 versionNumber on every design it was checked against.
	saves := entries[:0:0]
	for _, e := range entries {
		if e.Typename != "VersionCreatedHistoryChange" {
			saves = append(saves, e)
		}
	}
	d.VersionNumber = len(saves)
	d.Versions = make([]VersionSummary, 0, len(saves))
	for i, e := range saves {
		d.Versions = append(d.Versions, VersionSummary{
			Number:    len(saves) - i,
			Comment:   e.Description,
			CreatedOn: parseTime(e.Timestamp),
			CreatedBy: e.Author.fullName(),
		})
	}

	return d, nil
}

// apiUser is a helper for deserialising User objects.
type apiUser struct {
	First string `json:"firstName"`
	Last  string `json:"lastName"`
}

func (u apiUser) fullName() string {
	name := u.First
	if u.Last != "" {
		if name != "" {
			name += " "
		}
		name += u.Last
	}
	return name
}

// parseTime parses an ISO-8601 / RFC-3339 timestamp returned by the API.
func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t, _ = time.Parse("2006-01-02T15:04:05.000Z", s)
	}
	return t
}
