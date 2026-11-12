# MFGDM data report

`fusiondatacli -mfgdm-report` walks every project, folder and document of a hub through the Manufacturing Data Model (MFGDM) API and writes a Markdown report of what is missing MFGDM data, with ids and summary statistics. It does not open the interactive browser.

```sh
fusiondatacli -mfgdm-report -hub "My Hub"
```

Progress goes to stderr; the report is written to the current directory as `mfgdm-report-<hub>-<timestamp>.md`.

## Flags

| Flag | Default | Meaning |
|---|---|---|
| `-mfgdm-report` | | Run the report instead of the browser. |
| `-hub` | | Hub name, a unique part of the name, or the hub id. Required when the account sees more than one hub; run without it to list them. |
| `-project` | every project | Only these projects: comma-separated names or ids. |
| `-out` | `mfgdm-report-<hub>-<timestamp>.md` | Report file. |
| `-points-per-minute` | `6500` | API query points to spend per minute. Lowered automatically if the API throttles. |
| `-concurrency` | `4` | Most requests in flight at once (1–16). Lowered automatically if the API throttles. |
| `-include-inactive` | off | Also walk projects that are not active. The API usually refuses them, which is then reported as unread. |
| `-debug` | off | Log every request and response to `debug.log` in the config directory. |

Exit code: `0` report written and complete; `3` report written but something could not be read; `1` no report (sign-in, hub selection, the project list, or an interrupt).

## What counts as missing data

| Finding | What the API returned |
|---|---|
| Design with no model | A `DesignItem` whose `tipRootModel` is null. The document is listed, but has no properties, assembly relations, where-used or derivatives. |
| Drawing with no drawing data | A `DrawingItem` whose `tipDrawing` is null. |
| Configured design with no configuration table | A `ConfiguredDesignItem` whose `tipConfigurationTable` is null, or on which the API fails (`INTERNAL_SERVER_ERROR`) when asked for it. |
| Document with field errors | The row came back, with an API error on one of its fields. |
| Fusion file not recognised | A `.f3d` or `.f2d` file returned as a plain `BasicItem`. (Drawing templates, `.f2t`, and archives, `.f3z`, always come back as `BasicItem` and are not findings.) |
| Unreadable row | A list returned null in place of a document. There is no id; the report gives the location and the API's correlation id. |
| Folder whose contents do not add up | The folder's `objectCount` differs from the number of documents plus sub-folders that listing it returns. |

Files that are simply not CAD data (PDFs, STEP files, images and so on) are counted in the statistics as "Other files" and are not findings.

## What the report contains

1. **Result** — whether the report is complete, and the headline numbers.
2. **Summary** — projects, folders, documents by type, findings by kind, designs by file type with the share that has no model.
3. **By project** — one row per project with its id.
4. **Folders holding documents with missing data** — worst first, with folder ids.
5. **Documents missing MFGDM data** — every document, grouped by kind and then by location, with file type, size, last-modified date, item id, recheck outcome and any API error text.
6. **Folders whose contents do not add up** — with folder ids and the difference.
7. **Could not be read** — every list or row that still failed after retrying. If this section is not empty the report says "Incomplete" at the top.
8. **Projects not scanned** — inactive projects, unless `-include-inactive`.
9. **How this was collected** — requests, points, throttling, retries, rechecks.

## How accuracy is protected

- **Nothing is dropped silently.** The browser's API calls pass partial data through and drop the errors, which is right for an interactive list. The report uses a separate call (`api.QueryRaw`) that keeps every error with its path, and each one is attributed to the list or row it belongs to.
- **Throttling never costs data.** The API meters query points per minute and answers HTTP 429 once a minute's worth is spent (on the test account the quota behaved like 10,000 points, though a steady 8,000 a minute still drew an occasional 429, which is why the default is 6,500). The crawl spends points at a steady rate, `-points-per-minute` divided evenly across the minute, because bursts are what the API's counter punishes. If the API throttles anyway, every worker pauses (for `Retry-After`, or 30 seconds when the API sends none), the budget is cut by 15%, concurrency is halved, and the request is repeated — up to 30 times. A rate limit reported inside an HTTP 200 is treated the same way. A throttled response is never read as "no data".
- **Failures are retried, then reported.** HTTP 5xx / 408 and network failures are retried with a doubling backoff (six attempts). A list that returns an error is retried on its own up to four times. A refusal (`FORBIDDEN`) is not retried. Whatever still fails is listed under "Could not be read".
- **One broken document cannot hide its neighbours.** `tipConfigurationTable` is non-nullable, so when the API fails on it for one configured design it returns `results: null` for the whole page, with an error whose path points at a row that is not there. Read naively, that is an empty folder (and a project root has no object count to give the loss away). The crawl recognises such a page, lists it again without the data fields, and asks for every document on it by id, where the failure is confined to the one document. That document is reported with the API's error text; the report counts these pages and documents under *How this was collected*.
- **Every flag gets a second opinion.** A document flagged from a list page is asked for again on its own, by id, before it is reported; a flag the second answer does not support is withdrawn and counted. A page containing a null row is fetched a second time before the row is accepted as unreadable. A folder whose counts disagree is listed again with an ids-only query and its object count re-read; if the two listings differ from each other, that is reported as a read failure.
- **Empty folders are still asked.** A folder reporting zero objects is listed anyway. The object count is a cross-check, not a reason to skip.
- **Token expiry.** The access token is refreshed during long runs, and once more if the API answers 401.

## How it stays cheap

- **Batching.** Many folders' listings travel in one request, each under its own alias, packed to just under the API's limit of 1000 points per query.
- **Sized pages.** Points are charged on the page size asked for, not on what comes back (a list costs 11 points, plus 5 per document once its model is selected). Each folder's page is sized to its object count, so small folders pack many to a request and large folders use the biggest page a query may carry (170 documents).
- **Cost in practice.** About 5 points per document plus about 25 per folder. A project of 8,468 documents in 827 folders cost about 70,000 points; at the default budget that is roughly 11 minutes.

## Limits

- The report shows what the signed-in account can see.
- Object counts come from the API. A mismatch says the two disagree, not which one is right.
- The hub can change while the crawl runs.
- A run that is interrupted writes no report; there is no resume.
- Sub-folder rows that come back null cannot be walked; they are reported as unread.

The code is in `audit/` (crawler, requester, report writer) and `report_cmd.go` (flags, sign-in, hub selection).
