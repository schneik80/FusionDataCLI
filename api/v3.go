package api

// v3.go — Manufacturing Data Model v3 client helpers.
//
// v3 consolidates the v1/v2 Component/ComponentVersion split into a single
// `Component` type with a `composition: WORKING` argument, and exposes the
// built-in Fusion extended properties (Category, Manufacturer, Vendor, etc.)
// via `Component.baseProperties`. The v1/v2 endpoint still works for the
// TUI's hub/project/folder/item navigation, so we run both clients in the
// same package rather than migrating the TUI.
//
// Patterns here are ported directly from `cmd/enrichprobe/main.go`, which
// proved each query live against the Standard Components project on 2026-04-20.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// V3Endpoint is the public v3 GraphQL URL for the Manufacturing Data Model.
// Override by setting V3EndpointOverride before calling any V3* function.
const V3Endpoint = "https://developer.api.autodesk.com/mfg/v3/graphql/public"

// V3EndpointOverride, when non-empty, replaces V3Endpoint in every v3 call.
// Exposed so callers (tests, dev against a staging host) can redirect without
// code changes.
var V3EndpointOverride string

// V3Property is one entry on Component.baseProperties.
type V3Property struct {
	Name         string
	Value        any
	DisplayValue string
	DefinitionID string
}

// V3PropertyDefinition describes a hub-level built-in extended-property
// definition (from hub.basePropertyDefinitionCollections).
type V3PropertyDefinition struct {
	ID         string
	Name       string
	Collection string // e.g. "Component", "Project", "Summary"
	ReadOnly   bool
}

// V3ComponentProperties bundles the ids and props from one item lookup.
// Description / PartNumber / Name are first-class Component fields on v3
// (wrapped as `{ value }` in GraphQL), not baseProperties entries — so we
// surface them directly alongside the property list.
type V3ComponentProperties struct {
	ModelID     string
	ComponentID string
	Name        string
	Description string
	PartNumber  string
	Material    string // materialName (flat string, not wrapped)
	Properties  []V3Property
}

// V3PropertyInput is one element of a SetProperties mutation's input list.
type V3PropertyInput struct {
	PropertyDefinitionID string
	Value                any
}

// ---------------------------------------------------------------------------
// Public functions
// ---------------------------------------------------------------------------

// V3ResolveNativeHubID translates a Data Management API hub ID (prefixed
// "a." or "b.") to its MFG v3 native form (prefixed "urn:adsk.ace:..."),
// introspecting the query-field name first to survive schema renames.
// If the input already starts with "urn:", it's returned unchanged.
func V3ResolveNativeHubID(ctx context.Context, token, hubID string) (string, error) {
	if strings.HasPrefix(hubID, "urn:") {
		return hubID, nil
	}
	fieldName, argName, err := v3FindDMResolver(ctx, token, "hub")
	if err != nil {
		return "", err
	}
	q := fmt.Sprintf(`
		query($id: ID!) {
		  %s(%s: $id) { id }
		}`, fieldName, argName)
	raw, err := v3GQL(ctx, token, q, map[string]any{"id": hubID})
	if err != nil {
		return "", err
	}
	var env map[string]struct {
		ID string `json:"id"`
	}
	if jerr := json.Unmarshal(raw, &env); jerr != nil {
		return "", fmt.Errorf("decode: %w", jerr)
	}
	hub, ok := env[fieldName]
	if !ok || hub.ID == "" {
		return "", fmt.Errorf("%s returned no id — token may lack access to this hub", fieldName)
	}
	return hub.ID, nil
}

