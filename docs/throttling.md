# Throttling, query points and batching

How the interactive browser spends the Manufacturing Data Model API's query-point quota, what goes wrong when it runs out, and what the client does about it. This is the result of a top-to-bottom review of every request the browser makes, measured against a live hub. The hub audit (`-mfgdm-report`) has its own pacing; see [mfgdm-report.md](mfgdm-report.md).

## How the API meters requests

Every query has a point value, returned in `extensions.pointValue.requestedQueryPointValue`.

- **Per query: 1000 points.** Over that, the query is refused.
- **Per minute, per user and application.** On the test account the quota behaved like 10,000 points a minute. A request that would overdraw it is refused whole with **HTTP 429** and `Query point value per minute quota exceeded with point value of N and remaining quota M`. There is no `Retry-After` header. The quota frees up gradually: in testing a refused request went through about 25 seconds later.
- **Points are charged for what is asked, not what comes back.** A list costs a base of about 11 points, plus a per-row cost for each costly field *times the page size requested*. An empty folder asked for 50 rows costs the same as a full one.

Measured per-row costs (points per row asked for):

| Field on a list row | Cost per row |
|---|---:|
| `__typename`, `id`, `name`, `extensionType` | 0 |
| `fusionWebUrl` | 5 |
| `tipRootModel { id }` | 5 |
| `alternativeIdentifiers { … }` | 1–5 |
| `tipRootModel { … assemblyRelations(depth: 1, limit: 1) }` | 11 (5 + 6) |
| `tipRootModel { id materialName component { … } }` | 14 |
| nested `whereUsed { results { __typename } }` | 11 |
| a project row itself (`hub.projects`) | 5 |

Aliasing several root fields into one request saves round-trips but not points: 50 aliased `model(…)` probes cost exactly 50 × 6.

## What the review found

### 1. Every folder cost 261 points to open, even an empty one

`GetItems` and `GetProjectItems` asked for `tipRootModel { id }` on a page of 50, so that the assembly/part classifier had a model id to probe. That is 11 + 5 × 50 = **261 points per page whether the folder holds 400 designs or none**, followed by one 6-point request per design. Browsing through a tree of small folders — a fastener library, say — spent 261 points a folder, and about 38 folders a minute is the whole quota. A folder of 377 designs cost about 4,350 points and 385 requests.

### 2. A throttled request was retried for two seconds and then reported as "flaky"

`gqlQuery` treated HTTP 429 like gateway flakiness: three attempts, 0 → 500 ms → 1.5 s apart. A quota does not recover in two seconds, so every throttled request failed with `APS GraphQL flaky after 3 attempts: HTTP 429 …`, which the UI turns into the full-screen error. Each retry was also a further request into the limit.

### 3. The flakiness retry was hiding a deterministic error

The retry rule was "an error with `errorType: UNKNOWN` at path depth ≤ 2 is gateway flakiness". But `errorType: UNKNOWN` is also how the API labels failures that are facts about the data. A document in a closed project fails on `fusionWebUrl` with

```json
{"message": "Sorry, the project you are trying to access is now closed by the administrator.",
 "path": ["item", "fusionWebUrl"],
 "extensions": {"code": "EXCEPTION_CLOSED_COMMUNITY", "errorType": "UNKNOWN"}}
```

and returns every other field. The rule saw depth 2 and `UNKNOWN`, retried three times (3 × 88 points, 6.5 s), got the identical answer each time, and reported `flaky after 3 attempts` — so the details pane of such a document could never be shown. This reproduced on every attempt against a library part.

No instance of the original v2 flakiness (`NOT_FOUND`/`UNKNOWN` on a valid hub) was seen on v3 in this review or in the two full hub audits (about 900 requests). That is not proof it is gone, so the retry stays — narrowed to what it was written for.

### 4. Sub-folders were never listed

Opening a folder called only `itemsByFolder`, which returns documents. A folder holding only sub-folders looked empty. `foldersByFolderInHub` is now asked alongside.

### 5. Fields nobody reads

`GetHubs` asked for `fusionWebUrl` and `alternativeIdentifiers` (511 points for a page of 50 hubs); `GetProjects` asked for `fusionWebUrl` (250 of its 566 points per page). `NavItem.WebURL` is not read anywhere, and neither is the hub's alternate id.

### 6. Lists behind the tabs asked for 50 rows that are rarely there

Where Used cost **777 points** and Drawings **316** on a design used nowhere and drawn nowhere. The version history asked for 50 entries on every details load.

### 7. Details were fetched for every row the cursor passed

Holding the down arrow bought a details query — and, with a tab other than Details active, that tab's query too — for every row on the way. A late answer could also replace the details of the row the cursor had since moved to.

### 8. Show in Location walked one request per folder level

`GetItemLocation` asked for each ancestor folder in turn.

## What changed

