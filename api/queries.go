package api

import (
	"context"
	"encoding/json"
	"fmt"
)

// allPages calls the API repeatedly until no next-page cursor is returned.
// queryFirst is used for the first call (no cursor argument in pagination).
// queryNext is used for all subsequent calls ($cursor: String! is required).
// vars is the base variables map (without cursor); it is copied each call.
// extract receives the raw JSON data and returns the next cursor plus the
// raw results slice to be appended by the caller.
type pageResult struct {
	cursor string
	data   json.RawMessage
}

func allPages(
	ctx context.Context,
	token string,
	queryFirst, queryNext string,
	baseVars map[string]any,
	extract func(json.RawMessage) (pageResult, error),
) ([]json.RawMessage, error) {
	var pages []json.RawMessage
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

		pr, err := extract(data)
		if err != nil {
			return nil, err
		}
		pages = append(pages, pr.data)
		cursor = pr.cursor
		if cursor == "" {
			break
		}
	}
	return pages, nil
}

// ---------------------------------------------------------------------------
// GetHubs
// ---------------------------------------------------------------------------

func GetHubs(ctx context.Context, token string) ([]NavItem, error) {
	const qFirst = `
		query GetHubs {
			hubs(pagination: { limit: 50 }) {
				pagination { cursor }
				results {
					id name fusionWebUrl
					alternativeIdentifiers { dataManagementAPIHubId }
				}
			}
		}`
	const qNext = `
		query GetHubsNext($cursor: String!) {
			hubs(pagination: { cursor: $cursor, limit: 50 }) {
				pagination { cursor }
				results {
					id name fusionWebUrl
					alternativeIdentifiers { dataManagementAPIHubId }
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

	pages, err := allPages(ctx, token, qFirst, qNext, nil, func(data json.RawMessage) (pageResult, error) {
		var r struct {
			Hubs struct {
				Pagination struct{ Cursor string `json:"cursor"` } `json:"pagination"`
				Results    []hubResult                             `json:"results"`
			} `json:"hubs"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return pageResult{}, fmt.Errorf("hubs: %w", err)
		}
		raw, _ := json.Marshal(r.Hubs.Results)
		return pageResult{cursor: r.Hubs.Pagination.Cursor, data: raw}, nil
	})
	if err != nil {
		return nil, err
	}

	var all []hubResult
	for _, p := range pages {
		var batch []hubResult
		if err := json.Unmarshal(p, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
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
			projects(hubId: $hubId, pagination: { limit: 50 }) {
				pagination { cursor }
				results {
					id name fusionWebUrl
					alternativeIdentifiers { dataManagementAPIProjectId }
				}
			}
		}`
	const qNext = `
		query GetProjectsNext($hubId: ID!, $cursor: String!) {
			projects(hubId: $hubId, pagination: { cursor: $cursor, limit: 50 }) {
				pagination { cursor }
				results {
					id name fusionWebUrl
					alternativeIdentifiers { dataManagementAPIProjectId }
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

	pages, err := allPages(ctx, token, qFirst, qNext, map[string]any{"hubId": hubID}, func(data json.RawMessage) (pageResult, error) {
		var r struct {
			Projects struct {
				Pagination struct{ Cursor string `json:"cursor"` } `json:"pagination"`
				Results    []projectResult                         `json:"results"`
			} `json:"projects"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return pageResult{}, fmt.Errorf("projects: %w", err)
		}
		raw, _ := json.Marshal(r.Projects.Results)
		return pageResult{cursor: r.Projects.Pagination.Cursor, data: raw}, nil
	})
	if err != nil {
		return nil, err
	}

	var all []projectResult
	for _, p := range pages {
		var batch []projectResult
		if err := json.Unmarshal(p, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
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

// ---------------------------------------------------------------------------
// GetFolders
// ---------------------------------------------------------------------------

func GetFolders(ctx context.Context, token, projectID string) ([]NavItem, error) {
	const qFirst = `
		query GetFolders($projectId: ID!) {
			foldersByProject(projectId: $projectId, pagination: { limit: 50 }) {
				pagination { cursor }
				results { id name }
			}
		}`
	const qNext = `
		query GetFoldersNext($projectId: ID!, $cursor: String!) {
			foldersByProject(projectId: $projectId, pagination: { cursor: $cursor, limit: 50 }) {
				pagination { cursor }
				results { id name }
			}
		}`

	type folderResult struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	pages, err := allPages(ctx, token, qFirst, qNext, map[string]any{"projectId": projectID}, func(data json.RawMessage) (pageResult, error) {
		var r struct {
			FoldersByProject struct {
				Pagination struct{ Cursor string `json:"cursor"` } `json:"pagination"`
				Results    []folderResult                         `json:"results"`
			} `json:"foldersByProject"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return pageResult{}, fmt.Errorf("folders: %w", err)
		}
		raw, _ := json.Marshal(r.FoldersByProject.Results)
		return pageResult{cursor: r.FoldersByProject.Pagination.Cursor, data: raw}, nil
	})
	if err != nil {
		return nil, err
	}

	var all []folderResult
	for _, p := range pages {
		var batch []folderResult
		if err := json.Unmarshal(p, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
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
			itemsByProject(projectId: $projectId, pagination: { limit: 50 }) {
				pagination { cursor }
				results { __typename id name }
			}
		}`
	const qNext = `
		query GetProjectItemsNext($projectId: ID!, $cursor: String!) {
			itemsByProject(projectId: $projectId, pagination: { cursor: $cursor, limit: 50 }) {
				pagination { cursor }
				results { __typename id name }
			}
		}`

	type itemResult struct {
		Typename string `json:"__typename"`
		ID       string `json:"id"`
		Name     string `json:"name"`
	}

	pages, err := allPages(ctx, token, qFirst, qNext, map[string]any{"projectId": projectID}, func(data json.RawMessage) (pageResult, error) {
		var r struct {
			ItemsByProject struct {
				Pagination struct{ Cursor string `json:"cursor"` } `json:"pagination"`
				Results    []itemResult                           `json:"results"`
			} `json:"itemsByProject"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return pageResult{}, fmt.Errorf("project items: %w", err)
		}
		raw, _ := json.Marshal(r.ItemsByProject.Results)
		return pageResult{cursor: r.ItemsByProject.Pagination.Cursor, data: raw}, nil
	})
	if err != nil {
		return nil, err
	}

	var all []itemResult
	for _, p := range pages {
		var batch []itemResult
		if err := json.Unmarshal(p, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
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
			itemsByFolder(hubId: $hubId, folderId: $folderId, pagination: { limit: 50 }) {
				pagination { cursor }
				results { __typename id name }
			}
		}`
	const qNext = `
		query GetItemsNext($hubId: ID!, $folderId: ID!, $cursor: String!) {
			itemsByFolder(hubId: $hubId, folderId: $folderId, pagination: { cursor: $cursor, limit: 50 }) {
				pagination { cursor }
				results { __typename id name }
			}
		}`

	type itemResult struct {
		Typename string `json:"__typename"`
		ID       string `json:"id"`
		Name     string `json:"name"`
	}

	pages, err := allPages(ctx, token, qFirst, qNext, map[string]any{"hubId": hubID, "folderId": folderID}, func(data json.RawMessage) (pageResult, error) {
		var r struct {
			ItemsByFolder struct {
				Pagination struct{ Cursor string `json:"cursor"` } `json:"pagination"`
				Results    []itemResult                          `json:"results"`
			} `json:"itemsByFolder"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return pageResult{}, fmt.Errorf("items: %w", err)
		}
		raw, _ := json.Marshal(r.ItemsByFolder.Results)
		return pageResult{cursor: r.ItemsByFolder.Pagination.Cursor, data: raw}, nil
	})
	if err != nil {
		return nil, err
	}

	var all []itemResult
	for _, p := range pages {
		var batch []itemResult
		if err := json.Unmarshal(p, &batch); err != nil {
			return nil, err
		}
		all = append(all, batch...)
	}

	items := make([]NavItem, len(all))
	for i, it := range all {
		items[i] = navItemFromTypename(it.ID, it.Name, it.Typename)
	}
	return items, nil
}

