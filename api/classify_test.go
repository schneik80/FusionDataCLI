package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/schneik80/FusionDataCLI/internal/testutil"
)

func TestClassifyAssembly_Assembly(t *testing.T) {
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		// depth: 1 keeps the probe to direct children; without it the
		// API walks the whole tree to answer a yes/no question.
		if !strings.Contains(req.Query, "assemblyRelations(depth: 1, pagination") {
			t.Errorf("query missing depth-1 assemblyRelations: %q", req.Query)
		}
		if !strings.Contains(req.Query, "limit: 1") {
			t.Errorf("query should request limit:1, got: %q", req.Query)
		}
		if got, _ := req.Variables["modelId"].(string); got != "urn:model:asm" {
			t.Errorf("modelId variable = %v, want urn:model:asm", req.Variables["modelId"])
		}
		return testutil.GraphQLResponse{Data: map[string]any{
			"model": map[string]any{
				"assemblyRelations": map[string]any{
					// limit is a page-size hint in v3: asking for 1 can
					// return more. Any result at all means assembly.
					"results": []map[string]any{
						{"id": "rel-1"},
						{"id": "rel-2"},
					},
				},
			},
		}}
	})
	swapEndpoint(t, srv.URL)

	got, err := ClassifyAssembly(context.Background(), "tok", "urn:model:asm")
	if err != nil {
		t.Fatalf("ClassifyAssembly: %v", err)
	}
	if !got {
		t.Errorf("expected isAssembly=true for non-empty assemblyRelations, got false")
	}
}

func TestClassifyAssembly_Part(t *testing.T) {
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		return testutil.GraphQLResponse{Data: map[string]any{
			"model": map[string]any{
				"assemblyRelations": map[string]any{
					"results": []map[string]any{}, // empty = part
				},
			},
		}}
	})
	swapEndpoint(t, srv.URL)

	got, err := ClassifyAssembly(context.Background(), "tok", "urn:model:part")
	if err != nil {
		t.Fatalf("ClassifyAssembly: %v", err)
	}
	if got {
		t.Errorf("expected isAssembly=false for empty assemblyRelations, got true")
	}
}

func TestClassifyAssembly_EmptyModelID(t *testing.T) {
	// No GraphQL server registered — if we issued a request, it'd hit
	// the live endpoint (or fail DNS). The empty-id check must short-circuit.
	_, err := ClassifyAssembly(context.Background(), "tok", "")
	if err == nil {
		t.Errorf("expected error for empty modelID, got nil")
	}
	if !strings.Contains(err.Error(), "empty modelID") {
		t.Errorf("error = %q, want it to mention empty modelID", err.Error())
	}
}

func TestClassifyAssembly_GraphQLError(t *testing.T) {
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		return testutil.GraphQLResponse{
			Errors: []string{"Requested resource not found."},
		}
	})
	swapEndpoint(t, srv.URL)

	_, err := ClassifyAssembly(context.Background(), "tok", "urn:model:missing")
	if err == nil {
		t.Fatalf("expected error from GraphQL errors[], got nil")
	}
	if !strings.Contains(err.Error(), "Requested resource not found") {
		t.Errorf("error = %q, expected GraphQL message verbatim", err.Error())
	}
}

func TestClassifyAssembly_ContextCancelledOnSemaphoreWait(t *testing.T) {
	// Saturate the semaphore so the next call has to wait, then
	// cancel its context to verify the semaphore-acquire select
	// honours context cancellation.
	for i := 0; i < cap(classifySem); i++ {
		classifySem <- struct{}{}
	}
	t.Cleanup(func() {
		for i := 0; i < cap(classifySem); i++ {
			<-classifySem
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel synchronously so the select sees ctx.Done immediately.
	cancel()

	_, err := ClassifyAssembly(ctx, "tok", "urn:model:any")
	if err == nil {
		t.Fatalf("expected ctx cancellation error, got nil")
	}
	if err != context.Canceled {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestClassifyAssembly_SemaphoreReleasesAfterCall(t *testing.T) {
	// Verify the semaphore slot is returned even after a success, so
	// subsequent classifications aren't blocked indefinitely.
	srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
		return testutil.GraphQLResponse{Data: map[string]any{
			"model": map[string]any{
				"assemblyRelations": map[string]any{"results": []map[string]any{}},
			},
		}}
	})
	swapEndpoint(t, srv.URL)

	// Run more calls than the semaphore size to force re-use.
	const n = 12 // cap is 8
	for i := 0; i < n; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if _, err := ClassifyAssembly(ctx, "tok", "urn:model:x"); err != nil {
			t.Fatalf("call #%d failed: %v", i, err)
		}
		cancel()
	}
	// Confirm the channel is fully drained.
	if got, want := len(classifySem), 0; got != want {
		t.Errorf("classifySem occupancy after %d calls = %d, want %d", n, got, want)
	}
}
