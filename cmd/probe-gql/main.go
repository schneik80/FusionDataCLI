// probe-gql is a diagnostic that sends one arbitrary GraphQL document to
// an APS Manufacturing Data Model endpoint using the cached login, and
// prints the raw response. It exists so schema questions ("does v3 expose
// X yet?") can be answered against the live gateway instead of the docs.
//
// Usage:
//
//	go run ./cmd/probe-gql -q '{ hubs { results { id name } } }'
//	go run ./cmd/probe-gql -v3 -f query.graphql -vars '{"hubId":"..."}'
//	go run ./cmd/probe-gql -v3 -type Model          # introspect one type
//	go run ./cmd/probe-gql -v3 -roots               # list Query root fields
//
// Reads cached tokens from ~/.config/fusiondatacli/tokens.json and
// refreshes them if expired. Will not open a browser — if the refresh
// token has lapsed, run the main app once to log in.
//
// Not part of the release binary.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/schneik80/FusionDataCLI/auth"
	"github.com/schneik80/FusionDataCLI/config"
)

const (
	endpointV2 = "https://developer.api.autodesk.com/mfg/graphql"
	endpointV3 = "https://developer.api.autodesk.com/mfg/v3/graphql/public"
)

// The gateway drops variables on introspection requests ("missing value
// for non-null variable"), so the type name is inlined via %q.
const typeQuery = `{ __type(name: %q) {
	name kind description
	possibleTypes { name }
	enumValues { name }
	inputFields { name type { ...T } }
	fields(includeDeprecated: true) {
		name isDeprecated deprecationReason
		args { name type { ...T } }
		type { ...T }
	}
} }
fragment T on __Type { name kind ofType { name kind ofType { name kind ofType { name kind } } } }`

const rootsQuery = `{ __schema { queryType { fields {
	name
	args { name type { ...T } }
	type { ...T }
} } } }
fragment T on __Type { name kind ofType { name kind ofType { name kind ofType { name kind } } } }`

func main() {
	v3 := flag.Bool("v3", false, "target the v3 endpoint instead of v2")
	endpoint := flag.String("endpoint", "", "explicit endpoint URL (overrides -v3)")
	query := flag.String("q", "", "GraphQL document")
	file := flag.String("f", "", "read the GraphQL document from this file")
	vars := flag.String("vars", "", "variables as a JSON object")
	typeName := flag.String("type", "", "introspect this type instead of running a query")
	roots := flag.Bool("roots", false, "list the Query root fields")
	region := flag.String("region", "", "X-Ads-Region header value")
	login := flag.Bool("login", false, "open the browser to sign in when the cached refresh token has lapsed")
	flag.Parse()

	url := endpointV2
	if *v3 {
		url = endpointV3
	}
	if *endpoint != "" {
		url = *endpoint
	}

	doc := *query
	variables := map[string]any{}
	switch {
	case *typeName != "":
		doc = fmt.Sprintf(typeQuery, *typeName)
	case *roots:
		doc = rootsQuery
	case *file != "":
		b, err := os.ReadFile(*file)
		if err != nil {
			fail("read query", err)
		}
		doc = string(b)
	}
	if doc == "" {
		fail("usage", fmt.Errorf("one of -q, -f, -type, -roots is required"))
	}
	if *vars != "" {
		if err := json.Unmarshal([]byte(*vars), &variables); err != nil {
			fail("parse -vars", err)
		}
	}

	token, err := loadOrRefreshToken(*login)
	if err != nil {
		fail("auth", err)
	}

	body, err := json.Marshal(map[string]any{"query": doc, "variables": variables})
	if err != nil {
		fail("encode", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		fail("request", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if *region != "" {
		req.Header.Set("X-Ads-Region", *region)
	}

	t0 := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fail("http", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		fail("read body", err)
	}
	fmt.Fprintf(os.Stderr, "%s  HTTP %d  %s\n", url, resp.StatusCode, time.Since(t0).Round(time.Millisecond))

	var pretty bytes.Buffer
	if json.Indent(&pretty, raw, "", "  ") == nil {
		raw = pretty.Bytes()
	}
	fmt.Println(string(raw))
	if resp.StatusCode/100 != 2 {
		os.Exit(1)
	}
}

func loadOrRefreshToken(login bool) (string, error) {
	td, err := auth.LoadTokens()
	if err != nil {
		return "", err
	}
	if td.Valid() {
		return td.AccessToken, nil
	}
	cfg, err := config.Load()
	if err != nil || cfg == nil || cfg.ClientID == "" {
		return "", fmt.Errorf("token expired and no client_id available (build with the Makefile LDFLAGS or set APS_CLIENT_ID): %v", err)
	}
	var refreshErr error
	if td != nil && td.RefreshToken != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// Refresh saves the rotated token pair itself.
		td2, err := auth.Refresh(ctx, cfg.ClientID, cfg.ClientSecret, td.RefreshToken)
		if err == nil {
			return td2.AccessToken, nil
		}
		refreshErr = err
	}
	if !login {
		return "", fmt.Errorf("no usable cached login (refresh: %v) — rerun with -login or run the main app once", refreshErr)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	td3, err := auth.Login(ctx, cfg.ClientID, cfg.ClientSecret)
	if err != nil {
		return "", fmt.Errorf("login: %w", err)
	}
	return td3.AccessToken, nil
}

func fail(label string, err error) {
	fmt.Fprintf(os.Stderr, "probe-gql %s: %v\n", label, err)
	os.Exit(1)
}
