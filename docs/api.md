# APS Manufacturing Data Model API

FusionDataCLI queries the **APS Manufacturing Data Model GraphQL API v2** to retrieve hub, project, folder, and item data. All requests are authenticated with a Bearer token obtained through the OAuth PKCE flow.

---

## GraphQL Endpoint

```
POST https://developer.api.autodesk.com/mfg/graphql
Content-Type: application/json
Authorization: Bearer <access_token>
X-Ads-Region: <region>          (optional — US default, EMEA, or AUS)
```

The `X-Ads-Region` header is only sent when a non-default region is configured. It is omitted entirely for US hubs.

---

## API Client Design

```mermaid
flowchart TD
    caller["Caller\n(ui package)"] --> fn["GetHubs / GetProjects\n/ GetFolders / GetItems\n/ GetItemDetails"]
    fn --> ap["allPages()\npagination loop"]
    ap --> gql["gqlQuery()\nretry loop\n(0 → 500ms → 1.5s)"]
    gql --> once["gqlQueryOnce()\nsingle HTTP round-trip\n+ JSON decode"]
    once --> http["net/http\nPOST /mfg/graphql"]
    once --> debug["dbgLog()\noptional request/response dump\n(also mirrored to stderr)"]
    ap --> extract["extract() callback\npage-specific JSON unmarshal"]
    fn --> navItem["navItemFromTypename()\ntype → NavItem.Kind"]
```

---

## Pagination Strategy

The APS GraphQL API uses **cursor-based pagination**. A page returns a cursor string; passing that cursor in the next request fetches the following page. An empty cursor means the last page has been reached.

**Critical implementation detail:** The API rejects a first-page request that includes a `cursor` variable set to `null` or `""`. Two separate query strings are used per endpoint — one without a cursor parameter, one with `$cursor: String!`.

```mermaid
sequenceDiagram
    participant App
    participant API as APS GraphQL

    App->>API: queryFirst (no cursor field, limit: 50)
    API-->>App: { results: [...], pagination: { cursor: "abc123" } }

    App->>API: queryNext (cursor: "abc123", limit: 50)
    API-->>App: { results: [...], pagination: { cursor: "def456" } }

    App->>API: queryNext (cursor: "def456", limit: 50)
    API-->>App: { results: [...], pagination: { cursor: "" } }

    Note over App: cursor = "" → stop
```

Pagination limit is set to `50` per page. The APS validator rejects values ≥ 100 outright (`"pagination must be between 0 and 100"`), and even at 99 the GraphQL query-cost cap (1000 points) is exceeded by the field set this app uses — 200 results blew through it. 50 is the original safe value used since v0.1 and is enforced via the `pageSize = 50` constant in `api/queries.go`.

---

## Queries

### GetHubs

Fetches all hubs the authenticated user has access to.

```graphql
# First page
query GetHubs {
    hubs(pagination: { limit: 50 }) {
        pagination { cursor }
        results {
            id
            name
            fusionWebUrl
            alternativeIdentifiers {
                dataManagementAPIHubId
            }
        }
    }
}

# Subsequent pages
query GetHubsNext($cursor: String!) {
    hubs(pagination: { cursor: $cursor, limit: 50 }) {
        pagination { cursor }
        results {
            id
            name
            fusionWebUrl
            alternativeIdentifiers {
                dataManagementAPIHubId
            }
        }
    }
}
```

**Output fields used:**

| Field | Maps to |
|-------|---------|
| `id` | `NavItem.ID` |
| `name` | `NavItem.Name` |
| `fusionWebUrl` | `NavItem.WebURL` |
| `alternativeIdentifiers.dataManagementAPIHubId` | `NavItem.AltID` (used to build browser URLs) |

---

### GetProjects

Fetches projects within a hub. Uses the `hub(hubId)` nested resolver. Inactive projects are filtered client-side.

```graphql
# First page
query GetProjects($hubId: ID!) {
    hub(hubId: $hubId) {
        projects(pagination: { limit: 50 }) {
            pagination { cursor }
            results {
                id
                name
                fusionWebUrl
                projectStatus
                projectType
                alternativeIdentifiers {
                    dataManagementAPIProjectId
                }
            }
        }
    }
}

# Subsequent pages
query GetProjectsNext($hubId: ID!, $cursor: String!) {
    hub(hubId: $hubId) {
        projects(pagination: { cursor: $cursor, limit: 50 }) {
            pagination { cursor }
            results { ... }
        }
    }
}
```