// V3ResolveNativeProjectID translates a Data Management API project ID
// (prefixed "a." or "b.") to its MFG v3 native form. Returns input unchanged
// if it already starts with "urn:".
func V3ResolveNativeProjectID(ctx context.Context, token, projectID string) (string, error) {
	if strings.HasPrefix(projectID, "urn:") {
		return projectID, nil
	}
	fieldName, argName, err := v3FindDMResolver(ctx, token, "project")
	if err != nil {
		return "", err
	}
	q := fmt.Sprintf(`
		query($id: ID!) {
		  %s(%s: $id) { id }
		}`, fieldName, argName)
	raw, err := v3GQL(ctx, token, q, map[string]any{"id": projectID})
	if err != nil {
		return "", err
	}
	var env map[string]struct {
		ID string `json:"id"`
	}
	if jerr := json.Unmarshal(raw, &env); jerr != nil {
		return "", fmt.Errorf("decode: %w", jerr)
	}
	project, ok := env[fieldName]
	if !ok || project.ID == "" {
		return "", fmt.Errorf("%s returned no id — token may lack access to this project", fieldName)
	}
	return project.ID, nil
}

// V3Item is one entry from V3GetItemsByProject.
type V3Item struct {
	ID       string
	Name     string
	Typename string // "DesignItem" | "DrawingItem" | "ConfiguredDesignItem"
}

// V3GetItemsByProject lists items directly in the project (no folder
// traversal). Usually returns empty for library-style projects where every
// item lives in a folder — use V3WalkProjectItems to collect everything.
func V3GetItemsByProject(ctx context.Context, token, projectID string) ([]V3Item, error) {
	return v3paginateItems(ctx, token,
		`query($projectId: ID!) {
		   itemsByProject(projectId: $projectId, pagination: { limit: 100 }) {
		     pagination { cursor }
		     results { __typename id name }
		   }
		 }`,
		`query($projectId: ID!, $cursor: String!) {
		   itemsByProject(projectId: $projectId, pagination: { cursor: $cursor, limit: 100 }) {
		     pagination { cursor }
		     results { __typename id name }
		   }
		 }`,
		map[string]any{"projectId": projectID},
		"itemsByProject",
	)
}

// V3Folder is one folder in a project. Folders can contain both items and
// sub-folders, so real traversal recurses.
type V3Folder struct {
	ID   string
	Name string
}

// V3GetFoldersByProject lists top-level folders in a project.
func V3GetFoldersByProject(ctx context.Context, token, projectID string) ([]V3Folder, error) {
	return v3paginateFolders(ctx, token,
		`query($projectId: ID!) {
		   foldersByProject(projectId: $projectId, pagination: { limit: 100 }) {
		     pagination { cursor }
		     results { id name }
		   }
		 }`,
		`query($projectId: ID!, $cursor: String!) {
		   foldersByProject(projectId: $projectId, pagination: { cursor: $cursor, limit: 100 }) {
		     pagination { cursor }
		     results { id name }
		   }
		 }`,
		map[string]any{"projectId": projectID},
		"foldersByProject",
	)
}

// V3GetFoldersByFolder lists sub-folders inside a folder. Requires projectId
// (per v3 schema introspection — v1/v2 only needed folderId).
func V3GetFoldersByFolder(ctx context.Context, token, projectID, folderID string) ([]V3Folder, error) {
	return v3paginateFolders(ctx, token,
		`query($projectId: ID!, $folderId: ID!) {
		   foldersByFolder(projectId: $projectId, folderId: $folderId, pagination: { limit: 100 }) {
		     pagination { cursor }
		     results { id name }
		   }
		 }`,
		`query($projectId: ID!, $folderId: ID!, $cursor: String!) {
		   foldersByFolder(projectId: $projectId, folderId: $folderId, pagination: { cursor: $cursor, limit: 100 }) {
		     pagination { cursor }
		     results { id name }
		   }
		 }`,
		map[string]any{"projectId": projectID, "folderId": folderID},
		"foldersByFolder",
	)
}

