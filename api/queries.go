package api

import (
	"context"
	"encoding/json"
	"fmt"
)

// pageLimit is the number of results requested per page.
const pageLimit = 100

// GetHubs returns all hubs accessible to the authenticated user.
func GetHubs(ctx context.Context, token string) ([]NavItem, error) {
	const q = `
		query GetHubs($cursor: String) {
			hubs(pagination: { cursor: $cursor, limit: 100 }) {
				pagination { cursor }
				results {
					id
					name
					fusionWebUrl
					alternativeIdentifiers {
						dataManagementAPIHubId
					}
				}
			}
		}`

	type hubResult struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		FusionWebURL string `json:"fusionWebUrl"`
		AlternativeIdentifiers struct {
			DataManagementAPIHubID string `json:"dataManagementAPIHubId"`
		} `json:"alternativeIdentifiers"`
	}

	var all []hubResult
	var cursor string
	for {
		vars := map[string]any{}
		if cursor != "" {
			vars["cursor"] = cursor
		}
		data, err := gqlQuery(ctx, token, q, vars)
		if err != nil {
			return nil, err
		}
		var result struct {
			Hubs struct {
				Pagination struct{ Cursor string `json:"cursor"` } `json:"pagination"`
				Results    []hubResult `json:"results"`
			} `json:"hubs"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("hubs: %w", err)
		}
		all = append(all, result.Hubs.Results...)
		cursor = result.Hubs.Pagination.Cursor
		if cursor == "" {
			break
		}
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

// GetProjects returns all projects within a hub, fetching all pages.
func GetProjects(ctx context.Context, token, hubID string) ([]NavItem, error) {
	const q = `
		query GetProjects($hubId: ID!, $cursor: String) {
			projects(hubId: $hubId, pagination: { cursor: $cursor, limit: 100 }) {
				pagination { cursor }
				results {
					id
					name
					fusionWebUrl
					alternativeIdentifiers {
						dataManagementAPIProjectId
					}
				}
			}
		}`

	type projectResult struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		FusionWebURL string `json:"fusionWebUrl"`
		AlternativeIdentifiers struct {
			DataManagementAPIProjectID string `json:"dataManagementAPIProjectId"`
		} `json:"alternativeIdentifiers"`
	}

	var all []projectResult
	var cursor string
	for {
		vars := map[string]any{"hubId": hubID}
		if cursor != "" {
			vars["cursor"] = cursor
		}
		data, err := gqlQuery(ctx, token, q, vars)
		if err != nil {
			return nil, err
		}
		var result struct {
			Projects struct {
				Pagination struct{ Cursor string `json:"cursor"` } `json:"pagination"`
				Results    []projectResult `json:"results"`
			} `json:"projects"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("projects: %w", err)
		}
		all = append(all, result.Projects.Results...)
		cursor = result.Projects.Pagination.Cursor
		if cursor == "" {
			break
		}
	}

	items := make([]NavItem, len(all))
	for i, p := range all {
		items[i] = NavItem{
			ID:          p.ID,
			Name:        p.Name,
			Kind:        "project",
			AltID:       p.AlternativeIdentifiers.DataManagementAPIProjectID,
			WebURL:      p.FusionWebURL,
			IsContainer: true,
		}
	}
	return items, nil
}