| Operation | Before | After |
|---|---:|---:|
| Open a folder (any size up to 200 documents) | 261 per 50 rows | 22 (documents + sub-folders) |
| Open a 377-design folder, list only | 2,088 points, 8 requests (about 2.6 s each, one after another) | 33 points, 3 requests, 6 s measured |
| Assembly/part tags | 6 per design, one request each, all designs | 11 per row, one request per 50, only near the cursor |
| Hub list | 511 | 11 |
| Project list (70 projects) | 1,132 | 632 |
| Details | 88 | 48 |
| Where Used (first page) | 777 | 327 |
| Drawings (first page) | 316 | 76 |
| Show in Location (3 folders deep) | 47 points, 4 requests | 19 points, 1 request |

- **Lean lists.** `GetItems` / `GetProjectItems` ask for `__typename id name` in pages of 200. A page costs 11 points at any size, and a long folder needs a quarter of the round-trips.
- **Classification by page, near the cursor.** `api.ClassifyContents` re-lists the container with the model id and a one-row `assemblyRelations` probe nested in each design, sized to the documents it covers, at most 50 a request. The UI asks for the page under the cursor and one ahead; further pages are fetched as the cursor approaches them. A folder with no designs is never asked. Tags are cached for the session, so returning to a folder costs nothing. The request is *background* work (below).
- **Small first pages.** Where Used, Drawings and the version history ask for 10 rows first and page at full size only when there are more.
- **Throttling is waited out, not retried.** On HTTP 429 the client sets a pause shared by every request (5 s, then 10, 15, 20 for consecutive refusals; `Retry-After` wins if the API ever sends one), then sends the request again, for as long as the caller's context allows. A refusal does not count as an attempt. The header shows `API rate limit reached, resuming in Ns` meanwhile. If the context ends first the error is `api.ErrThrottled`, named as a rate limit.
- **Every request is priced before it is sent** (`api/cost.go`). A query's cost is fixed by its text, so the client knows it in advance: from a table of measured costs by operation name the first time, and from the API's own number for that exact text from then on. The client meters the points it spent in the last 60 seconds, counting requests still in flight at their price. A request that would take the minute past 9,000 points (`api.PointBudget`) waits until enough spending has aged out, instead of being sent into a 429; the header shows the same `resuming in Ns` notice.
- **Background budget.** Requests marked with `api.Background(ctx)` — the classification pages — are held to 5,000 points a minute, leaving the rest of the quota for navigation and for other sessions on the same account.
- **Listings are kept for two minutes** (`ui/listings.go`). Going back to a parent folder, or into a folder just left, re-sent both of its requests. A project list or a container's contents are now served from memory for two minutes; `[r]` always asks again. Details and the tabs were already cached for the session.
- **One unreadable Where Used parent no longer multiplies requests.** A document with damaged data answers `DATA_HAS_ERROR` (labelled `UNKNOWN`) on its `assemblyRelations`, beside the other parents' answers. The batch was retried three times as flaky and then asked again one parent at a time: up to 28 requests for a batch of 25. `DATA_HAS_ERROR` is now a deterministic code, the batch is one request, and that parent alone stays untagged. A batch is asked again per parent only when it was refused outright; one that timed out or kept failing transiently is not.
- **Retry rule narrowed.** A transient is an `UNKNOWN` error at or near the root whose code is not one that describes the data (`EXCEPTION_CLOSED_COMMUNITY`, `DATA_HAS_ERROR`, `FORBIDDEN`, `UNAUTHORIZED`, `BAD_REQUEST`, `BAD_USER_INPUT`, `GRAPHQL_VALIDATION_FAILED`). An error that leaves the root field intact passes the data through; one that nulls the root fails at once. Error text now carries the code and the path, so the next mislabelled error is recognisable on sight.
- **Details wait for the cursor to rest** (150 ms), and an answer for a row the cursor has left is cached but not shown.
- **Locate asks for six ancestors in one query**, and continues from the last only for deeper trees.

## What was looked at and left alone

- **Combining the folder and document lists into one request.** It would save a round-trip and no points; the two already run concurrently.
- **Aliasing unrelated requests together.** No point saving, and one failure would take its neighbours with it.
- **The project list's remaining 316 points a page.** A project row costs 5 points by itself, and `alternativeIdentifiers` is needed for Open in Fusion.
- **Where Used at 327 points.** What remains is the per-row model properties and the nested `whereUsed` that finds root parents; both are shown.
- **Uses** was already cheap (27 points for 50 rows).

## Limits of this review

- The quota figure (about 10,000 a minute) is inferred from one account. The 9,000-point budget rests on it: on an account with a smaller quota the 429 pause still does the work, and on one with a larger quota the client holds back earlier than it must.
- The meter counts this process only. Another session on the same account spends the same quota, which is why a 429 can arrive while the meter is low; the shared pause handles that case.
- A query not in the price table is taken as 100 points until the API has answered it once.
- The deterministic-code list holds the codes seen so far. An unlisted deterministic code at depth ≤ 2 is still retried three times before failing, now with its code and path in the message.