**Filtering:** Projects where `projectStatus` is `"inactive"` (case-insensitive) are excluded from results.

---

### GetFolders

Fetches top-level folders for a project.

```graphql
# First page
query GetFolders($projectId: ID!) {
    foldersByProject(projectId: $projectId, pagination: { limit: 50 }) {
        pagination { cursor }
        results {
            id
            name
        }
    }
}
```

Folders have no `fusionWebUrl` — their URL is constructed from project context when needed.

---

### GetProjectItems

Fetches items (designs, drawings) directly under a project root (not inside a folder).

```graphql
# First page
query GetProjectItems($projectId: ID!) {
    itemsByProject(projectId: $projectId, pagination: { limit: 50 }) {
        pagination { cursor }
        results {
            __typename
            id
            name
        }
    }
}
```

---

### GetItems

Fetches items within a specific folder.

```graphql
# First page
query GetItems($hubId: ID!, $folderId: ID!) {
    itemsByFolder(hubId: $hubId, folderId: $folderId, pagination: { limit: 50 }) {
        pagination { cursor }
        results {
            __typename
            id
            name
        }
    }
}
```

---

### GetItemDetails

Fetches rich metadata for a single item plus its complete version history. This query is not paginated.

```graphql
query GetItemDetails($hubId: ID!, $itemId: ID!) {
    item(hubId: $hubId, itemId: $itemId) {
        __typename
        id
        name
        size
        mimeType
        extensionType
        createdOn
        createdBy  { firstName lastName }
        lastModifiedOn
        lastModifiedBy { firstName lastName }

        ... on DesignItem {
            fusionWebUrl
            tipVersion { versionNumber }
            tipRootComponentVersion {
                partNumber
                partDescription
                materialName
                isMilestone
            }
        }
        ... on DrawingItem {
            fusionWebUrl
            tipVersion { versionNumber }
        }
        ... on ConfiguredDesignItem {
            fusionWebUrl
            tipVersion { versionNumber }
        }
    }

    itemVersions(hubId: $hubId, itemId: $itemId) {
        results {
            versionNumber
            name
            createdOn
            createdBy { firstName lastName }
        }
    }
}
```

`itemVersions.results` are returned oldest-first by the API. The UI reverses the slice to display newest-first.

---

### RequestSTEPDerivative

Asks APS to translate a design's tip root component version into a STEP file and report the signed download URL when ready. Used by the `[d]` key in the UI. Lives in `api/download.go`.

```graphql
query GetGeometry($componentVersionId: ID!) {
    componentVersion(componentVersionId: $componentVersionId) {
        derivatives(derivativeInput: {outputFormat: STEP, generate: true}) {
            expires
            signedUrl
            status
            outputFormat
        }
    }
}
```

The same query both **kicks off** generation (the first call, when no derivative exists yet) and **reports current status** thereafter. APS keeps the worker running between calls, so the client polls until status reaches `SUCCESS` or `FAILED`:

```mermaid
sequenceDiagram
    participant App
    participant API as APS GraphQL
    participant CDN as APS Signed-URL CDN

    App->>API: RequestSTEPDerivative(cvid) — first call
    API-->>App: { status: PENDING, signedUrl: "" }
    Note over App: Tea.Tick 2s, repeat
    App->>API: RequestSTEPDerivative(cvid)
    API-->>App: { status: PENDING, signedUrl: "" }
    App->>API: RequestSTEPDerivative(cvid)
    API-->>App: { status: SUCCESS, signedUrl: "https://…" }
    App->>CDN: GET signedUrl (no Authorization header)
    CDN-->>App: STEP bytes (streamed to ~/Downloads/<name>-<ts>.stp)
```

`RequestSTEPDerivative` returns `(status, signedURL, err)`. Status values are `PENDING`, `SUCCESS`, and `FAILED` (the constants `StepStatusPending`, `StepStatusSuccess`, `StepStatusFailed`). `signedURL` is empty until status reaches `SUCCESS`.

**Restrictions:**

- Only valid on `DesignItem`. Drawings, configured designs, and folders/projects have no `tipRootComponentVersion` and the API returns no derivative. The UI checks `details.Typename == "DesignItem"` and `details.RootComponentVersionID != ""` before issuing the call.
- The signed URL expires (the `expires` field is returned but currently unused — the client downloads immediately after `SUCCESS`).

#### DownloadFile

