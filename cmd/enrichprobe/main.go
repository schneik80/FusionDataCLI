// cmd/enrichprobe/main.go
//
// Schema probe for the Fasteners Enrichment tool.
//
// Confirms — against the public MFG Data Model GraphQL endpoint using the
// CLI's existing 3-legged OAuth token — whether the shapes we need to write
// enriched metadata are actually available:
//
//   1. Can we read customProperties (name + value + definition id) on a
//      DesignItem's tipRootComponentVersion?
//   2. Can we walk the hub's propertyDefinitionCollections to find the six
//      built-in Fusion extended property definitions (Category, Estimated
//      cost, Manufacturer, Manufacturer part number, Package number, Vendor)?
//   3. Does the setProperties mutation exist on the public endpoint?
//   4. (Opt-in, -write) does setProperties actually succeed when we call it
//      with an existing value (no-op overwrite) on one of the six fields?
//
// Probes 1–3 are read-only and always run. Probe 4 is gated behind -write and
// only touches the property it was told to (default: Vendor) — it re-writes
// the current value so the data doesn't change, but it does hit a mutation
// endpoint so use only against a test item you're comfortable touching.
//
// Usage:
//
//	go run ./cmd/enrichprobe -item-id <ITEM_ID>
//	go run ./cmd/enrichprobe -item-id <ITEM_ID> -write -write-field "Vendor"
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/schneik80/FusionDataCLI/auth"
	"github.com/schneik80/FusionDataCLI/config"
)

const (
	defaultEndpoint = "https://developer.api.autodesk.com/mfg/v3/graphql/public"

	// Pinned Standard Components hub ID (ADSK-Schneik).
	defaultHubID = "a.YnVzaW5lc3M6YXV0b2Rlc2s4MDgz"
)

// endpoint is set from the -endpoint flag in main().
var endpoint = defaultEndpoint

// The seven built-in Fusion extended properties we can target for write-back.
// Names are Title Case exactly as Fusion / MFG DM returns them. Confirmed
// 2026-04-20 via probe against a live Standard Components item — list
// includes "Stock Number" (not "Package number") and "Package Type" which
// isn't always mentioned in Fusion UI docs.
var targetProperties = []string{
	"Category",
	"Estimated Cost",
	"Manufacturer",
	"Manufacturer Part Number",
	"Package Type",
	"Stock Number",
	"Vendor",
}

