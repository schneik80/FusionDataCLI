package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"
)

// RawError is one entry of a GraphQL response's "errors" array, with
// the parts a caller needs to decide what the error applies to.
type RawError struct {
	Message string
	// Path locates the error in the response: field names (and aliases)
	// as strings, list indices as float64, per encoding/json.
	Path          []any
	Code          string // extensions.code, e.g. FORBIDDEN, INTERNAL_SERVER_ERROR
	ErrorType     string // extensions.errorType, e.g. UNKNOWN
	CorrelationID string // extensions.correlation_id / correlationId
}

// RawResponse is the unprocessed outcome of one GraphQL round-trip.
type RawResponse struct {
	Status     int           // HTTP status
	RetryAfter time.Duration // Retry-After header, 0 if absent or unparseable
	Data       json.RawMessage
	Errors     []RawError
	Points     int    // extensions.pointValue.requestedQueryPointValue, 0 if absent
	Body       []byte // the raw body, for diagnostics when it is not GraphQL JSON
}

// QueryRaw performs exactly one GraphQL round-trip and reports
// everything that came back. It is the counterpart to the browsing
// queries' gqlQuery, which retries a little and then passes partial
// data through with the errors dropped — the right trade for an
// interactive list, the wrong one for an audit that has to account for
// every row. QueryRaw makes no retry and hides nothing: the caller owns
// the retry policy and sees each error with its path.
//
// The returned error is non-nil only when no HTTP response was obtained
// (transport failure, cancelled context). Any HTTP status, including
// 429 and 5xx, is a RawResponse.
func QueryRaw(ctx context.Context, token, q string, vars map[string]any) (*RawResponse, error) {
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

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	dbgLog("RESPONSE HTTP %d\n%s", resp.StatusCode, raw)

	out := &RawResponse{
		Status:     resp.StatusCode,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		Body:       raw,
	}

	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message    string `json:"message"`
			Path       []any  `json:"path"`
			Extensions struct {
				Code           string `json:"code"`
				ErrorType      string `json:"errorType"`
				CorrelationID  string `json:"correlation_id"`
				CorrelationID2 string `json:"correlationId"`
			} `json:"extensions"`
		} `json:"errors"`
		Extensions struct {
			PointValue struct {
				Requested int `json:"requestedQueryPointValue"`
			} `json:"pointValue"`
		} `json:"extensions"`
	}
	// A body that is not GraphQL JSON (a gateway's HTML error page, an
	// empty 429) leaves Data and Errors empty; Status and Body say why.
	if json.Unmarshal(raw, &env) == nil {
		if len(env.Data) > 0 && string(env.Data) != "null" {
			out.Data = env.Data
		}
		out.Points = env.Extensions.PointValue.Requested
		for _, e := range env.Errors {
			cid := e.Extensions.CorrelationID
			if cid == "" {
				cid = e.Extensions.CorrelationID2
			}
			out.Errors = append(out.Errors, RawError{
				Message:       e.Message,
				Path:          e.Path,
				Code:          e.Extensions.Code,
				ErrorType:     e.Extensions.ErrorType,
				CorrelationID: cid,
			})
		}
	}
	return out, nil
}

// parseRetryAfter reads a Retry-After header: either a number of
// seconds or an HTTP date. Anything else, or a time already past, is 0.
func parseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseFloat(v, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}
