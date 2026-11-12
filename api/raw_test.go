package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func rawServer(t *testing.T, status int, header map[string]string, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range header {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	swapEndpoint(t, srv.URL)
}

// QueryRaw keeps what gqlQuery drops: the data and every error, with
// path, code and correlation id, plus the query's point value.
func TestQueryRaw_KeepsDataAndErrors(t *testing.T) {
	rawServer(t, 200, nil, `{
		"data": {"t0": {"results": [null, {"id": "a"}]}},
		"errors": [
			{"message": "boom", "path": ["t0", "results", 0], "extensions": {"code": "INTERNAL_SERVER_ERROR", "errorType": "UNKNOWN", "correlation_id": "c-1"}},
			{"message": "nope", "path": ["t1"], "extensions": {"code": "FORBIDDEN", "correlationId": "c-2"}}
		],
		"extensions": {"pointValue": {"requestedQueryPointValue": 261}}
	}`)
	resp, err := QueryRaw(context.Background(), "tok", "query { x }", nil)
	if err != nil {
		t.Fatalf("QueryRaw: %v", err)
	}
	if resp.Status != 200 || resp.Points != 261 || len(resp.Data) == 0 {
		t.Errorf("status=%d points=%d data=%q", resp.Status, resp.Points, resp.Data)
	}
	if len(resp.Errors) != 2 {
		t.Fatalf("errors = %+v, want 2", resp.Errors)
	}
	e := resp.Errors[0]
	if e.Message != "boom" || e.Code != "INTERNAL_SERVER_ERROR" || e.ErrorType != "UNKNOWN" || e.CorrelationID != "c-1" {
		t.Errorf("first error = %+v", e)
	}
	if len(e.Path) != 3 || e.Path[0] != "t0" || e.Path[2] != float64(0) {
		t.Errorf("path = %#v", e.Path)
	}
	// Both spellings of the correlation id are read.
	if resp.Errors[1].CorrelationID != "c-2" {
		t.Errorf("second error = %+v", resp.Errors[1])
	}
}

// A throttled response is returned as a response, not an error, with
// its status and Retry-After for the caller to act on.
func TestQueryRaw_ReportsThrottling(t *testing.T) {
	rawServer(t, 429, map[string]string{"Retry-After": "12"},
		`{"errors":[{"message":"Query point value per minute quota exceeded","extensions":{"code":"GRAPHQL_VALIDATION_FAILED"}}]}`)
	resp, err := QueryRaw(context.Background(), "tok", "query { x }", nil)
	if err != nil {
		t.Fatalf("QueryRaw: %v", err)
	}
	if resp.Status != 429 || resp.RetryAfter != 12*time.Second || len(resp.Errors) != 1 || resp.Data != nil {
		t.Errorf("resp = %+v", resp)
	}
}

// A body that is not GraphQL at all still yields the status and body.
func TestQueryRaw_NonJSONBody(t *testing.T) {
	rawServer(t, 502, nil, "<html>Bad Gateway</html>")
	resp, err := QueryRaw(context.Background(), "tok", "query { x }", nil)
	if err != nil {
		t.Fatalf("QueryRaw: %v", err)
	}
	if resp.Status != 502 || string(resp.Body) != "<html>Bad Gateway</html>" || resp.Data != nil || len(resp.Errors) != 0 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := map[string]time.Duration{
		"":                              0,
		"30":                            30 * time.Second,
		"1.5":                           1500 * time.Millisecond,
		"0":                             0,
		"-4":                            0,
		"soon":                          0,
		"Fri, 02 Jan 2026 03:04:35 GMT": 30 * time.Second,
		"Fri, 02 Jan 2026 03:04:00 GMT": 0, // already past
	}
	for in, want := range cases {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %s, want %s", in, got, want)
		}
	}
}
