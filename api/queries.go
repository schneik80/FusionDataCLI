package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// pageSize is the per-page result count requested from the GraphQL API.
// 200 is the documented maximum for the Manufacturing Data Model paginated
// fields and lets typical hubs / projects fit in a single round-trip.
const pageSize = 200

// allPages calls the API repeatedly until no next-page cursor is returned,
// accumulating typed results across pages. It is parameterised on T so the
// extract callback can decode straight into the caller's value type — no
// intermediate json.RawMessage round-trip per page.
//
// queryFirst is used for the first call (no cursor argument).
// queryNext  is used for all subsequent calls ($cursor: String! is required).
// baseVars   is the base variable map (without cursor); copied each call.
// extract    receives the raw JSON data and returns the next cursor plus the
//            decoded slice of T for that page.
func allPages[T any](
	ctx context.Context,
	token string,
	queryFirst, queryNext string,
	baseVars map[string]any,
	extract func(json.RawMessage) (cursor string, batch []T, err error),
) ([]T, error) {
	var all []T
	var cursor string
	first := true

	for {
		vars := make(map[string]any, len(baseVars)+1)
		for k, v := range baseVars {
			vars[k] = v
		}

		var q string
		if first {
			q = queryFirst
			first = false
		} else {
			q = queryNext
			vars["cursor"] = cursor
		}

		data, err := gqlQuery(ctx, token, q, vars)
		if err != nil {
			return nil, err
		}

		next, batch, err := extract(data)
		if err != nil {
			return nil, err
		}
		all = append(all, batch...)
		cursor = next
		if cursor == "" {
			break
		}
	}
	return all, nil
}

// ---------------------------------------------------------------------------
// GetHubs
// ---------------------------------------------------------------------------

func GetHubs(ctx context.Context, token string) ([]NavItem, error) {
	const qFirst = `
		query GetHubs {
			hubs(pagination: { limit: 200 }) {
				pagination { cursor }
				results {
					id name fusionWebUrl
					alternativeIdentifiers { dataManagementAPIHubId }
				}
			}
		}`
	const qNext = `
		query GetHubsNext($cursor: String!) {
			hubs(pagination: { cursor: $cursor, limit: 200 }) {
				pagination { cursor }
				results {
					id name fusionWebUrl
					alternativeIdentifiers { dataManagementAPIHubId }
				}
			}
		}`

	type hubResult struct {
		ID                     string `json:"id"`
		Name                   string `json:"name"`
		FusionWebURL           string `json:"fusionWebUrl"`
		AlternativeIdentifiers struct {
			DataManagementAPIHubID string `json:"dataManagementAPIHubId"`
		} `json:"alternativeIdentifiers"`
	}

	all, err := allPages(ctx, token, qFirst, qNext, nil, func(data json.RawMessage) (string, []hubResult, error) {
		var r struct {
			Hubs struct {
				Pagination struct {
					Cursor string `json:"cursor"`
				} `json:"pagination"`
				Results []hubResult `json:"results"`
			} `json:"hubs"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return "", nil, fmt.Errorf("hubs: %w", err)
		}
		return r.Hubs.Pagination.Cursor, r.Hubs.Results, nil
	})
	if err != nil {
		return nil, err
	}

	items := make([]NavItem, len(all))
	for i, h := range all {
		items[i] = NavItem{
			ID:          h.ID,
			Name:        h.Name,
			Kind:        "hub",
			AltID:       h.AlternativeIdentifiers.DataManagementAPIHubID,
			WebURL:      h.FusionWebURL,
			IsContainer: true,
		}
	}
	return items, nil
}

// ---------------------------------------------------------------------------
// GetProjects
// ---------------------------------------------------------------------------

func GetProjects(ctx context.Context, token, hubID string) ([]NavItem, error) {
	const qFirst = `
		query GetProjects($hubId: ID!) {
			hub(hubId: $hubId) {
				projects(pagination: { limit: 200 }) {
					pagination { cursor }
					results {
						id name fusionWebUrl projectStatus projectType
						alternativeIdentifiers { dataManagementAPIProjectId }
					}
				}
			}
		}`
	const qNext = `
		query GetProjectsNext($hubId: ID!, $cursor: String!) {
			hub(hubId: $hubId) {
				projects(pagination: { cursor: $cursor, limit: 200 }) {
					pagination { cursor }
					results {
						id name fusionWebUrl projectStatus projectType
						alternativeIdentifiers { dataManagementAPIProjectId }
					}
				}
			}
		}`

	type projectResult struct {
		ID                     string `json:"id"`
		Name                   string `json:"name"`
		FusionWebURL           string `json:"fusionWebUrl"`
		ProjectStatus          string `json:"projectStatus"`
		ProjectType            string `json:"projectType"`
		AlternativeIdentifiers struct {
			DataManagementAPIProjectID string `json:"dataManagementAPIProjectId"`
		} `json:"alternativeIdentifiers"`
	}

	all, err := allPages(ctx, token, qFirst, qNext, map[string]any{"hubId": hubID}, func(data json.RawMessage) (string, []projectResult, error) {
		var r struct {
			Hub struct {
				Projects struct {
					Pagination struct {
						Cursor string `json:"cursor"`
					} `json:"pagination"`
					Results []projectResult `json:"results"`
				} `json:"projects"`
			} `json:"hub"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return "", nil, fmt.Errorf("projects: %w", err)
		}
		return r.Hub.Projects.Pagination.Cursor, r.Hub.Projects.Results, nil
	})
	if err != nil {
		return nil, err
	}

	items := make([]NavItem, 0, len(all))
	for _, p := range all {
		if strings.EqualFold(p.ProjectStatus, "inactive") {
			continue
		}
		items = append(items, NavItem{
			ID:          p.ID,
			Name:        p.Name,
			Kind:        "project",
			AltID:       p.AlternativeIdentifiers.DataManagementAPIProjectID,
			WebURL:      p.FusionWebURL,
			IsContainer: true,
		})
	}
	return items, nil
}