// V3GetItemsByFolder lists items directly inside a folder (no recursion).
// Requires hubId + folderId per v3 schema.
func V3GetItemsByFolder(ctx context.Context, token, hubID, folderID string) ([]V3Item, error) {
	return v3paginateItems(ctx, token,
		`query($hubId: ID!, $folderId: ID!) {
		   itemsByFolder(hubId: $hubId, folderId: $folderId, pagination: { limit: 100 }) {
		     pagination { cursor }
		     results { __typename id name }
		   }
		 }`,
		`query($hubId: ID!, $folderId: ID!, $cursor: String!) {
		   itemsByFolder(hubId: $hubId, folderId: $folderId, pagination: { cursor: $cursor, limit: 100 }) {
		     pagination { cursor }
		     results { __typename id name }
		   }
		 }`,
		map[string]any{"hubId": hubID, "folderId": folderID},
		"itemsByFolder",
	)
}

// V3WalkProjectItems recursively collects every item under a project by
// descending into all folders. Needs hubID because itemsByFolder requires it.
// For catalog-style projects (Standard Components) this is the right
// listing primitive; don't call it on a project with tens of thousands of
// items without thinking about the round-trip cost.
func V3WalkProjectItems(ctx context.Context, token, hubID, projectID string) ([]V3Item, error) {
	var all []V3Item
	root, rootErr := V3GetItemsByProject(ctx, token, projectID)
	all = append(all, root...)

	folders, folderErr := V3GetFoldersByProject(ctx, token, projectID)
	queue := append([]V3Folder(nil), folders...)
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]

		items, err := V3GetItemsByFolder(ctx, token, hubID, f.ID)
		if err == nil {
			all = append(all, items...)
		}
		subs, err := V3GetFoldersByFolder(ctx, token, projectID, f.ID)
		if err == nil {
			queue = append(queue, subs...)
		}
	}

	if rootErr != nil {
		return all, rootErr
	}
	return all, folderErr
}

// ---------------------------------------------------------------------------
// Pagination helpers — DRY out the item/folder list loops.
// ---------------------------------------------------------------------------

func v3paginateItems(ctx context.Context, token, qFirst, qNext string, baseVars map[string]any, rootField string) ([]V3Item, error) {
	type row struct {
		Typename string `json:"__typename"`
		ID       string `json:"id"`
		Name     string `json:"name"`
	}
	var out []V3Item
	cursor := ""
	for {
		vars := cloneVars(baseVars)
		q := qFirst
		if cursor != "" {
			q = qNext
			vars["cursor"] = cursor
		}
		raw, err := v3GQL(ctx, token, q, vars)
		if err != nil {
			return out, err
		}
		env := map[string]struct {
			Pagination struct {
				Cursor string `json:"cursor"`
			} `json:"pagination"`
			Results []row `json:"results"`
		}{}
		if jerr := json.Unmarshal(raw, &env); jerr != nil {
			return out, fmt.Errorf("decode: %w", jerr)
		}
		payload, ok := env[rootField]
		if !ok {
			return out, fmt.Errorf("no %s in response", rootField)
		}
		for _, it := range payload.Results {
			out = append(out, V3Item{ID: it.ID, Name: it.Name, Typename: it.Typename})
		}
		cursor = payload.Pagination.Cursor
		if cursor == "" {
			return out, nil
		}
	}
}

func v3paginateFolders(ctx context.Context, token, qFirst, qNext string, baseVars map[string]any, rootField string) ([]V3Folder, error) {
	type row struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var out []V3Folder
	cursor := ""
	for {
		vars := cloneVars(baseVars)
		q := qFirst
		if cursor != "" {
			q = qNext
			vars["cursor"] = cursor
		}
		raw, err := v3GQL(ctx, token, q, vars)
		if err != nil {
			return out, err
		}
		env := map[string]struct {
			Pagination struct {
				Cursor string `json:"cursor"`
			} `json:"pagination"`
			Results []row `json:"results"`
		}{}
		if jerr := json.Unmarshal(raw, &env); jerr != nil {
			return out, fmt.Errorf("decode: %w", jerr)
		}
		payload, ok := env[rootField]
		if !ok {
			return out, fmt.Errorf("no %s in response", rootField)
		}
		for _, f := range payload.Results {
			out = append(out, V3Folder{ID: f.ID, Name: f.Name})
		}
		cursor = payload.Pagination.Cursor
		if cursor == "" {
			return out, nil
		}
	}
}

