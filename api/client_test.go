package api

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/schneik80/FusionDataCLI/internal/testutil"
)

func TestSetRegion(t *testing.T) {
	orig := region
	t.Cleanup(func() { region = orig })

	cases := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty stays empty", input: "", want: ""},
		{name: "US is normalized to empty", input: "US", want: ""},
		{name: "EMEA is preserved", input: "EMEA", want: "EMEA"},
		{name: "AUS is preserved", input: "AUS", want: "AUS"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			SetRegion(tc.input)
			if region != tc.want {
				t.Errorf("SetRegion(%q): region = %q, want %q", tc.input, region, tc.want)
			}
		})
	}
}

// swapEndpoint redirects the package-level graphqlEndpoint to url and
// schedules restoration via t.Cleanup. Tests use this to point the GraphQL
// client at an httptest.Server.
func swapEndpoint(t *testing.T, url string) {
	t.Helper()
	prev := graphqlEndpoint
	t.Cleanup(func() { graphqlEndpoint = prev })
	graphqlEndpoint = url
}

func TestGqlQuery_HappyPath(t *testing.T) {
	var sawAuth, sawQuery, sawFoo bool
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		if req.AuthHeader == "Bearer test-token" {
			sawAuth = true
		} else {
			t.Errorf("AuthHeader = %q, want %q", req.AuthHeader, "Bearer test-token")
		}
		if strings.Contains(req.Query, "Marker") {
			sawQuery = true
		} else {
			t.Errorf("Query missing marker: %q", req.Query)
		}
		if v, ok := req.Variables["foo"].(string); ok && v == "bar" {
			sawFoo = true
		} else {
			t.Errorf("Variables[foo] = %v, want \"bar\"", req.Variables["foo"])
		}
		return testutil.GraphQLResponse{Data: map[string]any{"x": 1}}
	})
	swapEndpoint(t, srv.URL)

	ctx := context.Background()
	raw, err := gqlQuery(ctx, "test-token", "query Marker { hubs { id } }", map[string]any{"foo": "bar"})
	if err != nil {
		t.Fatalf("gqlQuery returned error: %v", err)
	}

	var got map[string]int
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decoding raw data: %v (raw=%s)", err, raw)
	}
	if got["x"] != 1 {
		t.Errorf("decoded data = %v, want {x:1}", got)
	}
	if !sawAuth || !sawQuery || !sawFoo {
		t.Errorf("handler missed an assertion: auth=%v query=%v foo=%v", sawAuth, sawQuery, sawFoo)
	}
}

func TestGqlQuery_401_Wraps(t *testing.T) {
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		return testutil.GraphQLResponse{Status: 401}
	})
	swapEndpoint(t, srv.URL)

	_, err := gqlQuery(context.Background(), "tok", "query Q {}", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unauthorized") {
		t.Errorf("error = %q, want substring \"unauthorized\"", err.Error())
	}
}

func TestGqlQuery_GraphQLErrors_Joined(t *testing.T) {
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		return testutil.GraphQLResponse{Errors: []string{"first failure", "second failure"}}
	})
	swapEndpoint(t, srv.URL)

	_, err := gqlQuery(context.Background(), "tok", "query Q {}", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "first failure; second failure") {
		t.Errorf("error = %q, want both messages joined by \"; \"", msg)
	}
}

func TestGqlQuery_EmptyData_Errors(t *testing.T) {
	// A response with no "data" field at all leaves gr.Data as a zero-length
	// json.RawMessage, which trips the production code's len(gr.Data) == 0
	// guard. (Strings like `""` decode to a 2-byte RawMessage and would
	// pass that check.)
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		return testutil.GraphQLResponse{RawBody: `{}`}
	})
	swapEndpoint(t, srv.URL)

	_, err := gqlQuery(context.Background(), "tok", "query Q {}", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "empty") {
		t.Errorf("error = %q, want substring \"empty\"", err.Error())
	}
}

func TestGqlQuery_RegionHeader(t *testing.T) {
	// Region is shared global state — back up & restore around the whole test.
	origRegion := region
	t.Cleanup(func() { region = origRegion })

	cases := []struct {
		name       string
		setRegion  string
		wantRegion string
	}{
		{name: "with_region", setRegion: "EMEA", wantRegion: "EMEA"},
		{name: "without_region", setRegion: "", wantRegion: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen atomic.Value // string
			seen.Store("")
			srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
				seen.Store(req.Region)
				return testutil.GraphQLResponse{Data: map[string]any{"ok": true}}
			})
			swapEndpoint(t, srv.URL)

			SetRegion(tc.setRegion)

			if _, err := gqlQuery(context.Background(), "tok", "query Q {}", nil); err != nil {
				t.Fatalf("gqlQuery: %v", err)
			}
			if got := seen.Load().(string); got != tc.wantRegion {
				t.Errorf("X-Ads-Region = %q, want %q", got, tc.wantRegion)
			}
		})
	}
}
