package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const graphqlEndpoint = "https://developer.api.autodesk.com/mfg/graphql"

// region is the X-Ads-Region header value sent with every request.
// Empty means no header is sent (defaults to US on the server side).
var region string

// SetRegion configures the ADS region header (e.g. "EMEA", "AUS").
// Call this once at startup from the config; an empty string or "US" sends no header.
func SetRegion(r string) {
	if r == "US" {
		r = ""
	}
	region = r
}

// NavItem is a navigable node in the APS Manufacturing Data Model hierarchy.
type NavItem struct {
	ID          string
	Name        string
	Kind        string // "hub" | "project" | "folder" | "design" | "configured" | "unknown"
	AltID       string // alternativeIdentifier (data management API ID)
	WebURL      string // direct web URL when provided by the API
	IsContainer bool   // true if this item can be entered (hub, project, folder)
}

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func gqlQuery(ctx context.Context, token, q string, vars map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(gqlRequest{Query: q, Variables: vars})
	if err != nil {
		return nil, err
	}

	dbgLog("REQUEST vars=%v\n%s", vars, body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphqlEndpoint, bytes.NewReader(body))
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

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("unauthorized — token may be expired (re-run to reauthenticate)")
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	dbgLog("RESPONSE HTTP %d\n%s", resp.StatusCode, raw)

	var gr gqlResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, fmt.Errorf("parsing GraphQL response: %w", err)
	}
	if len(gr.Errors) > 0 {
		msgs := make([]string, len(gr.Errors))
		for i, e := range gr.Errors {
			msgs[i] = e.Message
		}
		return nil, fmt.Errorf("GraphQL errors: %s", strings.Join(msgs, "; "))
	}
	if len(gr.Data) == 0 {
		return nil, fmt.Errorf("empty GraphQL response (HTTP %d): %s", resp.StatusCode, raw)
	}
	return gr.Data, nil
}