func main() {
	var (
		itemID     = flag.String("item-id", "", "required: an item ID from the Standard Components project")
		hubID      = flag.String("hub-id", defaultHubID, "hub ID (defaults to ADSK-Schneik)")
		endpointFl = flag.String("endpoint", defaultEndpoint, "MFG GraphQL endpoint URL (default: v3)")
		doWrite    = flag.Bool("write", false, "opt-in: attempt a no-op setProperties mutation")
		writeField = flag.String("write-field", "Vendor", "which built-in property to no-op-write (only used with -write)")
	)
	flag.Parse()
	endpoint = *endpointFl

	if *itemID == "" {
		fmt.Fprintln(os.Stderr, "error: -item-id is required")
		fmt.Fprintln(os.Stderr, "hint:  pick any item ID from your Standard Components project")
		flag.Usage()
		os.Exit(2)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	token, err := loadAccessToken(ctx)
	if err != nil {
		die("auth: %v", err)
	}

	fmt.Println(hr())
	fmt.Println("FusionDataCLI — Fasteners Enrichment schema probe")
	fmt.Println(hr())
	fmt.Printf("endpoint:  %s\n", endpoint)
	fmt.Printf("hub-id:    %s\n", *hubID)
	fmt.Printf("item-id:   %s\n", *itemID)
	fmt.Printf("write:     %t\n", *doWrite)
	fmt.Println(hr())

	// ── Probe 0a — list Query root fields so we can see v3 navigation ──────
	fmt.Println("[0a] list Query root fields")
	if qFields, err := introspectTypeFields(ctx, token, "Query"); err != nil {
		fail("  fail: %v", err)
	} else {
		sort.Strings(qFields)
		info("  Query has %d root fields:", len(qFields))
		for i := 0; i < len(qFields); i += 4 {
			end := i + 4
			if end > len(qFields) {
				end = len(qFields)
			}
			info("    %s", strings.Join(qFields[i:end], ", "))
		}
	}
	fmt.Println()

	// ── Probe 0b — normalize hub ID to MFG native form if needed ───────────
	fmt.Println("[0/4] resolve hub ID to MFG native form")
	nativeHubID, err := resolveNativeHubID(ctx, token, *hubID)
	if err != nil {
		fail("  fail: %v", err)
		fail("  aborting — stages 1/2 need the native hub id")
		return
	}
	if nativeHubID != *hubID {
		pass("  ok: %s → %s", *hubID, nativeHubID)
	} else {
		pass("  ok: already native")
	}

	// ── Probe 1 — introspect Component / Model / DesignItem + read props ───
	fmt.Println()
	fmt.Println("[1/4] introspect Component / Model / DesignItem and read property fields")
	for _, typeName := range []string{"Component", "Model", "DesignItem"} {
		fields, err := introspectTypeFields(ctx, token, typeName)
		if err != nil {
			info("  %s: %v", typeName, err)
			continue
		}
		sort.Strings(fields)
		info("  %s (%d fields):", typeName, len(fields))
		// print 3 per line for readability
		for i := 0; i < len(fields); i += 3 {
			end := i + 3
			if end > len(fields) {
				end = len(fields)
			}
			info("    %s", strings.Join(fields[i:end], ", "))
		}
		// Highlight matches to our target built-in names.
		var matches []string
		targetLC := []string{"category", "cost", "manufacturer", "package", "vendor"}
		for _, f := range fields {
			fLC := strings.ToLower(f)
			for _, t := range targetLC {
				if strings.Contains(fLC, t) {
					matches = append(matches, f)
					break
				}
			}
		}
		if len(matches) > 0 {
			pass("    matches to target built-ins: %s", strings.Join(matches, ", "))
		}
	}

	modelID, componentID, baseProps, err := probeItemProperties(ctx, token, nativeHubID, *itemID)
	if err != nil && (modelID != "" || componentID != "" || len(baseProps) > 0) {
		info("  warning (partial data returned): %v", err)
		err = nil
	}
	if err != nil {
		fail("  fail: %v", err)
	} else {
		pass("  ok: modelId=%s", modelID)
		pass("  ok: componentId=%s", componentID)
		if len(baseProps) == 0 {
			info("  baseProperties: empty (item may have no extended-property values set)")
		} else {
			info("  baseProperties: %d entries", len(baseProps))
			for _, p := range sortedProps(baseProps) {
				hit := ""
				for _, t := range targetProperties {
					if p.Name == t {
						hit = "  ← target"
						break
					}
				}
				info("    - %-30s = %-20s  (defId=%s)%s", truncate(p.Name, 30), truncate(displayVal(p.Value), 20), p.DefinitionID, hit)
			}
		}
	}

	existingProps := baseProps

	// ── Probe 2 — walk hub.basePropertyDefinitionCollections (v3) ──────────
	fmt.Println()
	fmt.Println("[2/4] walk hub.basePropertyDefinitionCollections for target built-ins")
	defsByName, err := probeHubDefinitions(ctx, token, nativeHubID)
	if err != nil && len(defsByName) > 0 {
		info("  warning (partial data returned — likely missing data:search scope): %v", err)
		err = nil
	}
	if err != nil {
		fail("  fail: %v", err)
	} else {
		pass("  ok: %d total definitions across all collections", len(defsByName))
		for _, name := range targetProperties {
			if d, ok := defsByName[name]; ok {
				info("  ✓ %-30s  defId=%s  (collection: %s, readonly=%t)", name, d.ID, d.Collection, d.ReadOnly)
			} else {
				info("  ✗ %-30s  NOT FOUND in any collection on this hub", name)
			}
		}
	}

	// ── Probe 3 — does setProperties mutation exist on the public endpoint? ─
	fmt.Println()
	fmt.Println("[3/4] introspect Mutation type for setProperties")
	mutationFields, err := probeMutationIntrospection(ctx, token)
	if err != nil {
		fail("  fail: %v", err)
	} else {
		sort.Strings(mutationFields)
		has := func(f string) bool {
			for _, m := range mutationFields {
				if m == f {
					return true
				}
			}
			return false
		}
		pass("  ok: %d mutations on public endpoint", len(mutationFields))
		for _, m := range []string{"setProperties", "assignModelPartNumber", "createPropertyDefinition", "linkPropertyDefinitionCollection"} {
			if has(m) {
				info("  ✓ %s", m)
			} else {
				info("  ✗ %s  (not exposed on public endpoint)", m)
			}
		}
	}

	// ── Probe 4 — optional no-op setProperties write ───────────────────────
	fmt.Println()
	fmt.Println("[4/4] no-op setProperties mutation")
	if !*doWrite {
		info("  skipped: pass -write to enable (rewrites the current value — no data change)")
	} else {
		if err := probeNoopWrite(ctx, token, componentID, existingProps, *writeField); err != nil {
			fail("  fail: %v", err)
		} else {
			pass("  ok: setProperties accepted a no-op write for %q", *writeField)
		}
	}

	fmt.Println()
	fmt.Println(hr())
	fmt.Println("probe complete")
}

// ---------------------------------------------------------------------------
// Probe 0 — resolve a Data Management API hub id ("a....") to its MFG native
// form ("urn:adsk:ace:..."). MFG GraphQL rejects the DM form on most fields
// but exposes a `hubByDataManagementAPIID` resolver specifically for this
// translation. We introspect its exact argument name first rather than
// guessing, so this works regardless of the server's naming convention.
// ---------------------------------------------------------------------------

func resolveNativeHubID(ctx context.Context, token, hubID string) (string, error) {
	if strings.HasPrefix(hubID, "urn:") {
		return hubID, nil
	}

	// Find the resolver field. The error message suggests a name, but server
	// casing varies ("hubByDataManagementAPIID" vs "hubByDataManagementAPIId",
	// etc.) — introspect and pick the one that looks like hub + DM resolution.
	fieldName, argName, err := findDMResolver(ctx, token, "hub")
	if err != nil {
		return "", err
	}
	info("  using resolver: %s(%s: ...)", fieldName, argName)

	q := fmt.Sprintf(`
		query($id: ID!) {
		  %s(%s: $id) {
		    id
		    name
		  }
		}`, fieldName, argName)
	raw, err := gql(ctx, token, q, map[string]any{"id": hubID})
	if err != nil {
		return "", err
	}
	// Use dynamic alias since the JSON key matches fieldName.
	var env map[string]struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("decode: %w", err)
	}
	hub, ok := env[fieldName]
	if !ok || hub.ID == "" {
		return "", fmt.Errorf("%s returned no id — token may lack access to this hub", fieldName)
	}
	return hub.ID, nil
}

