package api

import (
	"context"
	"encoding/json"
	"fmt"
)

// Estimated Cost is not a field of the v3 schema. It is a system base
// property of a Component (collection "Component", FLOAT, STANDARD
// behaviour, writable) that only shows up on a component once it has a
// value. It is read with baseProperties filtered by name and written
// with setProperties against the property definition below.

// EstimatedCostName is the property's name as the API returns it.
const EstimatedCostName = "Estimated Cost"

// EstimatedCostDefinitionID is the id of the Estimated Cost property
// definition. It decodes to a definition in the production base
// collection, not to anything hub-specific; it was read off a component
// on one hub and is assumed to be the same on the others.
const EstimatedCostDefinitionID = "cHJvcGRlZn5RbUZ6WlZCeWIzQmxjblJwWlhOUWNtOWt-UW1GelpWQnliM0JsY25ScFpYTkhjbTkxY0ZCeWIyUX5RMjl0Y0c5dVpXNTB-cGFyYW1ldGVycy5RbUZ6WlZCeWIzQmxjblJwWlhOUWNtOWs6ZTYwZmQ3MzRiOWUwNDg4YWJlMGIwNTM1OWU4YzMzN2YtMS4wLjA"

// DesignRow is one design in a listing, with the save time that tells a
// new version from an old one.
type DesignRow struct {
	ID         string
	Name       string
	ModifiedOn string
}

// DesignCost is what pricing a design needs: the component the cost is
// written to, the text a price is looked up from, and the cost it has.
type DesignCost struct {
	ItemID      string
	Name        string
	ModifiedOn  string
	ComponentID string // "" when the design has no model data
	Writable    bool
	HasChildren bool // an assembly: its cost is a roll-up of its children's
	Description string
	PartNumber  string
	Material    string
	Cost        *float64 // nil when no Estimated Cost is set
}

type designListRow struct {
	Typename   string `json:"__typename"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	ModifiedOn string `json:"lastModifiedOn"`
}

type designListPage struct {
	Pagination struct {
		Cursor string `json:"cursor"`
	} `json:"pagination"`
	Results []designListRow `json:"results"`
}

func designRows(rows []designListRow) []DesignRow {
	var out []DesignRow
	for _, r := range rows {
		if r.Typename == "DesignItem" {
			out = append(out, DesignRow{ID: r.ID, Name: r.Name, ModifiedOn: r.ModifiedOn})
		}
	}
	return out
}

// ListProjectDesigns returns every design in a project, in the project
// root and in all of its folders.
func ListProjectDesigns(ctx context.Context, token, hubID, projectID string) ([]DesignRow, error) {
	const qFirst = `
		query ListRootDesigns($projectId: ID!) {
			itemsByProject(projectId: $projectId, pagination: { limit: ` + itemsPageSize + ` }) {
				pagination { cursor }
				results { __typename id name lastModifiedOn }
			}
		}`
	const qNext = `
		query ListRootDesignsNext($projectId: ID!, $cursor: String!) {
			itemsByProject(projectId: $projectId, pagination: { cursor: $cursor, limit: ` + itemsPageSize + ` }) {
				pagination { cursor }
				results { __typename id name lastModifiedOn }
			}
		}`
	rows, err := allPages(ctx, token, qFirst, qNext, map[string]any{"projectId": projectID}, func(data json.RawMessage) (string, []designListRow, error) {
		var r struct {
			Page designListPage `json:"itemsByProject"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return "", nil, fmt.Errorf("project designs: %w", err)
		}
		return r.Page.Pagination.Cursor, r.Page.Results, nil
	})
	if err != nil {
		return nil, err
	}
	out := designRows(rows)

	folders, err := GetFolders(ctx, token, projectID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for len(folders) > 0 {
		f := folders[0]
		folders = folders[1:]
		if seen[f.ID] {
			continue
		}
		seen[f.ID] = true
		inFolder, err := listFolderDesigns(ctx, token, hubID, f.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, inFolder...)
		subs, err := GetSubFolders(ctx, token, hubID, f.ID)
		if err != nil {
			return nil, err
		}
		folders = append(folders, subs...)
	}
	return out, nil
}

