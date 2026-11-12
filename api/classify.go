package api

import (
	"context"
	"encoding/json"
	"fmt"
)

// ClassifyPageSize is the most documents one ClassifyContents call
// covers. At 11 query points a row, a full page is 561 points — under
// the 1000-point cap on a single query with room to spare.
const ClassifyPageSize = 50

// Classification is what ClassifyContents learns about one design.
type Classification struct {
	ItemID      string
	RootModelID string
	IsAssembly  bool
}

// ClassifyContents tells assemblies from parts for one page of a
// folder's (or, with isProject, a project root's) documents, and
// returns each design's root model id along the way.
//
// The document list itself is fetched without this (GetItems,
// GetProjectItems): __typename, id and name cost no query points, while
// the model id costs 5 points per row asked for and the assembly probe
// another 6. Splitting the two lets a folder appear at once for 11
// points and lets the tags be bought only for the rows the user is
// looking at — limit is that number of rows, and cursor continues from
// a previous call's next.
//
// One request covers the page. The earlier implementation sent a
// request per design (ClassifyAssembly, 6 points and one round-trip
// each), which for a folder of 400 designs was 400 requests.
//
// An assembly is a design whose model has at least one direct child:
// assemblyRelations is asked for a single depth-1 result. Component.
// hasChildren looks like the cheaper signal but reports true for
// single-body parts, so the relation probe stays.
//
// Rows come back only for designs that have a model. The request runs
// as background work (see Background): the tags are decoration, and
// they wait when the minute's points are mostly spent.
func ClassifyContents(ctx context.Context, token, hubID, containerID string, isProject bool, limit int, cursor string) (rows []Classification, next string, err error) {
	if containerID == "" {
		return nil, "", fmt.Errorf("classify: empty container id")
	}
	limit = max(1, min(limit, ClassifyPageSize))

	decl, field := "$hubId: ID!, $id: ID!", "itemsByFolder(hubId: $hubId, folderId: $id"
	vars := map[string]any{"hubId": hubID, "id": containerID}
	if isProject {
		decl, field = "$id: ID!", "itemsByProject(projectId: $id"
		delete(vars, "hubId")
	}
	paging := fmt.Sprintf("limit: %d", limit)
	if cursor != "" {
		decl += ", $cursor: String!"
		paging = "cursor: $cursor, " + paging
		vars["cursor"] = cursor
	}
	q := fmt.Sprintf(`
		query ClassifyContents(%s) {
			list: %s, pagination: { %s }) {
				pagination { cursor }
				results {
					__typename id
					... on DesignItem {
						tipRootModel {
							id
							assemblyRelations(depth: 1, pagination: { limit: 1 }) { results { id } }
						}
					}
				}
			}
		}`, decl, field, paging)

	data, err := gqlQuery(Background(ctx), token, q, vars)
	if err != nil {
		return nil, "", err
	}
	var r struct {
		List struct {
			Pagination struct {
				Cursor string `json:"cursor"`
			} `json:"pagination"`
			Results []*struct {
				Typename string `json:"__typename"`
				ID       string `json:"id"`
				Model    *struct {
					ID        string `json:"id"`
					Relations *struct {
						Results []struct {
							ID string `json:"id"`
						} `json:"results"`
					} `json:"assemblyRelations"`
				} `json:"tipRootModel"`
			} `json:"results"`
		} `json:"list"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, "", fmt.Errorf("classify decode: %w", err)
	}
	for _, it := range r.List.Results {
		// A design with no model has nothing to classify, and a model
		// whose relations could not be read is left untagged rather
		// than called a part.
		if it == nil || it.Typename != "DesignItem" || it.Model == nil || it.Model.Relations == nil {
			continue
		}
		rows = append(rows, Classification{
			ItemID:      it.ID,
			RootModelID: it.Model.ID,
			IsAssembly:  len(it.Model.Relations.Results) > 0,
		})
	}
	return rows, r.List.Pagination.Cursor, nil
}