// ---------------------------------------------------------------------------
// GetFolders
// ---------------------------------------------------------------------------

func GetFolders(ctx context.Context, token, projectID string) ([]NavItem, error) {
	const qFirst = `
		query GetFolders($projectId: ID!) {
			foldersByProject(projectId: $projectId, pagination: { limit: 200 }) {
				pagination { cursor }
				results { id name }
			}
		}`
	const qNext = `
		query GetFoldersNext($projectId: ID!, $cursor: String!) {
			foldersByProject(projectId: $projectId, pagination: { cursor: $cursor, limit: 200 }) {
				pagination { cursor }
				results { id name }
			}
		}`

	type folderResult struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	all, err := allPages(ctx, token, qFirst, qNext, map[string]any{"projectId": projectID}, func(data json.RawMessage) (string, []folderResult, error) {
		var r struct {
			FoldersByProject struct {
				Pagination struct {
					Cursor string `json:"cursor"`
				} `json:"pagination"`
				Results []folderResult `json:"results"`
			} `json:"foldersByProject"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return "", nil, fmt.Errorf("folders: %w", err)
		}
		return r.FoldersByProject.Pagination.Cursor, r.FoldersByProject.Results, nil
	})
	if err != nil {
		return nil, err
	}

	items := make([]NavItem, len(all))
	for i, f := range all {
		items[i] = NavItem{ID: f.ID, Name: f.Name, Kind: "folder", IsContainer: true}
	}
	return items, nil
}

// ---------------------------------------------------------------------------
// GetProjectItems
// ---------------------------------------------------------------------------

func GetProjectItems(ctx context.Context, token, projectID string) ([]NavItem, error) {
	const qFirst = `
		query GetProjectItems($projectId: ID!) {
			itemsByProject(projectId: $projectId, pagination: { limit: 200 }) {
				pagination { cursor }
				results { __typename id name }
			}
		}`
	const qNext = `
		query GetProjectItemsNext($projectId: ID!, $cursor: String!) {
			itemsByProject(projectId: $projectId, pagination: { cursor: $cursor, limit: 200 }) {
				pagination { cursor }
				results { __typename id name }
			}
		}`

	type itemResult struct {
		Typename string `json:"__typename"`
		ID       string `json:"id"`
		Name     string `json:"name"`
	}

	all, err := allPages(ctx, token, qFirst, qNext, map[string]any{"projectId": projectID}, func(data json.RawMessage) (string, []itemResult, error) {
		var r struct {
			ItemsByProject struct {
				Pagination struct {
					Cursor string `json:"cursor"`
				} `json:"pagination"`
				Results []itemResult `json:"results"`
			} `json:"itemsByProject"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return "", nil, fmt.Errorf("project items: %w", err)
		}
		return r.ItemsByProject.Pagination.Cursor, r.ItemsByProject.Results, nil
	})
	if err != nil {
		return nil, err
	}

	items := make([]NavItem, len(all))
	for i, it := range all {
		items[i] = navItemFromTypename(it.ID, it.Name, it.Typename)
	}
	return items, nil
}

// ---------------------------------------------------------------------------
// GetItems
// ---------------------------------------------------------------------------

func GetItems(ctx context.Context, token, hubID, folderID string) ([]NavItem, error) {
	const qFirst = `
		query GetItems($hubId: ID!, $folderId: ID!) {
			itemsByFolder(hubId: $hubId, folderId: $folderId, pagination: { limit: 200 }) {
				pagination { cursor }
				results { __typename id name }
			}
		}`
	const qNext = `
		query GetItemsNext($hubId: ID!, $folderId: ID!, $cursor: String!) {
			itemsByFolder(hubId: $hubId, folderId: $folderId, pagination: { cursor: $cursor, limit: 200 }) {
				pagination { cursor }
				results { __typename id name }
			}
		}`

	type itemResult struct {
		Typename string `json:"__typename"`
		ID       string `json:"id"`
		Name     string `json:"name"`
	}

	all, err := allPages(ctx, token, qFirst, qNext, map[string]any{"hubId": hubID, "folderId": folderID}, func(data json.RawMessage) (string, []itemResult, error) {
		var r struct {
			ItemsByFolder struct {
				Pagination struct {
					Cursor string `json:"cursor"`
				} `json:"pagination"`
				Results []itemResult `json:"results"`
			} `json:"itemsByFolder"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return "", nil, fmt.Errorf("items: %w", err)
		}
		return r.ItemsByFolder.Pagination.Cursor, r.ItemsByFolder.Results, nil
	})
	if err != nil {
		return nil, err
	}

	items := make([]NavItem, len(all))
	for i, it := range all {
		items[i] = navItemFromTypename(it.ID, it.Name, it.Typename)
	}
	return items, nil
}

// navItemFromTypename maps a GraphQL __typename to a NavItem.
func navItemFromTypename(id, name, typename string) NavItem {
	kind := "unknown"
	isContainer := false
	switch typename {
	case "DesignItem":
		kind = "design"
	case "ConfiguredDesignItem":
		kind = "configured"
	case "DrawingItem":
		kind = "drawing"
	case "Folder":
		kind = "folder"
		isContainer = true
	}
	return NavItem{ID: id, Name: name, Kind: kind, IsContainer: isContainer}
}