Streams the signed-URL response to a destination path. Critically, **the user's bearer token is intentionally NOT attached** — APS signed URLs are self-authenticated (the signature is embedded in the URL) and adding a bearer would leak the access token to whatever host the signed URL points at. If a poisoned or MITM'd GraphQL response ever returned a non-Autodesk URL, the blast radius is confined to the (already untrusted) signed URL itself.

```go
func DownloadFile(ctx context.Context, url, destPath string) error
```

The destination directory is created (`0755`) if needed; the file is written via `os.Create`. Non-2xx responses are surfaced with the first 2 KiB of the body for diagnostics.

#### StepDownloadPath

Returns a sensible local path for a STEP file derived from `name`:

- Prefers `~/Downloads/<sanitised-name>-<YYYYMMDD-HHMMSS>.stp`
- Falls back to `os.TempDir()` if `os.UserHomeDir()` fails or returns empty
- Filenames are sanitised to alphanumerics + `- _ . space` (everything else becomes `_`) so the path round-trips cleanly across Linux, macOS, and Windows

The `userHomeDir` and `nowFunc` package vars are swappable by tests for deterministic output (see Testing below).

---

## NavItem Struct

All list queries produce `[]NavItem`. This is the fundamental navigation unit passed between the `api` and `ui` packages.

```go
type NavItem struct {
    ID          string  // GraphQL node ID
    Name        string
    Kind        string  // see table below
    AltID       string  // dataManagementAPIHubId or dataManagementAPIProjectId
    WebURL      string  // fusionWebUrl if available
    IsContainer bool    // true for hub, project, folder
}
```

**Kind mapping from `__typename`:**

| GraphQL `__typename` | `Kind` | `IsContainer` |
|---|---|---|
| (hub — set explicitly) | `"hub"` | `true` |
| (project — set explicitly) | `"project"` | `true` |
| (folder — set explicitly) | `"folder"` | `true` |
| `DesignItem` | `"design"` | `false` |
| `DrawingItem` | `"drawing"` | `false` |
| `ConfiguredDesignItem` | `"configured"` | `false` |
| anything else | `"unknown"` | `false` |

---

## ItemDetails Struct

Returned by `GetItemDetails`. Contains everything needed for the details panel.

```go
type ItemDetails struct {
    ID            string
    Name          string
    Typename      string        // DesignItem | DrawingItem | ConfiguredDesignItem | BasicItem
    Size          string        // raw bytes as string from API
    MimeType      string
    ExtensionType string
    FusionWebURL  string
    CreatedOn     time.Time
    CreatedBy     string        // "First Last"
    ModifiedOn    time.Time
    ModifiedBy    string
    VersionNumber int           // tipVersion.versionNumber
    // Design-specific
    PartNumber  string
    PartDesc    string
    Material    string
    IsMilestone bool
    // Versions — most recent first
    Versions []VersionSummary
}

type VersionSummary struct {
    Number    int
    CreatedOn time.Time
    CreatedBy string
    Comment   string           // version save comment (may be empty)
}
```

---

## Timestamp Parsing

The API returns timestamps in ISO-8601 format. Two formats are handled:

```go
// Primary: RFC 3339
time.Parse(time.RFC3339, s)           // "2026-03-15T14:30:00Z"

// Fallback: millisecond variant
time.Parse("2006-01-02T15:04:05.000Z", s)   // "2026-03-15T14:30:00.000Z"
```

---

## Error Handling and Retry

GraphQL errors are returned in a top-level `errors` array alongside `data`. Each error carries `extensions.code`, `extensions.errorType`, and `extensions.correlation_id`. The client collects all error messages and joins them with `"; "`:

```json
{
  "errors": [
    {
      "message": "Requested resource not found.",
      "extensions": {
        "code": "NOT_FOUND",
        "errorType": "UNKNOWN",
        "service": "cw",
        "correlation_id": "..."
      }
    }
  ]
}
```

**HTTP-level errors:**
- `401 Unauthorized` → short-circuited before body parsing; surfaced as `"unauthorized (HTTP 401) — token may be expired or lacks scope/entitlement; body: <raw>"`. Bypassing the JSON decode avoids spurious "parsing GraphQL response" errors when APS returns a non-JSON 401 body.
- `408 / 429 / 5xx` → retried (see below).
- Other 4xx → response body parsed and surfaced verbatim, no retry.

### Bounded retry on transient APS gateway flakiness

