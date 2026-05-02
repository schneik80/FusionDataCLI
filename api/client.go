package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// graphqlEndpoint is a var (not const) so tests can point it at an
// httptest.Server. Production code never reassigns it.
var graphqlEndpoint = "https://developer.api.autodesk.com/mfg/graphql"

// region is the X-Ads-Region header value sent with every request.
// Empty means no header is sent (defaults to US on the server side).
var region string

// httpClient is the shared HTTP client used for every APS request.
// A single client with a tuned transport keeps connections alive across
// pagination and rapid navigation; per-call timeouts come from the caller's
// context (so streaming downloads aren't capped by a global Client.Timeout).
var httpClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	},
}

// SetRegion configures the ADS region header (e.g. "EMEA", "AUS").
// Call this once at startup from the config; an empty string or "US" sends no header.
func SetRegion(r string) {
	if r == "US" {
		r = ""
	}
	region = r
}

// SetGraphqlEndpointForTesting overrides the GraphQL endpoint URL and
// returns a function that restores the prior value. Intended only for
// tests that need to point the api package at an httptest.Server from a
// different test package (notably ui flow tests that drive a tea.Cmd which
// internally calls into api). Production code MUST NOT call this.
func SetGraphqlEndpointForTesting(url string) (restore func()) {
	prev := graphqlEndpoint
	graphqlEndpoint = url
	return func() { graphqlEndpoint = prev }
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

type gqlError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code      string `json:"code"`
		ErrorType string `json:"errorType"`
	} `json:"extensions"`
}

type gqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []gqlError      `json:"errors"`
}

// retryBackoffs is the delay before each retry attempt (the first attempt
// has no preceding delay, hence the leading 0). APS's MFG GraphQL gateway
// returns intermittent NOT_FOUND with errorType:UNKNOWN on valid hub URNs;
// see ~/Documents/aps-mfg-graphql-flakiness.md. Two retries with these
// delays absorb the observed flakiness while keeping total worst-case
// added latency under 2 s and well inside the 30 s nav-cmd context.
var retryBackoffs = []time.Duration{0, 500 * time.Millisecond, 1500 * time.Millisecond}

func gqlQuery(ctx context.Context, token, q string, vars map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(gqlRequest{Query: q, Variables: vars})
	if err != nil {
		return nil, err
	}

	var lastErr error
	for attempt, delay := range retryBackoffs {
		if delay > 0 {
			dbgLog("RETRY attempt=%d delay=%s lastErr=%v", attempt+1, delay, lastErr)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		data, err, retriable := gqlQueryOnce(ctx, token, body, vars)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if !retriable {
			return nil, err
		}
	}
	return nil, fmt.Errorf("APS GraphQL flaky after %d attempts: %w", len(retryBackoffs), lastErr)
}

// gqlQueryOnce performs a single HTTP round-trip. The third return value
// reports whether the error class is worth retrying — true for transport
// errors, HTTP 5xx/408/429, and GraphQL errors carrying
// extensions.errorType="UNKNOWN" (APS gateway's marker for intermittent
// upstream failures). False for HTTP 401, parse errors, and concrete
// GraphQL errors.
func gqlQueryOnce(ctx context.Context, token string, body []byte, vars map[string]any) (json.RawMessage, error, bool) {
	dbgLog("REQUEST vars=%v\n%s", vars, body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphqlEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err, false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if region != "" {
		req.Header.Set("X-Ads-Region", region)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err, true
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err, true
	}

	dbgLog("RESPONSE HTTP %d\n%s", resp.StatusCode, raw)

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("unauthorized (HTTP 401) — token may be expired or lacks scope/entitlement; body: %s", raw), false
	}
	if resp.StatusCode >= 500 || resp.StatusCode == 408 || resp.StatusCode == 429 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, raw), true
	}

	var gr gqlResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		return nil, fmt.Errorf("parsing GraphQL response: %w", err), false
	}
	if len(gr.Errors) > 0 {
		msgs := make([]string, len(gr.Errors))
		retriable := false
		for i, e := range gr.Errors {
			msgs[i] = e.Message
			if e.Extensions.ErrorType == "UNKNOWN" {
				retriable = true
			}
		}
		return nil, fmt.Errorf("GraphQL errors: %s", strings.Join(msgs, "; ")), retriable
	}
	if len(gr.Data) == 0 {
		return nil, fmt.Errorf("empty GraphQL response (HTTP %d): %s", resp.StatusCode, raw), false
	}
	return gr.Data, nil, false
}
