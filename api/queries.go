package api

import (
	"context"
	"encoding/json"
	"fmt"
)

// GetHubs returns all hubs accessible to the authenticated user.
func GetHubs(ctx context.Context, token string) ([]NavItem, error) {
	const q = `
		query GetHubs {
			hubs {
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

	data, err := gqlQuery(ctx, token, q, nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Hubs struct {
			Results []struct {
				ID           string `json:"id"`
				Name         string `json:"name"`
				FusionWebURL string `json:"fusionWebUrl"`
				AlternativeIdentifiers struct {
					DataManagementAPIHubID string `json:"dataManagementAPIHubId"`
				} `json:"alternativeIdentifiers"`
			} `json:"results"`
		} `json:"hubs"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("hubs: %w", err)
	}

	items := make([]NavItem, len(result.Hubs.Results))
	for i, h := range result.Hubs.Results {
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

// GetProjects returns projects within a hub.
func GetProjects(ctx context.Context, token, hubID string) ([]NavItem, error) {
	const q = `
		query GetProjects($hubId: ID!) {
			projects(hubId: $hubId) {
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

	data, err := gqlQuery(ctx, token, q, map[string]any{"hubId": hubID})
	if err != nil {
		return nil, err
	}

	var result struct {
		Projects struct {
			Results []struct {
				ID           string `json:"id"`
				Name         string `json:"name"`
				FusionWebURL string `json:"fusionWebUrl"`
				AlternativeIdentifiers struct {
					DataManagementAPIProjectID string `json:"dataManagementAPIProjectId"`
				} `json:"alternativeIdentifiers"`
			} `json:"results"`
		} `json:"projects"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("projects: %w", err)
	}

	var items []NavItem
	for _, p := range result.Projects.Results {
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

// GetFolders returns root-level folders within a project.
func GetFolders(ctx context.Context, token, projectID string) ([]NavItem, error) {
	const q = `
		query GetFolders($projectId: ID!) {
			foldersByProject(projectId: $projectId) {
				results {
					id
					name
					objectCount
				}
			}
		}`

	data, err := gqlQuery(ctx, token, q, map[string]any{"projectId": projectID})
	if err != nil {
		return nil, err
	}

	var result struct {
		FoldersByProject struct {
			Results []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"results"`
		} `json:"foldersByProject"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("folders: %w", err)
	}

	items := make([]NavItem, len(result.FoldersByProject.Results))
	for i, f := range result.FoldersByProject.Results {
		items[i] = NavItem{
			ID:          f.ID,
			Name:        f.Name,
			Kind:        "folder",
			IsContainer: true,
		}
	}
	return items, nil
}

// GetProjectItems returns all items within a project using the itemsByProject query.
// Used as a fallback when foldersByProject returns no folders (project has no sub-folders).
func GetProjectItems(ctx context.Context, token, projectID string) ([]NavItem, error) {
	const q = `
		query GetProjectItems($projectId: ID!) {
			itemsByProject(projectId: $projectId) {
				results {
					__typename
					id
					name
				}
			}
		}`

	data, err := gqlQuery(ctx, token, q, map[string]any{"projectId": projectID})
	if err != nil {
		return nil, fmt.Errorf("project items: %w", err)
	}

	var result struct {
		ItemsByProject struct {
			Results []struct {
				Typename string `json:"__typename"`
				ID       string `json:"id"`
				Name     string `json:"name"`
			} `json:"results"`
		} `json:"itemsByProject"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("project items decode: %w", err)
	}

	items := make([]NavItem, len(result.ItemsByProject.Results))
	for i, it := range result.ItemsByProject.Results {
		kind := "unknown"
		isContainer := false
		switch it.Typename {
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
		items[i] = NavItem{
			ID:          it.ID,
			Name:        it.Name,
			Kind:        kind,
			IsContainer: isContainer,
		}
	}
	return items, nil
}

// GetItems returns items within a folder.
func GetItems(ctx context.Context, token, hubID, folderID string) ([]NavItem, error) {
	const q = `
		query GetItems($hubId: ID!, $folderId: ID!) {
			itemsByFolder(hubId: $hubId, folderId: $folderId) {
				results {
					__typename
					id
					name
				}
			}
		}`

	data, err := gqlQuery(ctx, token, q, map[string]any{"hubId": hubID, "folderId": folderID})
	if err != nil {
		return nil, err
	}

	var result struct {
		ItemsByFolder struct {
			Results []struct {
				Typename string `json:"__typename"`
				ID       string `json:"id"`
				Name     string `json:"name"`
			} `json:"results"`
		} `json:"itemsByFolder"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("items: %w", err)
	}

	items := make([]NavItem, len(result.ItemsByFolder.Results))
	for i, it := range result.ItemsByFolder.Results {
		kind := "unknown"
		isContainer := false
		switch it.Typename {
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
		items[i] = NavItem{
			ID:          it.ID,
			Name:        it.Name,
			Kind:        kind,
			IsContainer: isContainer,
		}
	}
	return items, nil
}