The MFG GraphQL gateway intermittently returns `code:NOT_FOUND, errorType:UNKNOWN` for hub URNs it just successfully enumerated via the `hubs` query. The same query body, same access token, and same hub URN succeeds and fails within seconds. Repro details and a defect-report template are kept outside the repo at `~/Documents/aps-mfg-graphql-flakiness.md` so anyone can pick it up to file with APS.

`gqlQuery` wraps `gqlQueryOnce` in a 3-attempt retry loop with bounded backoffs `0 → 500 ms → 1.5 s` (max ~2 s added latency, well inside the 30 s nav-cmd context). The retry decision:

```mermaid
flowchart TD
    A[gqlQueryOnce returns] --> B{error?}
    B -- no --> OK([return data])
    B -- yes --> C{transport error<br/>or HTTP 408/429/5xx?}
    C -- yes --> RETRY[retry with backoff]
    C -- no --> D{HTTP 401?}
    D -- yes --> FAIL([surface, no retry])
    D -- no --> E{GraphQL errors[]<br/>contain errorType:UNKNOWN?}
    E -- yes --> RETRY
    E -- no --> FAIL
    RETRY --> F{attempts left?}
    F -- yes --> A
    F -- no --> FINAL([surface 'flaky after N attempts'])
```

Concrete `errorType` values (`VALIDATION`, `BAD_USER_INPUT`, etc.) and HTTP 401 are **never** retried — those are real errors. Only the gateway's `UNKNOWN` marker and transport/server-side faults trigger a retry.

If the call is still failing after 3 attempts, the wrapped error reads `APS GraphQL flaky after 3 attempts: <last error>` so the symptom is distinguishable from a one-shot failure when reading logs.

---

## Debug Mode

Set `APSNAV_DEBUG=1` before running to enable full request/response logging:

```sh
APSNAV_DEBUG=1 fusiondatacli            # in-app overlay only
APSNAV_DEBUG=1 fusiondatacli 2> log     # also captured to file via stderr
```

Logs are stored in memory (rolling buffer, max 500 lines) and displayed via the `?` overlay from `stateBrowsing`. Each entry is **also mirrored to stderr** when debug is enabled, so the log can be captured to a file even when the in-app overlay is unreachable (e.g. from `stateError`). BubbleTea uses the alternate screen buffer, so stderr writes do not smear the TUI.

Each log entry includes:
- Query name and variables
- HTTP status code
- Raw JSON response body
- `RETRY attempt=N delay=… lastErr=…` lines whenever the bounded-retry loop kicks in

Debug logs are **not** written to disk by the app itself, and do not include Authorization header values.

---

## Region Support

APS hubs in EMEA and Australia are served from regional API endpoints. Set the region before running:

```sh
APS_REGION=EMEA fusiondatacli   # Europe, Middle East, Africa
APS_REGION=AUS  fusiondatacli   # Australia
```

Or set `"region": "EMEA"` in `~/.config/fusiondatacli/config.json`.

When a region is set, the `X-Ads-Region` header is added to every GraphQL request.

---

## Testing

The `api` package endpoint is held in a package-level `var` rather than a `const` so tests in any package can swap it for an `httptest.Server` URL:

```go
// api/client.go
var graphqlEndpoint = "https://developer.api.autodesk.com/mfg/graphql"
```

Same-package tests (`api/*_test.go`) can write `graphqlEndpoint` directly. Cross-package tests (notably `ui/` flow tests that drive a `tea.Cmd` which internally calls into `api`) use the exported helper:

```go
// SetGraphqlEndpointForTesting overrides graphqlEndpoint and returns a
// restore func. Production code MUST NOT call this.
func SetGraphqlEndpointForTesting(url string) (restore func())
```

Typical use:

```go
srv := testutil.GraphQLServer(t, func(req testutil.GraphQLRequest) testutil.GraphQLResponse {
    return testutil.GraphQLResponse{ Data: map[string]any{ /* ... */ } }
})
restore := api.SetGraphqlEndpointForTesting(srv.URL)
defer restore()
// drive UI / api code under test...
```

`testutil.GraphQLServer` is in the shared `internal/testutil/` package — see [`docs/architecture.md`](architecture.md) and [`docs/development.md`](development.md) for the full test strategy.

The `download.go` package vars `userHomeDir` and `nowFunc` follow the same pattern: tests overwrite them to redirect downloads into a `t.TempDir()` and produce deterministic timestamps.