// findDMResolver introspects the Query type and returns the field name + its
// first argument name for a "<kind>ByDataManagement*" resolver. Matching is
// case-insensitive and tolerates naming variations between APS services.
func findDMResolver(ctx context.Context, token, kind string) (fieldName, argName string, err error) {
	const q = `
		query {
		  __type(name: "Query") {
		    fields {
		      name
		      args { name }
		    }
		  }
		}`
	raw, err := gql(ctx, token, q, nil)
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
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", "", fmt.Errorf("decode: %w", err)
	}

	kindLC := strings.ToLower(kind)
	var candidates []string
	for _, f := range r.Type.Fields {
		n := strings.ToLower(f.Name)
		if strings.HasPrefix(n, kindLC+"bydatamanagement") {
			if len(f.Args) == 0 {
				continue
			}
			return f.Name, f.Args[0].Name, nil
		}
		if strings.Contains(n, "datamanagement") {
			candidates = append(candidates, f.Name)
		}
	}
	if len(candidates) == 0 {
		return "", "", fmt.Errorf("no %sByDataManagement* resolver on Query type", kind)
	}
	return "", "", fmt.Errorf("no %sByDataManagement* match; saw these DM-related fields: %s", kind, strings.Join(candidates, ", "))
}

// ---------------------------------------------------------------------------
// Probe 1 — read customProperties on tipRootComponentVersion
// ---------------------------------------------------------------------------

type propInfo struct {
	Name         string
	Value        any
	DefinitionID string
	ReadOnly     bool
}