func cloneVars(v map[string]any) map[string]any {
	out := make(map[string]any, len(v)+1)
	for k, val := range v {
		out[k] = val
	}
	return out
}

// V3GetComponentProperties reads a DesignItem's root-component properties
// in one round trip: item → tipRootModel → component(composition: WORKING) →
// baseProperties.results[]. `hubID` must be the MFG-native form
// (run V3ResolveNativeHubID first if you have a Data Management API id).
func V3GetComponentProperties(ctx context.Context, token, hubID, itemID string) (*V3ComponentProperties, error) {
	const q = `
		query Nav($hubId: ID!, $itemId: ID!) {
		  item(hubId: $hubId, itemId: $itemId) {
		    __typename
		    ... on DesignItem {
		      tipRootModel {
		        id
		        component(composition: WORKING) {
		          id
		          name         { value }
		          description  { value }
		          partNumber   { value }
		          materialName { value }
		          baseProperties {
		            results {
		              name
		              displayValue
		              value
		              definition { id }
		            }
		          }
		        }
		      }
		    }
		  }
		}`
	raw, gqlErr := v3GQL(ctx, token, q, map[string]any{"hubId": hubID, "itemId": itemID})
	if raw == nil {
		return nil, gqlErr
	}
	type wrappedVal struct {
		Value string `json:"value"`
	}
	var r struct {
		Item struct {
			Typename     string `json:"__typename"`
			TipRootModel struct {
				ID        string `json:"id"`
				Component struct {
					ID             string     `json:"id"`
					Name           wrappedVal `json:"name"`
					Description    wrappedVal `json:"description"`
					PartNumber     wrappedVal `json:"partNumber"`
					MaterialName   wrappedVal `json:"materialName"`
					BaseProperties struct {
						Results []struct {
							Name         string `json:"name"`
							DisplayValue string `json:"displayValue"`
							Value        any    `json:"value"`
							Definition   struct {
								ID string `json:"id"`
							} `json:"definition"`
						} `json:"results"`
					} `json:"baseProperties"`
				} `json:"component"`
			} `json:"tipRootModel"`
		} `json:"item"`
	}
	if jerr := json.Unmarshal(raw, &r); jerr != nil {
		return nil, fmt.Errorf("decode: %w", jerr)
	}
	if r.Item.Typename != "DesignItem" {
		return nil, fmt.Errorf("item is a %s, not a DesignItem", r.Item.Typename)
	}
	c := r.Item.TipRootModel.Component
	out := &V3ComponentProperties{
		ModelID:     r.Item.TipRootModel.ID,
		ComponentID: c.ID,
		Name:        c.Name.Value,
		Description: c.Description.Value,
		PartNumber:  c.PartNumber.Value,
		Material:    c.MaterialName.Value,
	}
	for _, p := range c.BaseProperties.Results {
		v := p.Value
		if v == nil && p.DisplayValue != "" {
			v = p.DisplayValue
		}
		out.Properties = append(out.Properties, V3Property{
			Name:         p.Name,
			Value:        v,
			DisplayValue: p.DisplayValue,
			DefinitionID: p.Definition.ID,
		})
	}
	return out, gqlErr
}

