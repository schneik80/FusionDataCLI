package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// graphqlEndpoint is the Manufacturing Data Model API v3 endpoint. The
// v2 endpoint (/mfg/graphql) still answers, but its ComponentVersion /
// DrawingVersion types do not exist here — queries are not portable
// between the two.
//
// It is a var (not const) so tests can point it at an httptest.Server.
// Production code never reassigns it.
var graphqlEndpoint = "https://developer.api.autodesk.com/mfg/v3/graphql/public"

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

	// RootModelID is the id of the design's tipRootModel, captured at
	// items-list time so the asynchronous assembly classifier can issue
	// its assembly-relations probe without a second per-row round-trip.
	// Populated only for Kind == "design"; empty for everything else.
	RootModelID string

	// Subtype refines Kind for designs after classification: "assembly"
	// when the design has at least one sub-component, "part" when it
	// has none, "" while the classification is still in flight (or for
	// non-design rows). The Contents-column renderer reads this to
	// pick the icon; an unclassified design falls back to the generic
	// design icon until the refinement message lands.
	Subtype string
}

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type gqlError struct {
	Message string `json:"message"`
	// Path is the location of the error in the GraphQL response. Per spec
	// it is a list of strings (field names) and ints (array indices), so
	// we decode as []any. Path length is used to distinguish root-level
	// failures (the data is unusable) from leaf-level failures (the data
	// list still works, just one cell is null).
	Path       []any `json:"path"`
	Extensions struct {
		Code      string `json:"code"`
		ErrorType string `json:"errorType"`
	} `json:"extensions"`
}

type gqlResponse struct {
	Data       json.RawMessage `json:"data"`
	Errors     []gqlError      `json:"errors"`
	Extensions struct {
		PointValue struct {
			Requested int `json:"requestedQueryPointValue"`
		} `json:"pointValue"`
	} `json:"extensions"`
}

// retryBackoffs is the delay before each attempt at a request that
// failed transiently (the first attempt has no preceding delay, hence
// the leading 0): a transport error, HTTP 5xx/408, or a GraphQL error
// that nulled a root field with errorType UNKNOWN. The v2 gateway
// returned intermittent NOT_FOUND/UNKNOWN on valid hub URNs; two retries
// absorbed it. Throttling (HTTP 429) is not retried on this schedule —
// see throttle.go.
var retryBackoffs = []time.Duration{0, 500 * time.Millisecond, 1500 * time.Millisecond}

// maxThrottleWaits bounds how often one request waits out a throttle
// pause: several minutes in all, far beyond any quota window.
const maxThrottleWaits = 15

// outcome classifies one HTTP round-trip.
type outcome int

const (
	outcomeOK        outcome = iota
	outcomeFatal             // a real failure; asking again will not help
	outcomeTransient         // worth another attempt on retryBackoffs
	outcomeThrottled         // the per-minute point quota is spent
)

// result is what one round-trip produced.
type result struct {
	data       json.RawMessage
	err        error
	retryAfter time.Duration
	points     int // what the API charged, when it said
}

func gqlQuery(ctx context.Context, token, q string, vars map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(gqlRequest{Query: q, Variables: vars})
	if err != nil {
		return nil, err
	}

	var last result
	throttles := 0
	for attempt := 0; attempt < len(retryBackoffs); {
		if delay := retryBackoffs[attempt]; delay > 0 {
			dbgLog("RETRY attempt=%d delay=%s lastErr=%v", attempt+1, delay, last.err)
			if err := sleepCtx(ctx, delay); err != nil {
				return nil, err
			}
		}
		// Hold while the API is throttling us, and while the request
		// would not fit in what is left of the minute's points.
		reserved, err := quota.admit(ctx, costs.estimate(q))
		if err != nil {
			if throttles > 0 {
				return nil, fmt.Errorf("%w (%v)", ErrThrottled, err)
			}
			return nil, err
		}

		res, oc := gqlQueryOnce(ctx, token, body, vars)
		quota.settle(reserved, res.points)
		costs.observe(q, res.points)
		switch oc {
		case outcomeOK:
			return res.data, nil
		case outcomeFatal:
			return nil, res.err
		case outcomeThrottled:
			// Not an attempt: the request was never run. Everything
			// waits out the pause, then this request goes again, for
			// as long as the caller's context allows.
			last = res
			quota.throttled(res.retryAfter)
			// The context normally ends the wait; the cap is for
			// callers that passed one without a deadline.
			if throttles++; throttles > maxThrottleWaits {
				return nil, fmt.Errorf("%w (%v)", ErrThrottled, res.err)
			}
			continue
		}
		last = res
		attempt++
	}
	return nil, fmt.Errorf("%w after %d attempts: %w", ErrFlaky, len(retryBackoffs), last.err)
}

