package api

import (
	"context"
	"encoding/json"
	"fmt"
)

// FolderRef is a single hop in an item's folder ancestry.
type FolderRef struct {
	ID   string
	Name string
}

// ItemLocation describes where an item lives — used to drive
// "Show in Location" navigation from the Uses / Where Used / Drawings
// tabs into the Contents column.
type ItemLocation struct {
	HubID        string
	ProjectID    string // GraphQL ID — used to find the row in m.cols[colProjects]
	ProjectAltID string // dataManagementAPIProjectId — needed for Fusion MCP integration
	ProjectName  string
	// FolderPath is the ancestor chain from the project root down to
	// the folder that directly contains the item. Empty when the item
	// sits in the project root.
	FolderPath []FolderRef
}

// GetItemLocation looks up an item's project + folder ancestry. Walks
// parentFolder iteratively until null — handles arbitrary folder depth
// at the cost of one round-trip per ancestor level (typical: 2-4).
func GetItemLocation(ctx context.Context, token, hubID, itemID string) (*ItemLocation, error) {
	itemQ := `
		query LocateItem($hubId: ID!, $itemId: ID!) {
			item(hubId: $hubId, itemId: $itemId) {
				project {
					id name
					hub { id }
					alternativeIdentifiers { dataManagementAPIProjectId }
				}
				parentFolder ` + parentChain + `
			}
		}`

	data, err := gqlQuery(ctx, token, itemQ, map[string]any{"hubId": hubID, "itemId": itemID})
	if err != nil {
		return nil, fmt.Errorf("locate item: %w", err)
	}

	var raw struct {
		Item struct {
			Project struct {
				ID                     string `json:"id"`
				Name                   string `json:"name"`
				Hub                    struct{ ID string `json:"id"` } `json:"hub"`
				AlternativeIdentifiers struct {
					DataManagementAPIProjectID string `json:"dataManagementAPIProjectId"`
				} `json:"alternativeIdentifiers"`
			} `json:"project"`
			ParentFolder *folderChain `json:"parentFolder"`
		} `json:"item"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("locate item decode: %w", err)
	}
	if raw.Item.Project.ID == "" {
		return nil, fmt.Errorf("item %q has no project", itemID)
	}

	loc := &ItemLocation{
		HubID:        raw.Item.Project.Hub.ID,
		ProjectID:    raw.Item.Project.ID,
		ProjectName:  raw.Item.Project.Name,
		ProjectAltID: raw.Item.Project.AlternativeIdentifiers.DataManagementAPIProjectID,
	}

	// Walk parentFolder up to the project root. Collect leaf-first then
	// reverse so the caller gets root→leaf order, which is the order it
	// needs to drill the Contents column. Each query brings a chain of
	// parentChainDepth ancestors, so only a deeper tree needs a second.
	folderQ := `
		query GetFolderParent($hubId: ID!, $folderId: ID!) {
			folderByHubId(hubId: $hubId, folderId: $folderId) {
				parentFolder ` + parentChain + `
			}
		}`

	var ancestry []FolderRef
	chain := raw.Item.ParentFolder
	// Cap the queries defensively — a malformed response with a cycle
	// would otherwise spin forever. Folder trees in practice are a
	// handful of levels deep.
	for q := 0; chain != nil && chain.ID != "" && q < 20; q++ {
		n := 0
		var last *folderChain
		for c := chain; c != nil && c.ID != ""; c = c.Parent {
			ancestry = append(ancestry, FolderRef{ID: c.ID, Name: c.Name})
			last = c
			n++
		}
		// A chain shorter than was asked for ended at the project root.
		if n < parentChainDepth {
			break
		}
		d, err := gqlQuery(ctx, token, folderQ, map[string]any{"hubId": hubID, "folderId": last.ID})
		if err != nil {
			return nil, fmt.Errorf("walk folder %q: %w", last.ID, err)
		}
		var r struct {
			FolderByHubId struct {
				ParentFolder *folderChain `json:"parentFolder"`
			} `json:"folderByHubId"`
		}
		if err := json.Unmarshal(d, &r); err != nil {
			return nil, fmt.Errorf("walk folder decode: %w", err)
		}
		chain = r.FolderByHubId.ParentFolder
	}

	// Reverse leaf-first → root-first.
	for i, j := 0, len(ancestry)-1; i < j; i, j = i+1, j-1 {
		ancestry[i], ancestry[j] = ancestry[j], ancestry[i]
	}
	loc.FolderPath = ancestry
	return loc, nil
}

// parentChainDepth is how many ancestors one locate query asks for. A
// folder's parent costs no more query points nested than asked for on
// its own, so the whole chain rides on one request instead of one
// request per level.
const parentChainDepth = 6

// parentChain is the selection for a folder and its ancestors, nested
// parentChainDepth deep.
var parentChain = func() string {
	sel := "{ id name }"
	for i := 1; i < parentChainDepth; i++ {
		sel = "{ id name parentFolder " + sel + " }"
	}
	return sel
}()

// folderChain decodes parentChain.
type folderChain struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Parent *folderChain `json:"parentFolder"`
}