// GetFolders returns all root-level folders within a project, fetching all pages.
func GetFolders(ctx context.Context, token, projectID string) ([]NavItem, error) {
	const q = `
		query GetFolders($projectId: ID!, $cursor: String) {
			foldersByProject(projectId: $projectId, pagination: { cursor: $cursor, limit: 100 }) {
				pagination { cursor }
				results {
					id
					name
				}
			}
		}`

	type folderResult struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	var all []folderResult
	var cursor string
	for {
		vars := map[string]any{"projectId": projectID}
		if cursor != "" {
			vars["cursor"] = cursor
		}
		data, err := gqlQuery(ctx, token, q, vars)
		if err != nil {
			return nil, err
		}
		var result struct {
			FoldersByProject struct {
				Pagination struct{ Cursor string `json:"cursor"` } `json:"pagination"`
				Results    []folderResult `json:"results"`
			} `json:"foldersByProject"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("folders: %w", err)
		}
		all = append(all, result.FoldersByProject.Results...)
		cursor = result.FoldersByProject.Pagination.Cursor
		if cursor == "" {
			break
		}
	}

	items := make([]NavItem, len(all))
	for i, f := range all {
		items[i] = NavItem{
			ID:          f.ID,
			Name:        f.Name,
			Kind:        "folder",
			IsContainer: true,
		}
	}
	return items, nil
}

// GetProjectItems returns all items at the root of a project, fetching all pages.
// Used as a fallback when foldersByProject returns no folders (project has no sub-folders).
func GetProjectItems(ctx context.Context, token, projectID string) ([]NavItem, error) {
	const q = `
		query GetProjectItems($projectId: ID!, $cursor: String) {
			itemsByProject(projectId: $projectId, pagination: { cursor: $cursor, limit: 100 }) {
				pagination { cursor }
				results {
					__typename
					id
					name
				}
			}
		}`

	type itemResult struct {
		Typename string `json:"__typename"`
		ID       string `json:"id"`
		Name     string `json:"name"`
	}

	var all []itemResult
	var cursor string
	for {
		vars := map[string]any{"projectId": projectID}
		if cursor != "" {
			vars["cursor"] = cursor
		}
		data, err := gqlQuery(ctx, token, q, vars)
		if err != nil {
			return nil, fmt.Errorf("project items: %w", err)
		}
		var result struct {
			ItemsByProject struct {
				Pagination struct{ Cursor string `json:"cursor"` } `json:"pagination"`
				Results    []itemResult `json:"results"`
			} `json:"itemsByProject"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("project items decode: %w", err)
		}
		all = append(all, result.ItemsByProject.Results...)
		cursor = result.ItemsByProject.Pagination.Cursor
		if cursor == "" {
			break
		}
	}

	items := make([]NavItem, len(all))
	for i, it := range all {
		items[i] = navItemFromTypename(it.ID, it.Name, it.Typename)
	}
	return items, nil
}

// GetItems returns all items within a folder, fetching all pages.
func GetItems(ctx context.Context, token, hubID, folderID string) ([]NavItem, error) {
	const q = `
		query GetItems($hubId: ID!, $folderId: ID!, $cursor: String) {
			itemsByFolder(hubId: $hubId, folderId: $folderId, pagination: { cursor: $cursor, limit: 100 }) {
				pagination { cursor }
				results {
					__typename
					id
					name
				}
			}
		}`

	type itemResult struct {
		Typename string `json:"__typename"`
		ID       string `json:"id"`
		Name     string `json:"name"`
	}

	var all []itemResult
	var cursor string
	for {
		vars := map[string]any{"hubId": hubID, "folderId": folderID}
		if cursor != "" {
			vars["cursor"] = cursor
		}
		data, err := gqlQuery(ctx, token, q, vars)
		if err != nil {
			return nil, err
		}
		var result struct {
			ItemsByFolder struct {
				Pagination struct{ Cursor string `json:"cursor"` } `json:"pagination"`
				Results    []itemResult `json:"results"`
			} `json:"itemsByFolder"`
		}
		if err := json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("items: %w", err)
		}
		all = append(all, result.ItemsByFolder.Results...)
		cursor = result.ItemsByFolder.Pagination.Cursor
		if cursor == "" {
			break
		}
	}

	items := make([]NavItem, len(all))
	for i, it := range all {
		items[i] = navItemFromTypename(it.ID, it.Name, it.Typename)
	}
	return items, nil
}

// navItemFromTypename maps a GraphQL __typename to a NavItem kind and IsContainer flag.
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
