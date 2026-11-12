package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// ComponentRef describes one row of the Uses or Where Used tab.
//
//   - Uses on a design: a direct child Model of the design's root model.
//   - Uses on a drawing: the Model the drawing documents.
//   - Where Used: an Item (design, configured design, or drawing) that
//     references the selected design.
type ComponentRef struct {
	ID             string // Model.id (Uses) or Item.id (Where Used)
	Name           string
	PartNumber     string
	PartDesc       string
	Material       string
	DesignItemID   string // owning Item.id — the Show-in-Location target
	DesignItemName string // owning Item.name
	FusionWebURL   string // owning Item.fusionWebUrl, if exposed
	// Kind is set on Where Used rows, where the referencing item is not
	// necessarily a design: "design" | "configured" | "drawing" |
	// "unknown". Empty on Uses rows.
	Kind string
	// Relation is set on Where Used rows: RelationDirect when the item
	// references the selected design itself, RelationIndirect when it only
	// reaches it through a sub-assembly (or, for a drawing, documents such
	// an assembly). Empty when that could not be determined.
	Relation string
	// Root is set on Where Used rows for a design that is itself used by
	// no other design: the top of a parent chain. It is only set when the
	// API's answer for that row was complete; false means "not a root or
	// not known".
	Root bool
	// RootModelID is set on Where Used design rows: the referencing
	// design's tipRootModel id, which GetWhereUsedRelations needs. A
	// design row with this set and Relation empty has not been checked
	// yet, or its check failed.
	RootModelID string
}

// Where Used relation values.
const (
	RelationDirect   = "direct"
	RelationIndirect = "indirect"
)

// DrawingRef describes a drawing shown in the Drawings tab.
// DrawingItemID is the DrawingItem.id — needed to drive
// Show-In-Location navigation from this row to the Contents column.
type DrawingRef struct {
	ID            string
	Name          string
	DrawingItemID string
	ModifiedOn    time.Time
	ModifiedBy    string
	FusionWebURL  string
}

// refsPageSize is the page-size hint for the Where Used query, and for
// the where-used list nested under each of its design rows. With both
// at 25 the query scores ~800 of the 1000-point budget; 50 is over.
const refsPageSize = 25

// propValue is the v3 Property wrapper around name / partNumber /
// description / materialName. Only the display string is needed here.
type propValue struct {
	Value any `json:"value"`
}

// String flattens the PropertyValue scalar. It is a string for every
// property this package reads, but the scalar is untyped in the schema,
// so anything else is rendered rather than dropped.
func (p *propValue) String() string {
	if p == nil || p.Value == nil {
		return ""
	}
	if s, ok := p.Value.(string); ok {
		return s
	}
	return fmt.Sprint(p.Value)
}

// modelRef is the selection shared by every query that surfaces a Model
// as a ComponentRef row. Every level is nullable in the schema.
type modelRef struct {
	ID        string     `json:"id"`
	Name      *propValue `json:"name"`
	Material  *propValue `json:"materialName"`
	Component *struct {
		PartNumber  *propValue `json:"partNumber"`
		Description *propValue `json:"description"`
	} `json:"component"`
	DesignItem *struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		FusionWebURL string `json:"fusionWebUrl"`
	} `json:"designItem"`
}

const modelRefFields = `
	id
	name { value }
	materialName { value }
	component { partNumber { value } description { value } }
	designItem { id name fusionWebUrl }`

func (m *modelRef) toRef() ComponentRef {
	r := ComponentRef{
		ID:       m.ID,
		Name:     m.Name.String(),
		Material: m.Material.String(),
	}
	if m.Component != nil {
		r.PartNumber = m.Component.PartNumber.String()
		r.PartDesc = m.Component.Description.String()
	}
	if m.DesignItem != nil {
		r.DesignItemID = m.DesignItem.ID
		r.DesignItemName = m.DesignItem.Name
		r.FusionWebURL = m.DesignItem.FusionWebURL
	}
	return r
}