// probeItemProperties (v3): walks DesignItem → tipRootModel → component,
// then reads baseProperties / allProperties / customProperties in one go. The
// `composition: WORKING` argument selects the current working state of the
// component. Pattern matches Patrick Rainsberry's fusion-data-demo-v3 Apollo
// queries.
func probeItemProperties(ctx context.Context, token, hubID, itemID string) (modelID, componentID string, baseProps []propInfo, err error) {
	const q = `
		query Nav($hubId: ID!, $itemId: ID!) {
		  item(hubId: $hubId, itemId: $itemId) {
		    __typename
		    ... on DesignItem {
		      tipRootModel {
		        id
		        component(composition: WORKING) {
		          id
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
	raw, err := gql(ctx, token, q, map[string]any{"hubId": hubID, "itemId": itemID})
	if raw == nil {
		return "", "", nil, err
	}
	// Non-fatal: partial-data errors are surfaced by caller; we still decode.
	var nav struct {
		Item struct {
			Typename      string `json:"__typename"`
			TipRootModel struct {
				ID        string `json:"id"`
				Component struct {
					ID             string `json:"id"`
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
	if jerr := json.Unmarshal(raw, &nav); jerr != nil {
		return "", "", nil, fmt.Errorf("decode: %w", jerr)
	}
	if nav.Item.Typename != "DesignItem" {
		return "", "", nil, fmt.Errorf("item is a %s, not a DesignItem", nav.Item.Typename)
	}
	modelID = nav.Item.TipRootModel.ID
	componentID = nav.Item.TipRootModel.Component.ID
	for _, p := range nav.Item.TipRootModel.Component.BaseProperties.Results {
		v := p.Value
		if v == nil && p.DisplayValue != "" {
			v = p.DisplayValue
		}
		baseProps = append(baseProps, propInfo{
			Name:         p.Name,
			Value:        v,
			DefinitionID: p.Definition.ID,
		})
	}
	return modelID, componentID, baseProps, err
}

// introspectTypeFields returns the names of all fields on a named GraphQL type.
func introspectTypeFields(ctx context.Context, token, typeName string) ([]string, error) {
	q := fmt.Sprintf(`
		query {
		  __type(name: "%s") {
		    fields { name }
		  }
		}`, typeName)
	raw, err := gql(ctx, token, q, nil)
	if err != nil {
		return nil, err
	}
	var r struct {
		Type *struct {
			Fields []struct {
				Name string `json:"name"`
			} `json:"fields"`
		} `json:"__type"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if r.Type == nil {
		return nil, fmt.Errorf("type %s not found", typeName)
	}
	out := make([]string, 0, len(r.Type.Fields))
	for _, f := range r.Type.Fields {
		out = append(out, f.Name)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Probe 2 — hub property-definition collections
// ---------------------------------------------------------------------------

type defInfo struct {
	ID         string
	Collection string
	ReadOnly   bool
}

func probeHubDefinitions(ctx context.Context, token, hubID string) (map[string]defInfo, error) {
	// v3 exposes built-in Fusion extended-property definitions via
	// `basePropertyDefinitionCollections`. `propertyDefinitionCollections`
	// (no prefix) holds only user-defined / custom collections.
	const q = `
		query ProbeDefs($hubId: ID!) {
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
	raw, gqlErr := gql(ctx, token, q, map[string]any{"hubId": hubID})
	if raw == nil {
		return nil, gqlErr
	}
	var r struct {
		Hub struct {
			BasePropertyDefinitionCollections struct {
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
		return nil, fmt.Errorf("decode hub response: %w", jerr)
	}
	out := make(map[string]defInfo)
	for _, coll := range r.Hub.BasePropertyDefinitionCollections.Results {
		for _, d := range coll.Definitions.Results {
			if d.IsArchived {
				continue
			}
			if _, already := out[d.Name]; already {
				continue
			}
			out[d.Name] = defInfo{
				ID:         d.ID,
				Collection: coll.Name,
				ReadOnly:   d.ReadOnly,
			}
		}
	}
	return out, gqlErr
}

// ---------------------------------------------------------------------------
// Probe 3 — mutation type introspection
// ---------------------------------------------------------------------------

func probeMutationIntrospection(ctx context.Context, token string) ([]string, error) {
	const q = `
		query ProbeMutations {
		  __type(name: "Mutation") {
		    fields { name }
		  }
		}`
	raw, err := gql(ctx, token, q, nil)
	if err != nil {
		return nil, err
	}
	var r struct {
		Type struct {
			Fields []struct {
				Name string `json:"name"`
			} `json:"fields"`
		} `json:"__type"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("decode introspection: %w", err)
	}
	names := make([]string, 0, len(r.Type.Fields))
	for _, f := range r.Type.Fields {
		names = append(names, f.Name)
	}
	return names, nil
}

// ---------------------------------------------------------------------------
// Probe 4 — opt-in no-op setProperties mutation
// ---------------------------------------------------------------------------

func probeNoopWrite(ctx context.Context, token, componentID string, existing []propInfo, field string) error {
	if componentID == "" {
		return errors.New("no componentId from probe 1 — cannot write")
	}

	// Look up the property's current value + definitionId from the component's
	// own baseProperties (stage 1). No hub-level lookup needed.
	var target *propInfo
	for i, p := range existing {
		if p.Name == field {
			target = &existing[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("field %q not found in baseProperties on this item — pick a -write-field that has a value set", field)
	}
	if target.DefinitionID == "" {
		return fmt.Errorf("field %q has no definition id returned — cannot build setProperties input", field)
	}
	if target.Value == nil {
		return fmt.Errorf("field %q has no existing value on this item — no-op write refuses to introduce new data", field)
	}

	const m = `
		mutation NoopWrite($input: SetPropertiesInput!) {
		  setProperties(input: $input) {
		    targetId
		    properties {
		      name
		      value
		      definition { id name }
		    }
		  }
		}`
	vars := map[string]any{
		"input": map[string]any{
			"targetId": componentID,
			"propertyInputs": []map[string]any{
				{"propertyDefinitionId": target.DefinitionID, "value": target.Value},
			},
		},
	}
	raw, err := gql(ctx, token, m, vars)
	if err != nil {
		return err
	}
	info("  server echoed: %s", string(raw))
	return nil
}

// ---------------------------------------------------------------------------
// GraphQL client — self-contained so we don't have to export internals.
// ---------------------------------------------------------------------------

// gql posts a GraphQL query and returns the `data` payload plus any errors.
// GraphQL can return both partial data and errors in the same response (e.g.
// the specific sub-path is denied but other fields resolve). Callers that only
// want hard failures should treat (nil, err) as fatal and (data!=nil, err!=nil)
// as "warnings — partial data is usable".
func gql(ctx context.Context, token, query string, vars map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("unauthorized (HTTP 401) — run the TUI once to refresh tokens")
	}
	var gr struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
			// GraphQL spec: path entries can be either strings (field names)
			// or integers (list indices). Decode as raw and stringify.
			Path []json.RawMessage `json:"path"`
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
	// Treat data-present + errors as partial success: return both.
	if len(gr.Data) == 0 || bytes.Equal(gr.Data, []byte("null")) {
		if gqlErr != nil {
			return nil, gqlErr
		}
		return nil, fmt.Errorf("empty response (HTTP %d)", resp.StatusCode)
	}
	return gr.Data, gqlErr
}

// ---------------------------------------------------------------------------
// Auth — load & refresh via the existing CLI packages.
// ---------------------------------------------------------------------------

func loadAccessToken(ctx context.Context) (string, error) {
	td, err := auth.LoadTokens()
	if err != nil {
		return "", err
	}
	if td == nil {
		return "", errors.New("no saved token — run the FusionDataCLI TUI once to sign in")
	}
	if td.Valid() {
		return td.AccessToken, nil
	}
	// Token is expired — refresh. `go run` doesn't get the Makefile's build-time
	// client_id, so seed APS_CLIENT_ID from .aps-client-id in CWD if present.
	if os.Getenv("APS_CLIENT_ID") == "" {
		if id, err := os.ReadFile(".aps-client-id"); err == nil {
			_ = os.Setenv("APS_CLIENT_ID", strings.TrimSpace(string(id)))
		}
	}
	if os.Getenv("APS_REGION") == "" {
		if r, err := os.ReadFile(".aps-region"); err == nil {
			_ = os.Setenv("APS_REGION", strings.TrimSpace(string(r)))
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("load config: %w (run the TUI once, or `make build` first, or set APS_CLIENT_ID)", err)
	}
	refreshed, err := auth.Refresh(ctx, cfg.ClientID, cfg.ClientSecret, td.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("refresh token: %w", err)
	}
	return refreshed.AccessToken, nil
}

// ---------------------------------------------------------------------------
// Pretty output
// ---------------------------------------------------------------------------

func hr() string                  { return strings.Repeat("─", 72) }
func die(f string, a ...any)      { fmt.Fprintf(os.Stderr, "FATAL: "+f+"\n", a...); os.Exit(1) }
func pass(f string, a ...any)     { fmt.Printf("\033[32m"+f+"\033[0m\n", a...) }
func fail(f string, a ...any)     { fmt.Printf("\033[31m"+f+"\033[0m\n", a...) }
func info(f string, a ...any)     { fmt.Printf(f+"\n", a...) }
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
func displayVal(v any) string {
	if v == nil {
		return "<nil>"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}
func sortedProps(in []propInfo) []propInfo {
	out := append([]propInfo(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