// V3GetHubPropertyDefinitions walks hub.basePropertyDefinitionCollections
// and returns every non-archived definition keyed by its display name.
// Requires `data:search` scope; without it the response omits the
// `definitions` payload per-collection and the map will be empty (we return
// the partial-data error so callers can distinguish scope failures).
func V3GetHubPropertyDefinitions(ctx context.Context, token, hubID string) (map[string]V3PropertyDefinition, error) {
	const q = `
		query GetDefs($hubId: ID!) {
		  hub(hubId: $hubId) {
		    basePropertyDefinitionCollections {
		      results {
		        id
		        name
		        definitions {
		          results {
		            id
		            name
		            isReadOnly
		            isArchived
		          }
		        }
		      }
		    }
		  }
		}`
	raw, gqlErr := v3GQL(ctx, token, q, map[string]any{"hubId": hubID})
	if raw == nil {
		return nil, gqlErr
	}
	var r struct {
		Hub struct {
			Collections struct {
				Results []struct {
					ID          string `json:"id"`
					Name        string `json:"name"`
					Definitions struct {
						Results []struct {
							ID         string `json:"id"`
							Name       string `json:"name"`
							ReadOnly   bool   `json:"isReadOnly"`
							IsArchived bool   `json:"isArchived"`
						} `json:"results"`
					} `json:"definitions"`
				} `json:"results"`
			} `json:"basePropertyDefinitionCollections"`
		} `json:"hub"`
	}
	if jerr := json.Unmarshal(raw, &r); jerr != nil {
		return nil, fmt.Errorf("decode: %w", jerr)
	}
	out := make(map[string]V3PropertyDefinition)
	for _, coll := range r.Hub.Collections.Results {
		for _, d := range coll.Definitions.Results {
			if d.IsArchived {
				continue
			}
			if _, exists := out[d.Name]; exists {
				continue
			}
			out[d.Name] = V3PropertyDefinition{
				ID:         d.ID,
				Name:       d.Name,
				Collection: coll.Name,
				ReadOnly:   d.ReadOnly,
			}
		}
	}
	return out, gqlErr
}

// V3SetComponentProperties runs the setProperties mutation against a
// Component.id (NOT componentVersionId). Each input maps a propertyDefinitionId
// to its new value. The response echo is discarded — we only care about the
// non-error status for write-back.
func V3SetComponentProperties(ctx context.Context, token, componentID string, inputs []V3PropertyInput) error {
	if componentID == "" {
		return errors.New("empty componentID")
	}
	if len(inputs) == 0 {
		return errors.New("no property inputs")
	}
	propertyInputs := make([]map[string]any, len(inputs))
	for i, in := range inputs {
		if in.PropertyDefinitionID == "" {
			return fmt.Errorf("input %d: empty propertyDefinitionID", i)
		}
		propertyInputs[i] = map[string]any{
			"propertyDefinitionId": in.PropertyDefinitionID,
			"value":                in.Value,
		}
	}
	const m = `
		mutation Set($input: SetPropertiesInput!) {
		  setProperties(input: $input) {
		    targetId
		    properties { name value definition { id } }
		  }
		}`
	vars := map[string]any{
		"input": map[string]any{
			"targetId":       componentID,
			"propertyInputs": propertyInputs,
		},
	}
	_, err := v3GQL(ctx, token, m, vars)
	return err
}

// ---------------------------------------------------------------------------
// Internals
// ---------------------------------------------------------------------------

// v3Endpoint returns the active v3 endpoint (override wins).
func v3Endpoint() string {
	if V3EndpointOverride != "" {
		return V3EndpointOverride
	}
	return V3Endpoint
}

// v3GQL posts a v3 GraphQL request. Returns (data, err) where both can be
// non-nil — GraphQL responses can carry partial data alongside errors.
// Callers that only want hard failures should treat (nil, err) as fatal.
func v3GQL(ctx context.Context, token, query string, vars map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v3Endpoint(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if region != "" {
		req.Header.Set("X-Ads-Region", region)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("unauthorized (HTTP 401) — re-authenticate")
	}

	var gr struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string            `json:"message"`
			Path    []json.RawMessage `json:"path"`
		} `json:"errors"`
	}
	if jerr := json.Unmarshal(raw, &gr); jerr != nil {
		return nil, fmt.Errorf("non-JSON response (HTTP %d): %s", resp.StatusCode, string(raw))
	}

	var gqlErr error
	if len(gr.Errors) > 0 {
		msgs := make([]string, len(gr.Errors))
		for i, e := range gr.Errors {
			if len(e.Path) > 0 {
				parts := make([]string, len(e.Path))
				for j, p := range e.Path {
					parts[j] = strings.Trim(string(p), `"`)
				}
				msgs[i] = fmt.Sprintf("%s (at %s)", e.Message, strings.Join(parts, "."))
			} else {
				msgs[i] = e.Message
			}
		}
		gqlErr = fmt.Errorf("GraphQL: %s", strings.Join(msgs, "; "))
	}

	if len(gr.Data) == 0 || bytes.Equal(gr.Data, []byte("null")) {
		if gqlErr != nil {
			return nil, gqlErr
		}
		return nil, fmt.Errorf("empty response (HTTP %d)", resp.StatusCode)
	}
	return gr.Data, gqlErr
}