// GetOccurrences returns the direct children of the given root model
// (the "Uses" relationship), one row per instance.
//
// depth: 1 is load-bearing: assemblyRelations defaults to the whole
// tree, which on a mid-sized assembly is hundreds of rows across every
// sub-assembly instead of the immediate children the tab shows.
func GetOccurrences(ctx context.Context, token, modelID string) ([]ComponentRef, error) {
	const body = `{
					pagination { cursor }
					results { id toModel {` + modelRefFields + ` } }
				}
			}
		}`
	qFirst := fmt.Sprintf(`
		query GetOccurrences($modelId: ID!) {
			model(modelId: $modelId) {
				assemblyRelations(depth: 1, pagination: { limit: %d }) `, pageSize) + body
	qNext := fmt.Sprintf(`
		query GetOccurrencesNext($modelId: ID!, $cursor: String!) {
			model(modelId: $modelId) {
				assemblyRelations(depth: 1, pagination: { cursor: $cursor, limit: %d }) `, pageSize) + body

	type relResult struct {
		ID      string    `json:"id"`
		ToModel *modelRef `json:"toModel"`
	}

	all, err := allPages(ctx, token, qFirst, qNext, map[string]any{"modelId": modelID}, func(data json.RawMessage) (string, []relResult, error) {
		var r struct {
			Model struct {
				AssemblyRelations struct {
					Pagination struct {
						Cursor string `json:"cursor"`
					} `json:"pagination"`
					Results []relResult `json:"results"`
				} `json:"assemblyRelations"`
			} `json:"model"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return "", nil, fmt.Errorf("occurrences: %w", err)
		}
		return r.Model.AssemblyRelations.Pagination.Cursor, r.Model.AssemblyRelations.Results, nil
	})
	if err != nil {
		return nil, err
	}

	refs := make([]ComponentRef, 0, len(all))
	for _, rel := range all {
		if rel.ToModel == nil {
			continue
		}
		refs = append(refs, rel.ToModel.toRef())
	}
	return refs, nil
}

// GetDrawingSource returns the model a drawing documents. For drawings,
// "Uses" means the design the drawing was made from: the tip drawing's
// model and its owning DesignItem. A drawing has one source, so the
// result has at most one entry; it is a slice so the Uses tab renderer
// stays polymorphic across designs (children) and drawings (source).
func GetDrawingSource(ctx context.Context, token, hubID, drawingItemID string) ([]ComponentRef, error) {
	const q = `
		query GetDrawingSource($hubId: ID!, $itemId: ID!) {
			item(hubId: $hubId, itemId: $itemId) {
				... on DrawingItem {
					tipDrawing {
						model {` + modelRefFields + ` }
					}
				}
			}
		}`

	data, err := gqlQuery(ctx, token, q, map[string]any{"hubId": hubID, "itemId": drawingItemID})
	if err != nil {
		return nil, fmt.Errorf("drawing source: %w", err)
	}

	var raw struct {
		Item struct {
			TipDrawing *struct {
				Model *modelRef `json:"model"`
			} `json:"tipDrawing"`
		} `json:"item"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("drawing source decode: %w", err)
	}

	// No tip drawing, or a drawing whose source model is gone: return an
	// empty list so the UI shows the tab's empty state.
	if raw.Item.TipDrawing == nil || raw.Item.TipDrawing.Model == nil {
		return nil, nil
	}
	ref := raw.Item.TipDrawing.Model.toRef()
	if ref.ID == "" && ref.DesignItemID == "" {
		return nil, nil
	}
	return []ComponentRef{ref}, nil
}

// GetWhereUsed returns the items that reference the given design (the
// reverse-lookup "Where Used" relationship).
//
// Two things differ from the v2 ComponentVersion.whereUsed this
// replaced:
//
//   - Results are Items, not component versions, so there is one row per
//     referencing item with no per-version duplicates to collapse.
//   - Drawings come back alongside designs, and on live data the list
//     includes assemblies that contain the design indirectly (through a
//     sub-assembly), not only its immediate parents. Kind lets the UI
//     tell the row types apart.
//
// Nothing in the whereUsed response separates direct from indirect, so
// Relation is worked out separately:
//
//   - A drawing is direct when it is one of this design's own drawings,
//     and indirect when it documents an assembly further up. The design's
//     DesignItem.drawingItems list — the same field the Drawings tab
//     reads — rides along on this query and decides it, so the two tabs
//     cannot disagree. (Asking each drawing for tipDrawing.model instead
//     is not reliable: that field is an internal server error on some
//     drawings, which would leave them untagged.)
//   - A design needs a look at its own first-level references. That is a
//     second request, so it is not made here: design rows come back with
//     Relation empty and RootModelID set, and GetWhereUsedRelations
//     fills them in. The caller can show the rows in the meantime.
//
// Root marks the end of a parent chain. Each design row carries its own
// whereUsed list, and a design with no design in that list has no
// parent. The flag is left unset when that nested list was cut short by
// paging, and for configured designs, which are not asked.
func GetWhereUsed(ctx context.Context, token, hubID, itemID string) ([]ComponentRef, error) {
	const body = `{
					pagination { cursor }
					results {
						__typename id name
						... on DesignItem {
							fusionWebUrl
							tipRootModel {
								id
								materialName { value }
								component { partNumber { value } description { value } }
							}
							whereUsed(pagination: { limit: %[3]d }) {
								pagination { cursor }
								results { __typename }
							}
						}
						... on ConfiguredDesignItem { fusionWebUrl }
						... on DrawingItem { fusionWebUrl }
					}
				}
			}
		}`
	qFirst := fmt.Sprintf(`
		query GetWhereUsed($hubId: ID!, $itemId: ID!) {
			item(hubId: $hubId, itemId: $itemId) {
				... on DesignItem {
					drawingItems(pagination: { limit: %[2]d }) {
						pagination { cursor }
						results { id }
					}
				}
				whereUsed(pagination: { limit: %[1]d }) `+body, firstPageSize, pageSize, refsPageSize)
	qNext := fmt.Sprintf(`
		query GetWhereUsedNext($hubId: ID!, $itemId: ID!, $cursor: String!) {
			item(hubId: $hubId, itemId: $itemId) {
				whereUsed(pagination: { cursor: $cursor, limit: %[1]d }) `+body, refsPageSize, pageSize, refsPageSize)

	type itemRef struct {
		Typename     string `json:"__typename"`
		ID           string `json:"id"`
		Name         string `json:"name"`
		FusionWebURL string `json:"fusionWebUrl"`
		TipRootModel *struct {
			ID        string     `json:"id"`
			Material  *propValue `json:"materialName"`
			Component *struct {
				PartNumber  *propValue `json:"partNumber"`
				Description *propValue `json:"description"`
			} `json:"component"`
		} `json:"tipRootModel"`
		// The row's own parents; only requested on DesignItem.
		WhereUsed *struct {
			Pagination struct {
				Cursor string `json:"cursor"`
			} `json:"pagination"`
			Results []struct {
				Typename string `json:"__typename"`
			} `json:"results"`
		} `json:"whereUsed"`
	}

	// ownDrawings is this design's drawings, from the first page only;
	// ownDrawingsComplete says whether that list is the whole set.
	ownDrawings := map[string]bool{}
	ownDrawingsComplete := false

	vars := map[string]any{"hubId": hubID, "itemId": itemID}
	all, err := allPages(ctx, token, qFirst, qNext, vars, func(data json.RawMessage) (string, []itemRef, error) {
		var r struct {
			Item struct {
				DrawingItems *struct {
					Pagination struct {
						Cursor string `json:"cursor"`
					} `json:"pagination"`
					Results []struct {
						ID string `json:"id"`
					} `json:"results"`
				} `json:"drawingItems"`
				WhereUsed struct {
					Pagination struct {
						Cursor string `json:"cursor"`
					} `json:"pagination"`
					Results []itemRef `json:"results"`
				} `json:"whereUsed"`
			} `json:"item"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return "", nil, fmt.Errorf("whereUsed: %w", err)
		}
		if d := r.Item.DrawingItems; d != nil {
			for _, dr := range d.Results {
				ownDrawings[dr.ID] = true
			}
			ownDrawingsComplete = d.Pagination.Cursor == ""
		}
		return r.Item.WhereUsed.Pagination.Cursor, r.Item.WhereUsed.Results, nil
	})
	if err != nil {
		return nil, err
	}

	// The API returns each item once, but a page boundary is not a
	// uniqueness boundary — dedupe by id so a repeat can't double a row.
	seen := make(map[string]bool, len(all))
	refs := make([]ComponentRef, 0, len(all))
	for _, it := range all {
		if it.ID != "" {
			if seen[it.ID] {
				continue
			}
			seen[it.ID] = true
		}
		ref := ComponentRef{
			ID:             it.ID,
			Name:           it.Name,
			DesignItemID:   it.ID,
			DesignItemName: it.Name,
			FusionWebURL:   it.FusionWebURL,
			Kind:           navItemFromTypename(it.ID, it.Name, it.Typename).Kind,
		}
		if m := it.TipRootModel; m != nil {
			ref.Material = m.Material.String()
			if m.Component != nil {
				ref.PartNumber = m.Component.PartNumber.String()
				ref.PartDesc = m.Component.Description.String()
			}
			ref.RootModelID = m.ID
		}
		// A design is a chain root when its own where-used holds no
		// design. Drawings do not count as parents. A truncated nested
		// page proves nothing, so it leaves the flag unset.
		if w := it.WhereUsed; w != nil && w.Pagination.Cursor == "" {
			ref.Root = true
			for _, p := range w.Results {
				if p.Typename != "DrawingItem" {
					ref.Root = false
					break
				}
			}
		}
		// A drawing is direct when it is one of this design's drawings.
		// With the own-drawings list cut short, absence proves nothing.
		if ref.Kind == "drawing" {
			switch {
			case ownDrawings[it.ID]:
				ref.Relation = RelationDirect
			case ownDrawingsComplete:
				ref.Relation = RelationIndirect
			}
		}
		refs = append(refs, ref)
	}
	sortWhereUsed(refs)
	return refs, nil
}

// sortWhereUsed orders rows direct first, undetermined next, indirect
// last, keeping the API's order within each group.
func sortWhereUsed(refs []ComponentRef) {
	rank := map[string]int{RelationDirect: 0, "": 1, RelationIndirect: 2}
	sort.SliceStable(refs, func(i, j int) bool {
		return rank[refs[i].Relation] < rank[refs[j].Relation]
	})
}

// NeedsWhereUsedRelations reports whether any row from GetWhereUsed is
// still waiting for GetWhereUsedRelations.
func NeedsWhereUsedRelations(refs []ComponentRef) bool {
	for _, r := range refs {
		if r.Relation == "" && r.RootModelID != "" {
			return true
		}
	}
	return false
}

// GetWhereUsedRelations is the second half of GetWhereUsed: it decides
// direct or indirect for the design rows and returns a new, re-sorted
// list (refs itself is not modified). A row whose check failed keeps an
// empty Relation; there is no error to return because the rows are
// already usable without it. A row whose design references nothing in
// its current version (see relationNone) also stays untagged, and loses
// Root.
func GetWhereUsedRelations(ctx context.Context, token, itemID string, refs []ComponentRef) []ComponentRef {
	out := append([]ComponentRef(nil), refs...)
	models := make(map[int]string)
	for i, r := range out {
		if r.Relation == "" && r.RootModelID != "" {
			models[i] = r.RootModelID
		}
	}
	for i, rel := range directRelations(ctx, token, itemID, models) {
		if rel == relationNone {
			// Not a current user of this item, so not the top of its
			// parent chain either.
			out[i].Root = false
			continue
		}
		out[i].Relation = rel
	}
	sortWhereUsed(out)
	return out
}

// relationBatchSize is how many parents one direct/indirect request
// covers. Each parent costs 12 query points, so 25 is 300 of the 1000
// budget, and it matches the where-used page size.
const relationBatchSize = 25

// relationPageSize is the page size for a parent's first-level
// references. 250 is the most the API accepts, costs the same points
// and time as 50, and saves a second round for any parent with more
// than 50 references.
const relationPageSize = 250

// relationCheckConcurrency caps the direct/indirect requests a single
// Where Used load has in flight (only reached past 25 parents, or when
// a batch has to be retried one parent at a time).
const relationCheckConcurrency = 4

// maxRelationPages bounds the walk through one parent's references
// (100 pages is 25000 first-level references) so a cursor that never
// ends cannot hang a tab.
const maxRelationPages = 100

// relationCheck is one parent whose first-level references are being
// searched for the selected item.
type relationCheck struct {
	key     int    // caller's row index
	modelID string // parent's root model
	cursor  string // next page of its relations; empty on the first
	seen    int    // references read so far, across pages
	rel     string // RelationDirect / RelationIndirect / relationNone once decided
}

// relationNone is the internal verdict for a parent whose current
// version has no references at all. whereUsed lists such items (on live
// data: documents that used the design in an older version, and hidden
// wrapper documents), but a design that references nothing cannot hold
// this one through a sub-assembly either, so calling it indirect would
// be wrong. Its row is left untagged.
const relationNone = "none"

// directRelations decides, for each parent root model, whether that
// design references itemID at its own first level. The result maps the
// caller's key to RelationDirect or RelationIndirect; a parent whose
// check failed is absent, and one whose current version references
// nothing at all maps to relationNone.
//
// Indirect therefore means: the API lists the design as a user of this
// item, its current version has references, and this item is not at
// their first level. The path through the sub-assembly is not walked.
//
// All parents go into one request, each under its own alias, because a
// request costs over a second however little it asks: seven parents
// take about as long batched as one does alone. Parents with more
// first-level references than a page holds, and no match yet, go round
// again with their cursors.
//
// The scope argument is what makes this "direct" in the Fusion sense:
// it returns the design's internal components plus the external designs
// inserted into them, and stops there instead of descending into those
// designs' own children.
func directRelations(ctx context.Context, token, itemID string, models map[int]string) map[int]string {
	out := make(map[int]string, len(models))
	pending := make([]*relationCheck, 0, len(models))
	for key, modelID := range models {
		pending = append(pending, &relationCheck{key: key, modelID: modelID})
	}
	// Map order is random; keep the batches reproducible.
	sort.Slice(pending, func(i, j int) bool { return pending[i].key < pending[j].key })

	for page := 0; len(pending) > 0 && page < maxRelationPages; page++ {
		var wg sync.WaitGroup
		sem := make(chan struct{}, relationCheckConcurrency)
		run := func(batch []*relationCheck) error {
			sem <- struct{}{}
			defer func() { <-sem }()
			return relationBatch(ctx, token, itemID, batch)
		}
		failed := make([]bool, len(pending))
		for start := 0; start < len(pending); start += relationBatchSize {
			end := min(start+relationBatchSize, len(pending))
			wg.Add(1)
			go func() {
				defer wg.Done()
				if run(pending[start:end]) == nil {
					return
				}
				// One unreadable parent spoils the whole request. Ask
				// again one at a time so the others still get an answer.
				var inner sync.WaitGroup
				for i := start; i < end; i++ {
					if end-start == 1 {
						failed[i] = true
						break
					}
					inner.Add(1)
					go func() {
						defer inner.Done()
						failed[i] = run(pending[i:i+1]) != nil
					}()
				}
				inner.Wait()
			}()
		}
		wg.Wait()

		var next []*relationCheck
		for i, c := range pending {
			switch {
			case failed[i]:
			case c.rel != "":
				out[c.key] = c.rel
			default:
				next = append(next, c) // has a cursor, no match yet
			}
		}
		pending = next
	}
	return out
}

// relationBatch runs one request covering every check in batch and
// advances each: rel is set once the item is found (direct) or the
// parent's references run out (indirect); otherwise cursor moves on.
// On error no check is modified.
func relationBatch(ctx context.Context, token, itemID string, batch []*relationCheck) error {
	var decl, sel []string
	vars := make(map[string]any, 2*len(batch))
	for i, c := range batch {
		decl = append(decl, fmt.Sprintf("$m%d: ID!", i))
		vars[fmt.Sprintf("m%d", i)] = c.modelID
		paging := fmt.Sprintf("limit: %d", relationPageSize)
		if c.cursor != "" {
			decl = append(decl, fmt.Sprintf("$c%d: String!", i))
			vars[fmt.Sprintf("c%d", i)] = c.cursor
			paging = fmt.Sprintf("cursor: $c%d, %s", i, paging)
		}
		sel = append(sel, fmt.Sprintf(`
			p%d: model(modelId: $m%d) {
				assemblyRelations(scope: INTERNAL_PLUS_DIRECT_EXTERNAL, pagination: { %s }) {
					pagination { cursor }
					results { toModel { designItem { id } } }
				}
			}`, i, i, paging))
	}
	q := fmt.Sprintf("\n\t\tquery WhereUsedIsDirect(%s) {%s\n\t\t}", strings.Join(decl, ", "), strings.Join(sel, ""))

	data, err := gqlQuery(ctx, token, q, vars)
	if err != nil {
		return fmt.Errorf("where-used relation: %w", err)
	}
	var r map[string]*struct {
		AssemblyRelations *struct {
			Pagination struct {
				Cursor string `json:"cursor"`
			} `json:"pagination"`
			Results []struct {
				ToModel *struct {
					DesignItem *struct {
						ID string `json:"id"`
					} `json:"designItem"`
				} `json:"toModel"`
			} `json:"results"`
		} `json:"assemblyRelations"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("where-used relation decode: %w", err)
	}
	// Validate before touching any check, so an error leaves them as-is.
	for i := range batch {
		if m := r[fmt.Sprintf("p%d", i)]; m == nil || m.AssemblyRelations == nil {
			return fmt.Errorf("where-used relation: no relations returned for parent %d", i)
		}
	}
	for i, c := range batch {
		ar := r[fmt.Sprintf("p%d", i)].AssemblyRelations
		c.cursor = ar.Pagination.Cursor
		c.seen += len(ar.Results)
		if c.cursor == "" {
			c.rel = RelationIndirect
			if c.seen == 0 {
				c.rel = relationNone
			}
		}
		for _, rel := range ar.Results {
			if rel.ToModel != nil && rel.ToModel.DesignItem != nil && rel.ToModel.DesignItem.ID == itemID {
				c.rel = RelationDirect
				break
			}
		}
	}
	return nil
}

// GetDrawingsForDesign returns the drawing items that reference the
// given design item, most recently modified first.
//
// DesignItem.drawingItems answers this directly. The v2 implementation
// had to walk every saved version of the design and collect each
// version's drawings, because a v2 drawing pointed at one specific
// component version and went missing from the tip as soon as the design
// was saved again.
func GetDrawingsForDesign(ctx context.Context, token, hubID, designItemID string) ([]DrawingRef, error) {
	const body = `{
						pagination { cursor }
						results {
							id name fusionWebUrl
							lastModifiedOn
							lastModifiedBy { firstName lastName }
						}
					}
				}
			}
		}`
	qFirst := fmt.Sprintf(`
		query GetDrawingsForDesign($hubId: ID!, $itemId: ID!) {
			item(hubId: $hubId, itemId: $itemId) {
				... on DesignItem {
					drawingItems(pagination: { limit: %d }) `, firstPageSize) + body
	qNext := fmt.Sprintf(`
		query GetDrawingsForDesignNext($hubId: ID!, $itemId: ID!, $cursor: String!) {
			item(hubId: $hubId, itemId: $itemId) {
				... on DesignItem {
					drawingItems(pagination: { cursor: $cursor, limit: %d }) `, pageSize) + body

	type drawingResult struct {
		ID           string  `json:"id"`
		Name         string  `json:"name"`
		FusionWebURL string  `json:"fusionWebUrl"`
		ModifiedOn   string  `json:"lastModifiedOn"`
		ModifiedBy   apiUser `json:"lastModifiedBy"`
	}

	vars := map[string]any{"hubId": hubID, "itemId": designItemID}
	all, err := allPages(ctx, token, qFirst, qNext, vars, func(data json.RawMessage) (string, []drawingResult, error) {
		var r struct {
			Item struct {
				DrawingItems struct {
					Pagination struct {
						Cursor string `json:"cursor"`
					} `json:"pagination"`
					Results []drawingResult `json:"results"`
				} `json:"drawingItems"`
			} `json:"item"`
		}
		if err := json.Unmarshal(data, &r); err != nil {
			return "", nil, fmt.Errorf("drawings for design decode: %w", err)
		}
		return r.Item.DrawingItems.Pagination.Cursor, r.Item.DrawingItems.Results, nil
	})
	if err != nil {
		return nil, fmt.Errorf("drawings for design: %w", err)
	}

	seen := make(map[string]bool, len(all))
	refs := make([]DrawingRef, 0, len(all))
	for _, d := range all {
		if d.ID == "" || seen[d.ID] {
			continue
		}
		seen[d.ID] = true
		refs = append(refs, DrawingRef{
			Name:          d.Name,
			DrawingItemID: d.ID,
			ModifiedOn:    parseTime(d.ModifiedOn),
			ModifiedBy:    d.ModifiedBy.fullName(),
			FusionWebURL:  d.FusionWebURL,
		})
	}
	sort.SliceStable(refs, func(i, j int) bool {
		return refs[i].ModifiedOn.After(refs[j].ModifiedOn)
	})
	return refs, nil
}