// ---------------------------------------------------------------------------
// IntrospectType — schema introspection for debugging
// ---------------------------------------------------------------------------

// IntrospectTypeFields queries the live GraphQL schema for all input fields on
// a named type and returns formatted lines suitable for the debug log.
// Useful for discovering undocumented filter fields (e.g. ProjectFilter).
func IntrospectTypeFields(ctx context.Context, token, typeName string) ([]string, error) {
	// Inline the type name — this API does not accept variables in introspection queries.
	q := `{ __type(name: "` + typeName + `") { name kind inputFields { name description type { name kind ofType { name kind ofType { name kind } } } } fields { name description type { name kind ofType { name kind ofType { name kind } } } } enumValues { name description } } }`

	data, err := gqlQuery(ctx, token, q, nil)
	if err != nil {
		return nil, err
	}

	var result struct {
		Type *struct {
			Name   string `json:"name"`
			Kind   string `json:"kind"`
			InputFields []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Type        struct {
					Name   string `json:"name"`
					Kind   string `json:"kind"`
					OfType *struct {
						Name   string `json:"name"`
						Kind   string `json:"kind"`
						OfType *struct {
							Name string `json:"name"`
							Kind string `json:"kind"`
						} `json:"ofType"`
					} `json:"ofType"`
				} `json:"type"`
			} `json:"inputFields"`
			Fields []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
				Type        struct {
					Name   string `json:"name"`
					Kind   string `json:"kind"`
					OfType *struct {
						Name   string `json:"name"`
						Kind   string `json:"kind"`
						OfType *struct {
							Name string `json:"name"`
							Kind string `json:"kind"`
						} `json:"ofType"`
					} `json:"ofType"`
				} `json:"type"`
			} `json:"fields"`
			EnumValues []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"enumValues"`
		} `json:"__type"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("introspect %s: %w", typeName, err)
	}

	if result.Type == nil {
		return []string{"--- __type(" + typeName + ") returned null — type does not exist in schema ---"}, nil
	}

	t := result.Type
	lines := []string{
		fmt.Sprintf("--- __type(%s)  kind=%s ---", t.Name, t.Kind),
	}

	typStr := func(name, kind string, ofType any) string {
		if name != "" {
			return name
		}
		return kind
	}
	_ = typStr

	resolveType := func(name, kind string, of interface{ getName() string }) string {
		if name != "" {
			return name
		}
		return "(" + kind + ")"
	}
	_ = resolveType

	// Simple type string resolver
	typeLabel := func(name, kind string) string {
		if name != "" {
			return name
		}
		return kind
	}

	if len(t.InputFields) > 0 {
		lines = append(lines, "  inputFields:")
		for _, f := range t.InputFields {
			typName := typeLabel(f.Type.Name, f.Type.Kind)
			if f.Type.OfType != nil {
				typName = typeLabel(f.Type.OfType.Name, f.Type.OfType.Kind)
				if f.Type.OfType.OfType != nil {
					typName = typeLabel(f.Type.OfType.OfType.Name, f.Type.OfType.OfType.Kind)
				}
			}
			desc := ""
			if f.Description != "" {
				desc = "  // " + f.Description
			}
			lines = append(lines, fmt.Sprintf("    %-30s %s%s", f.Name+":", typName, desc))
		}
	}

	if len(t.Fields) > 0 {
		lines = append(lines, "  fields:")
		for _, f := range t.Fields {
			typName := typeLabel(f.Type.Name, f.Type.Kind)
			if f.Type.OfType != nil {
				typName = typeLabel(f.Type.OfType.Name, f.Type.OfType.Kind)
				if f.Type.OfType.OfType != nil {
					typName = typeLabel(f.Type.OfType.OfType.Name, f.Type.OfType.OfType.Kind)
				}
			}
			desc := ""
			if f.Description != "" {
				desc = "  // " + f.Description
			}
			lines = append(lines, fmt.Sprintf("    %-30s %s%s", f.Name+":", typName, desc))
		}
	}

	if len(t.EnumValues) > 0 {
		lines = append(lines, "  enumValues:")
		for _, e := range t.EnumValues {
			desc := ""
			if e.Description != "" {
				desc = "  // " + e.Description
			}
			lines = append(lines, fmt.Sprintf("    %s%s", e.Name, desc))
		}
	}

	if len(t.InputFields) == 0 && len(t.Fields) == 0 && len(t.EnumValues) == 0 {
		lines = append(lines, "  (no fields, inputFields, or enumValues returned)")
	}

	lines = append(lines, "--- end ---")
	return lines, nil
}

// ---------------------------------------------------------------------------
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