// ErrFlaky is returned (wrapped) when a request kept failing in a way
// that looked transient. The same request may succeed later.
var ErrFlaky = errors.New("APS GraphQL flaky")

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// deterministicCodes are GraphQL error codes that describe the request
// or the data, not the weather: the same request gets the same answer,
// however errorType is labelled. EXCEPTION_CLOSED_COMMUNITY, for one,
// arrives as errorType UNKNOWN on the fusionWebUrl of every document in
// a closed project, and DATA_HAS_ERROR on the assemblyRelations of a
// document whose saved data is damaged.
var deterministicCodes = map[string]bool{
	"EXCEPTION_CLOSED_COMMUNITY": true,
	"DATA_HAS_ERROR":             true,
	"FORBIDDEN":                  true,
	"UNAUTHORIZED":               true,
	"BAD_REQUEST":                true,
	"BAD_USER_INPUT":             true,
	"GRAPHQL_VALIDATION_FAILED":  true,
}

// gqlQueryOnce performs a single HTTP round-trip and classifies it.
func gqlQueryOnce(ctx context.Context, token string, body []byte, vars map[string]any) (result, outcome) {
	dbgLog("REQUEST vars=%v\n%s", vars, body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphqlEndpoint, bytes.NewReader(body))
	if err != nil {
		return result{err: err}, outcomeFatal
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if region != "" {
		req.Header.Set("X-Ads-Region", region)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return result{err: err}, outcomeFatal
		}
		return result{err: err}, outcomeTransient
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return result{err: err}, outcomeTransient
	}

	dbgLog("RESPONSE HTTP %d\n%s", resp.StatusCode, raw)

	if resp.StatusCode == http.StatusUnauthorized {
		return result{err: fmt.Errorf("unauthorized (HTTP 401) — token may be expired or lacks scope/entitlement; body: %s", raw)}, outcomeFatal
	}
	if resp.StatusCode == http.StatusTooManyRequests || isQuotaBody(raw) {
		return result{
			err:        fmt.Errorf("HTTP %d: %s", resp.StatusCode, raw),
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}, outcomeThrottled
	}
	if resp.StatusCode >= 500 || resp.StatusCode == 408 {
		return result{err: fmt.Errorf("HTTP %d: %s", resp.StatusCode, raw)}, outcomeTransient
	}

	var gr gqlResponse
	if err := json.Unmarshal(raw, &gr); err != nil {
		return result{err: fmt.Errorf("parsing GraphQL response: %w", err)}, outcomeFatal
	}
	points := gr.Extensions.PointValue.Requested

	hasData := len(gr.Data) > 0 && string(gr.Data) != "null"
	if len(gr.Errors) == 0 {
		if !hasData {
			return result{points: points, err: fmt.Errorf("empty GraphQL response (HTTP %d): %s", resp.StatusCode, raw)}, outcomeFatal
		}
		return result{points: points, data: gr.Data}, outcomeOK
	}

	// Errors and, possibly, data. Three cases:
	//   - An UNKNOWN error at or near the root (path depth ≤ 2) is the
	//     gateway flakiness the retry exists for — unless its code says
	//     the failure is a fact about the data, which no retry changes.
	//   - An error that nulled a root field leaves nothing to show.
	//   - Anything else left the rest of the answer usable: asking
	//     again returns the same partial response, so the data is
	//     passed through as it is.
	msgs := make([]string, len(gr.Errors))
	rootLost, transient := false, false
	for i, e := range gr.Errors {
		msgs[i] = describeError(e)
		lost := !hasData || rootIsNull(gr.Data, e.Path)
		if lost {
			rootLost = true
		}
		if (lost || len(e.Path) <= 2) && e.Extensions.ErrorType == "UNKNOWN" && !deterministicCodes[e.Extensions.Code] {
			transient = true
		}
	}
	gerr := fmt.Errorf("GraphQL errors: %s", strings.Join(msgs, "; "))
	switch {
	case transient:
		return result{points: points, err: gerr}, outcomeTransient
	case rootLost:
		return result{points: points, err: gerr}, outcomeFatal
	default:
		dbgLog("GraphQL partial errors (kept data): %s", strings.Join(msgs, "; "))
		return result{points: points, data: gr.Data}, outcomeOK
	}
}

// describeError renders one GraphQL error with what is needed to tell a
// real failure from a transient one in a log or on the error screen:
// the code and where in the response it struck.
func describeError(e gqlError) string {
	s := e.Message
	var tags []string
	if e.Extensions.Code != "" {
		tags = append(tags, e.Extensions.Code)
	}
	if len(e.Path) > 0 {
		parts := make([]string, len(e.Path))
		for i, p := range e.Path {
			parts[i] = fmt.Sprint(p)
		}
		tags = append(tags, "at "+strings.Join(parts, "."))
	}
	if len(tags) > 0 {
		s += " [" + strings.Join(tags, " ") + "]"
	}
	return s
}

// rootIsNull reports whether the root field an error path starts at is
// null (or absent) in data. An error with no path is taken to concern
// the whole response.
func rootIsNull(data json.RawMessage, path []any) bool {
	if len(path) == 0 {
		return true
	}
	name, ok := path[0].(string)
	if !ok {
		return true
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return true
	}
	v, present := root[name]
	return !present || string(v) == "null"
}
