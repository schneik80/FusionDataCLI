package api

import (
	"context"
	"encoding/json"
	"fmt"
)

// classifySem caps concurrent in-flight assembly-classification calls so
// the gateway isn't hammered when the contents column lands on a folder
// with dozens of designs. 8 is empirically a good balance between
// wall-clock throughput and gateway-side cost (measured against a real
// hub on the v2 API; the v3 probe is the same shape and cost).
var classifySem = make(chan struct{}, 8)

// ClassifyAssembly reports whether the design rooted at the given model
// id has at least one direct child. The query asks assemblyRelations for
// a single depth-1 result; an empty response means the design is a part,
// any result means an assembly.
//
// Component.hasChildren looks like the cheaper signal and could ride
// along on the items list, but it is not usable: it reports true for
// single-body parts (a library screw with no relations at all), so the
// relation probe stays.
//
// The call blocks on a package-level semaphore so concurrent fan-out
// from a single contents-loaded message stays bounded. Callers should
// pair the returned bool with the originating item id and a generation
// counter so late-arriving refinements after a folder change can be
// dropped on the floor.
func ClassifyAssembly(ctx context.Context, token, modelID string) (bool, error) {
	if modelID == "" {
		return false, fmt.Errorf("classify: empty modelID")
	}
	select {
	case classifySem <- struct{}{}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	defer func() { <-classifySem }()

	const q = `
		query ClassifyAssembly($modelId: ID!) {
			model(modelId: $modelId) {
				assemblyRelations(depth: 1, pagination: { limit: 1 }) {
					results { id }
				}
			}
		}`
	data, err := gqlQuery(ctx, token, q, map[string]any{"modelId": modelID})
	if err != nil {
		return false, err
	}
	var r struct {
		Model struct {
			AssemblyRelations struct {
				Results []struct {
					ID string `json:"id"`
				} `json:"results"`
			} `json:"assemblyRelations"`
		} `json:"model"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return false, fmt.Errorf("classify decode: %w", err)
	}
	return len(r.Model.AssemblyRelations.Results) > 0, nil
}
