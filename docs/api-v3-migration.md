# Manufacturing Data Model API: v2 → v3 migration

**Status: migrated. No blockers.** As of 2026-11-11 every query the app makes runs against the v3 endpoint, and the v2 endpoint is no longer called anywhere.

The two gaps that previously kept the app on v2 — where-used and the design ↔ drawing relationships — are present in the v3 schema and return correct data:

| Was missing in v3 | Now |
|---|---|
| Where used | `Item.whereUsed` |
| Drawings of a design | `DesignItem.drawingItems` (also `Model.drawings`) |
| Source design of a drawing | `DrawingItem.tipDrawing.model` (also `DrawingItem.designItem`, `Drawing.model`) |

Nothing below is a blocker. The [behaviour changes](#behaviour-changes-you-will-notice) are differences a user can see, and the [unverified](#unverified) list is what could not be tested with the hubs and data available.

## How this was verified

Against the live v3 gateway, with a real login, on 2026-11-11:

- **Schema** — introspected directly (`go run ./cmd/probe-gql -v3 -roots` / `-type <Name>`). Introspection is open on v3. The public docs page renders client-side and could not be read by a script, so the live schema is the source for everything here.
- **Every `api` function** — run end to end against a real hub after the migration: hubs, projects, folders, project items (170 items, 4 pages), folder items, details + history, uses, where used, drawings, drawing source, locate (project root and nested folder), classify (assembly and part), and a STEP export downloaded to disk and opened as valid ISO-10303-21.
- **v2 parity** — the same items were queried on v2 and compared: direct children (41 = 41 on the largest test assembly), version numbers (15 of 15 designs match), drawings of a design, and where-used.
- **Hub coverage** — all 13 hubs the account can see list projects, folders and items on v3.
- **Unit tests** — `go test ./...` passes; the `api` fixtures were rewritten to the v3 response shapes.

## What changed in the code

Endpoint: `https://developer.api.autodesk.com/mfg/graphql` → `https://developer.api.autodesk.com/mfg/v3/graphql/public`. The OAuth scope (`data:read`) is unchanged.

v3 removes `ComponentVersion`, `DrawingVersion`, and the item-version objects. A design's geometry is a `Model`; its part number and description live on the model's `Component`; history replaces versions.

| Function | v2 | v3 |
|---|---|---|
| `GetHubs`, `GetFolders`, `GetItemLocation` | — | unchanged |
| `GetProjects` | `projectType` requested | dropped (deprecated — every project is `FOLDER_LEVEL`) |
| `GetItems`, `GetProjectItems` | `tipRootComponentVersion { id }` | `tipRootModel { id }` |
| `ClassifyAssembly` (since replaced by `ClassifyContents`, see [throttling.md](throttling.md)) | `componentVersion.occurrences(limit: 1)` | `model.assemblyRelations(depth: 1, limit: 1)` |
| `GetOccurrences` (Uses) | `componentVersion.occurrences` | `model.assemblyRelations(depth: 1)` → `toModel` |
| `GetWhereUsed` | `componentVersion.whereUsed`, deduped per parent design | `item.whereUsed`; signature now takes `hubID, itemID` |
| `GetDrawingsForDesign` | walk `versions` → `drawingItemVersions`, dedupe | `DesignItem.drawingItems` |
| `GetDrawingSource` | `tipDrawingVersion.componentVersion` | `tipDrawing.model` |
| `GetItemDetails` | `tipVersion`, `tipRootComponentVersion`, `itemVersions` | `tipRootModel` + `component`, per-type `history` |
| `RequestSTEPDerivative` | `componentVersion.derivatives(outputFormat: STEP)` | `model.derivatives(targetFormat: [STEP])` |

Renames that follow from this: `NavItem.ComponentVersionID` and `ItemDetails.RootComponentVersionID` are both now `RootModelID`. `ComponentRef` gained a `Kind` field.

`cmd/probe-assembly` (a v2-only diagnostic whose header said to delete it once the schema decision landed) is replaced by `cmd/probe-gql`, which sends any query or introspection to either endpoint.

Things in v3 that are easy to get wrong, all handled in `api/` and pinned by tests:

- **`name`, `partNumber`, `description`, `materialName` are `Property` objects** on `Model` and `Component`; read `.value`. `Item.name` is still a string.
- **`assemblyRelations` defaults to the whole tree.** Without `depth: 1` the test assembly returned 413 relations instead of its 41 direct children.
- **`pagination.limit` is a hint on `assemblyRelations`.** `limit: 1` returned 2 rows; `limit: 50` returned 61. Follow the cursor; never assume `len(results) <= limit`.
- **History filters are per item type.** Asking a `DesignItem` for `DRAWING_WRITTEN` is a `BAD_REQUEST` that nulls the whole item.
- **Introspection requests drop variables.** `__type(name: $n)` fails with "missing value for non-null variable"; inline the name.
- **`Component.hasChildren` is not a part/assembly signal.** It is `true` for a single-body screw with no assembly or BOM relations, so classification still needs the per-design relation probe.
- **Derivative statuses grew.** `NOT_GENERATED` and `IN_PROGRESS` now precede `SUCCESS`; `TIMEOUT` is a new terminal state and is treated as a failure.

## Behaviour changes you will notice

1. **Where Used lists more.** v2 returned only the direct parent design. v3 also returns assemblies that contain the design indirectly, through a sub-assembly, and drawings of those assemblies. Example: for a shock-shaft sub-assembly, v2 listed `Front Shock`; v3 lists `Front Shock`, `Front Suspension ASSY`, `_4WD Buggy`, and `_4WD Buggy Drawing`. Drawing rows are tagged `drawing`. The schema text says `whereUsed` returns "the immediate parents", which is not what the gateway does, so this may change on Autodesk's side. Nothing in the response marks a row as direct or indirect, so the app works it out and tags rows `direct` or `indirect`, direct first. The rows are shown as soon as they load and the design tags follow from one extra request covering all rows; drawing tags come from the design's own drawings list, so they always agree with the Drawings tab. Parents whose current version references nothing (older-version or hidden documents the API still lists) are left untagged. A design that no other design uses is also tagged `root`: the top of the parent chain (`_4WD Buggy` in the example). See `GetWhereUsed` in [api.md](api.md).
2. **Version comments are real.** The Versions list now shows the history description ("User Saved", "Auto save in Fusion: …", "Imported from …"). On v2 that line only repeated the item name.
3. **Version numbers are derived.** v3 has no version number. The app counts the item's "written" history entries, oldest = v1. This matched v2 on every design compared, but it is an inference, not a field. Version timestamps are the history timestamps, which differ from v2's `createdOn` by up to a couple of minutes.
4. **Item lists are slower.** A page of 50 items with `tipRootModel { id }` takes about 2.6 s on v3 against 1.9 s on v2 (three runs each, same project). Without the model id the two are equal at about 1.1 s, so the cost is resolving the root model. The 170-item test project loads in about 11 s.
5. **Drawings tab is one request.** It no longer pages through the design's saved versions, and it no longer truncates at five drawings per design version.

## Unverified

These could not be tested with the hubs and data on hand. None failed; they are simply unproven.

- **Hubs that are not on the new data model.** Autodesk's v3 announcement says v3 requires a hub type that supports Collaborative Editing, with existing hubs migrating later. All 13 hubs visible to the test account answered v3 and report `hubDataVersion: "2.0.0"`, so no un-migrated hub was available to see what v3 does for one. The app has no v2 fallback. If a user reports an empty or erroring hub, check `hubDataVersion` with `cmd/probe-gql` first.
- **Non-US regions.** The client still sends `X-Ads-Region` for EMEA / AUS. Only US hubs were available, so the header is untested on v3.
- **Milestone flag.** `isMilestone` is gone. `IsMilestone` is now "the newest history entry is a `VERSION_CREATED`". None of the test designs has a milestone, so the star in the details pane is untested against real data. `Model.version` / `Model.latestVersion` are the other candidates if this proves wrong.
- **Drawings made against an older design version.** The v2 version-walk existed because such drawings vanished from the tip. `DesignItem.drawingItems` is documented as "all DrawingItems that reference this DesignItem" and returned the same drawing as v2 on the one design with a drawing, but that drawing was made against the tip version.
- **Configured designs and electronics items.** One configured design appeared in a list and decoded; its details and history were not exercised. No electronics item was available.
- **Gateway flakiness.** The 3-attempt retry on root-level `errorType: UNKNOWN` was written for v2 and is kept unchanged. No such failure was seen in the v3 requests made during this work, which is too small a sample to say it is gone.
- **Usage metering.** Autodesk began metering the Manufacturing Data Model API on 2026-08-17 (free tier, then a paid APS offering or a qualifying subscription), with usage counted per returned component. How v3's model/component responses count against that was not assessed. See [Data Model APIs included in subscriptions](https://aps.autodesk.com/blog/data-model-apis-included-subscriptions-plus-flexible-ways-scale).

## Also fixed along the way

- `TestStepDownloadPath_Sanitizes` failed on macOS before this work: it compared against `os.TempDir() + "/"`, and `$TMPDIR` already ends in a slash there. The test now cleans the path.
- The details pane iterated the version list backwards while the API layer documented it as newest-first; both now agree on newest-first.

## References

- [v3 migration guide](https://aps.autodesk.com/en/docs/mfgdataapi/v3/developers_guide/migration)
- [Manufacturing Data Model API v3 announcement](https://aps.autodesk.com/blog/manufacturing-data-model-api-v3)
- [v3 developer's guide](https://aps.autodesk.com/en/docs/mfgdataapi/v3/developers_guide/overview/)