// V3DescribeResolvers introspects a set of Query field names and returns,
// for each one found, a comma-separated list of its arguments in the form
// "argName: TypeName". Useful for debugging unexpected empty responses —
// v3 sometimes adds or removes args vs v1/v2.
func V3DescribeResolvers(ctx context.Context, token string, fieldNames []string) (map[string]string, error) {
	const q = `
		query {
		  __type(name: "Query") {
		    fields {
		      name
		      args {
		        name
		        type {
		          name
		          kind
		          ofType { name kind }
		        }
		      }
		    }
		  }
		}`
	raw, err := v3GQL(ctx, token, q, nil)
	if err != nil {
		return nil, err
	}
	var r struct {
		Type struct {
			Fields []struct {
				Name string `json:"name"`
				Args []struct {
					Name string `json:"name"`
					Type struct {
						Name   string `json:"name"`
						Kind   string `json:"kind"`
						OfType struct {
							Name string `json:"name"`
							Kind string `json:"kind"`
						} `json:"ofType"`
					} `json:"type"`
				} `json:"args"`
			} `json:"fields"`
		} `json:"__type"`
	}
	if jerr := json.Unmarshal(raw, &r); jerr != nil {
		return nil, fmt.Errorf("decode: %w", jerr)
	}
	want := make(map[string]bool, len(fieldNames))
	for _, n := range fieldNames {
		want[n] = true
	}
	out := make(map[string]string)
	for _, f := range r.Type.Fields {
		if !want[f.Name] {
			continue
		}
		parts := make([]string, len(f.Args))
		for i, a := range f.Args {
			tn := a.Type.Name
			if tn == "" {
				tn = a.Type.OfType.Name
			}
			parts[i] = fmt.Sprintf("%s: %s", a.Name, tn)
		}
		out[f.Name] = strings.Join(parts, ", ")
	}
	return out, nil
}

// v3FindDMResolver introspects the v3 Query type for a
// "<kind>ByDataManagement*" resolver and returns its field + first arg name.
// Used by V3ResolveNativeHubID to survive minor schema renames like
// hubByDataManagementAPIID vs hubByDataManagementAPIId.
func v3FindDMResolver(ctx context.Context, token, kind string) (fieldName, argName string, err error) {
	const q = `
		query {
		  __type(name: "Query") {
		    fields {
		      name
		      args { name }
		    }
		  }
		}`
	raw, err := v3GQL(ctx, token, q, nil)
	if err != nil {
		return "", "", err
	}
	var r struct {
		Type struct {
			Fields []struct {
				Name string `json:"name"`
				Args []struct {
					Name string `json:"name"`
				} `json:"args"`
			} `json:"fields"`
		} `json:"__type"`
	}
	if jerr := json.Unmarshal(raw, &r); jerr != nil {
		return "", "", fmt.Errorf("decode: %w", jerr)
	}
	prefix := strings.ToLower(kind) + "bydatamanagement"
	for _, f := range r.Type.Fields {
		if strings.HasPrefix(strings.ToLower(f.Name), prefix) && len(f.Args) > 0 {
			return f.Name, f.Args[0].Name, nil
		}
	}
	return "", "", fmt.Errorf("no %sByDataManagement* resolver found on Query type", kind)
}