func listFolderDesigns(ctx context.Context, token, hubID, folderID string) ([]DesignRow, error) {
	const qFirst = `
		query ListFolderDesigns($hubId: ID!, $folderId: ID!) {
			itemsByFolder(hubId: $hubId, folderId: $folderId, pagination: { limit: ` + itemsPageSize + ` }) {
				pagination { cursor }
				results { __typename id name lastModifiedOn }
			}
		}`
	const qNext = `
		query ListFolderDesignsNext($hubId: ID!, $folderId: ID!, $cursor: String!) {
			itemsByFolder(hubId: $hubId, folderId: $folderId, pagination: { cursor: $cursor, limit: ` + itemsPageSize + ` }) {
				pagination { cursor }
				results { __typename id name lastModifiedOn }
			}
		}`
	rows, err := allPages(ctx, token, qFirst, qNext, map[string]any{"hubId": hubID, "folderId": folderID}, func(data json.RawMessage) (string, []designListRow, error) {
		var r struct {
			Page designListPage `json:"itemsByFolder"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return "", nil, fmt.Errorf("folder designs: %w", err)
		}
		return r.Page.Pagination.Cursor, r.Page.Results, nil
	})
	if err != nil {
		return nil, err
	}
	return designRows(rows), nil
}

// GetDesignCost reads the tip of one design: its component, whether it
// has child components, the text a price can be looked up from, and its
// Estimated Cost if it has one.
//
// Children are probed the way ClassifyContents does it: one depth-1
// assembly relation. Component.hasChildren is true for single-body
// parts, so it cannot be used.
func GetDesignCost(ctx context.Context, token, hubID, itemID string) (*DesignCost, error) {
	const q = `
		query GetDesignCost($hubId: ID!, $itemId: ID!) {
			item(hubId: $hubId, itemId: $itemId) {
				__typename
				id
				name
				lastModifiedOn
				... on DesignItem {
					tipRootModel {
						id
						materialName { value }
						assemblyRelations(depth: 1, pagination: { limit: 1 }) { results { id } }
						component {
							id
							isWritableByUser
							description { value }
							partNumber { value }
							baseProperties(filter: { names: ["` + EstimatedCostName + `"] }) {
								results { name value }
							}
						}
					}
				}
			}
		}`
	data, err := gqlQuery(ctx, token, q, map[string]any{"hubId": hubID, "itemId": itemID})
	if err != nil {
		return nil, fmt.Errorf("design cost: %w", err)
	}
	var raw struct {
		Item struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			ModifiedOn   string `json:"lastModifiedOn"`
			TipRootModel *struct {
				Material *propValue `json:"materialName"`
				Children *struct {
					Results []struct {
						ID string `json:"id"`
					} `json:"results"`
				} `json:"assemblyRelations"`
				Component *struct {
					ID          string     `json:"id"`
					Writable    bool       `json:"isWritableByUser"`
					Description *propValue `json:"description"`
					PartNumber  *propValue `json:"partNumber"`
					Base        *struct {
						Results []struct {
							Name  string          `json:"name"`
							Value json.RawMessage `json:"value"`
						} `json:"results"`
					} `json:"baseProperties"`
				} `json:"component"`
			} `json:"tipRootModel"`
		} `json:"item"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("design cost decode: %w", err)
	}
	d := &DesignCost{ItemID: raw.Item.ID, Name: raw.Item.Name, ModifiedOn: raw.Item.ModifiedOn}
	m := raw.Item.TipRootModel
	if m == nil || m.Component == nil {
		return d, nil
	}
	c := m.Component
	d.ComponentID = c.ID
	d.Writable = c.Writable
	d.HasChildren = m.Children != nil && len(m.Children.Results) > 0
	d.Description = c.Description.String()
	d.PartNumber = c.PartNumber.String()
	d.Material = m.Material.String()
	if c.Base != nil {
		for _, p := range c.Base.Results {
			var v float64
			if p.Name == EstimatedCostName && json.Unmarshal(p.Value, &v) == nil {
				d.Cost = &v
			}
		}
	}
	return d, nil
}

// SetEstimatedCost writes a component's Estimated Cost and returns the
// value the API reports back. The token needs the data:write scope.
// Estimated Cost is a STANDARD property, so the write may create a new
// version of the component.
func SetEstimatedCost(ctx context.Context, token, componentID string, cost float64) (float64, error) {
	const q = `
		mutation SetEstimatedCost($input: SetPropertiesInput!) {
			setProperties(input: $input) {
				targetId
				properties { name value }
			}
		}`
	vars := map[string]any{"input": map[string]any{
		"targetId": componentID,
		"propertyInputs": []map[string]any{{
			"propertyDefinitionId": EstimatedCostDefinitionID,
			"value":                cost,
		}},
	}}
	data, err := gqlQuery(ctx, token, q, vars)
	if err != nil {
		return 0, fmt.Errorf("set estimated cost: %w", err)
	}
	var raw struct {
		SetProperties struct {
			Properties []struct {
				Name  string          `json:"name"`
				Value json.RawMessage `json:"value"`
			} `json:"properties"`
		} `json:"setProperties"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return 0, fmt.Errorf("set estimated cost decode: %w", err)
	}
	for _, p := range raw.SetProperties.Properties {
		var v float64
		if p.Name == EstimatedCostName && json.Unmarshal(p.Value, &v) == nil {
			return v, nil
		}
	}
	return 0, fmt.Errorf("set estimated cost: the API did not return the property")
}

// ClearEstimatedCost removes a component's Estimated Cost. The API has
// no mutation for removing a property value; setProperties with a null
// value is what clears it.
func ClearEstimatedCost(ctx context.Context, token, componentID string) error {
	const q = `
		mutation ClearEstimatedCost($input: SetPropertiesInput!) {
			setProperties(input: $input) { targetId }
		}`
	vars := map[string]any{"input": map[string]any{
		"targetId": componentID,
		"propertyInputs": []map[string]any{{
			"propertyDefinitionId": EstimatedCostDefinitionID,
			"value":                nil,
		}},
	}}
	if _, err := gqlQuery(ctx, token, q, vars); err != nil {
		return fmt.Errorf("clear estimated cost: %w", err)
	}
	return nil
}
