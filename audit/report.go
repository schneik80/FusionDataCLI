package audit

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// kindInfo is the report's wording for each kind of finding, in the
// order the sections appear.
var kindInfo = []struct {
	kind    Kind
	title   string
	explain string
}{
	{KindNoModel, "Designs with no model",
		"The document is listed as a design, but the API returns no model for it (`tipRootModel` is null). Without a model there are no properties, no assembly relations, no where-used and no derivatives."},
	{KindNoDrawing, "Drawings with no drawing data",
		"The document is listed as a drawing, but the API returns no drawing for it (`tipDrawing` is null)."},
	{KindNoConfigTable, "Configured designs with no configuration table",
		"The document is listed as a configured design, but the API returns no configuration table for it (`tipConfigurationTable` is null)."},
	{KindFieldError, "Documents with field errors",
		"The document came back, but the API reported an error on one of its fields."},
	{KindUnrecognised, "Fusion files not recognised as designs or drawings",
		"The file has a Fusion extension, but the API returns it as a plain file (`BasicItem`), so it has no design or drawing data at all."},
	{KindUnreadableRow, "Unreadable rows",
		"A list returned null in place of a document. The API gave no id, so only the location is known."},
}

// WriteMarkdown writes the report. version is the tool's version string.
func WriteMarkdown(w io.Writer, r *Result, version string) error {
	b := &strings.Builder{}
	p := func(format string, a ...any) { fmt.Fprintf(b, format, a...) }

	byKind := map[Kind][]Finding{}
	for _, f := range r.Findings {
		byKind[f.Kind] = append(byKind[f.Kind], f)
	}
	designs := r.ByType["DesignItem"]
	noModel := len(byKind[KindNoModel])

	p("# MFGDM data report: %s\n\n", cell(orDash(r.HubName)))
	p("| | |\n|---|---|\n")
	p("| Hub | %s |\n", cell(orDash(r.HubName)))
	p("| Hub id | %s |\n", code(r.HubID))
	p("| Hub data version | %s |\n", cell(orDash(r.HubDataVersion)))
	if len(r.ProjectFilter) > 0 {
		p("| Scope | Only these projects: %s |\n", cell(strings.Join(r.ProjectFilter, ", ")))
	} else {
		p("| Scope | Every project in the hub |\n")
	}
	p("| Started | %s |\n", r.Started.Format(time.RFC3339))
	p("| Duration | %s |\n", r.Finished.Sub(r.Started).Round(time.Second))
	p("| Tool | FusionDataCLI %s |\n\n", cell(version))

	// ---- Result -----------------------------------------------------
	p("## Result\n\n")
	if r.Complete() {
		p("**Complete.** Every project, folder and document list in scope was read.\n\n")
	} else {
		p("**Incomplete.** %s could not be read after retrying; they are listed under [Could not be read](#could-not-be-read). The counts below do not include whatever those hold.\n\n",
			plural(len(r.Unread), "list or row", "lists or rows"))
	}
	p("- **%s of %s** documents are missing MFGDM data (%s).\n", num(len(r.Findings)), num(r.TotalItems), pct(len(r.Findings), r.TotalItems))
	p("- **%s of %s** designs have no model (%s).\n", num(noModel), num(designs), pct(noModel, designs))
	p("- **%s contents that do not add up to their own object count.\n", plural(len(r.Folders), "folder** has", "folders** have"))
	if len(r.Skipped) > 0 {
		p("- %s not scanned; see [Projects not scanned](#projects-not-scanned).\n", plural(len(r.Skipped), "project was", "projects were"))
	}
	p("\n")

	// ---- Summary ----------------------------------------------------
	p("## Summary\n\n")
	p("| Measure | Count |\n|---|---:|\n")
	p("| Projects scanned | %s |\n", num(len(r.Projects)))
	p("| Projects not scanned | %s |\n", num(len(r.Skipped)))
	p("| Folders | %s |\n", num(r.TotalFolders))
	p("| Documents | %s |\n", num(r.TotalItems))
	for _, t := range typeOrder(r.ByType) {
		p("| — %s | %s |\n", cell(typeLabel(t)), num(r.ByType[t]))
	}
	p("| Documents missing MFGDM data | %s |\n", num(len(r.Findings)))
	for _, k := range kindInfo {
		if n := len(byKind[k.kind]); n > 0 {
			p("| — %s | %s |\n", k.title, num(n))
		}
	}
	p("| Folders whose contents do not add up | %s |\n", num(len(r.Folders)))
	p("| Lists or rows that could not be read | %s |\n", num(len(r.Unread)))
	p("\n")

	if len(r.DesignsByExt) > 0 {
		p("### Designs by file type\n\n")
		p("| File type | Designs | No model | Share with no model |\n|---|---:|---:|---:|\n")
		for _, ext := range sortedKeys(r.DesignsByExt) {
			n, miss := r.DesignsByExt[ext], r.DesignsNoModelByExt[ext]
			p("| %s | %s | %s | %s |\n", code(ext), num(n), num(miss), pct(miss, n))
		}
		p("\n")
	}

	if len(r.ByExt) > 0 {
		p("### Documents by type and file type\n\n")
		p("| Type | File type | Documents |\n|---|---|---:|\n")
		for _, typ := range typeOrder(r.ByType) {
			for _, k := range sortedKeys(r.ByExt) {
				if t, ext, _ := strings.Cut(k, "/"); t == typ {
					p("| %s | %s | %s |\n", cell(typeLabel(typ)), code(ext), num(r.ByExt[k]))
				}
			}
		}
		p("\n")
	}

	// ---- By project -------------------------------------------------
	p("## By project\n\n")
	if len(r.Projects) == 0 {
		p("No projects were scanned.\n\n")
	} else {
		p("| Project | Folders | Documents | Designs | Missing data | Share | Folders not adding up | Unread | Project id |\n|---|---:|---:|---:|---:|---:|---:|---:|---|\n")
		for _, pr := range r.Projects {
			p("| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n", cell(pr.Name), num(pr.Folders), num(pr.Items), num(pr.Designs),
				num(pr.Missing), pct(pr.Missing, pr.Items), num(pr.FolderMismatches), num(pr.Unread), code(pr.ID))
		}
		p("\n")
	}

	// ---- Folders with missing data ----------------------------------
	type folderTally struct {
		path, id string
		n        int
	}
	tallies := map[string]*folderTally{}
	for _, f := range r.Findings {
		key := f.Path + "\x00" + f.FolderID
		if tallies[key] == nil {
			tallies[key] = &folderTally{path: f.Path, id: f.FolderID}
		}
		tallies[key].n++
	}
	if len(tallies) > 0 {
		list := make([]*folderTally, 0, len(tallies))
		for _, t := range tallies {
			list = append(list, t)
		}
		sort.Slice(list, func(i, j int) bool {
			if list[i].n != list[j].n {
				return list[i].n > list[j].n
			}
			return strings.ToLower(list[i].path) < strings.ToLower(list[j].path)
		})
		p("## Folders holding documents with missing data\n\n")
		p("| Location | Documents missing data | Folder id |\n|---|---:|---|\n")
		for _, t := range list {
			id := "(project root)"
			if t.id != "" {
				id = code(t.id)
			}
			p("| %s | %s | %s |\n", cell(t.path), num(t.n), id)
		}
		p("\n")
	}

	// ---- Documents --------------------------------------------------
	p("## Documents missing MFGDM data\n\n")
	if len(r.Findings) == 0 {
		p("None found.\n\n")
	}
	for _, k := range kindInfo {
		fs := byKind[k.kind]
		if len(fs) == 0 {
			continue
		}
		p("### %s (%s)\n\n%s\n\n", k.title, num(len(fs)), k.explain)
		// Findings arrive sorted by path, then name.
		for i := 0; i < len(fs); {
			j := i
			for j < len(fs) && fs[j].Path == fs[i].Path && fs[j].FolderID == fs[i].FolderID {
				j++
			}
			loc := "project root"
			if fs[i].FolderID != "" {
				loc = "folder id " + code(fs[i].FolderID)
			}
			p("#### %s\n\n%s, %s\n\n", cell(fs[i].Path), plural(j-i, "document", "documents"), loc)
			p("| Document | File type | Size | Last modified | Item id | Recheck | API detail |\n|---|---|---:|---|---|---|---|\n")
			for _, f := range fs[i:j] {
				id := "(none returned)"
				if f.ItemID != "" {
					id = code(f.ItemID)
				}
				p("| %s | %s | %s | %s | %s | %s | %s |\n", cell(orDash(f.Name)), code(orDash(f.Ext)), size(f.Size), day(f.Modified), id, cell(orDash(f.Recheck)), cell(orDash(f.Detail)))
			}
			p("\n")
			i = j
		}
	}

	// ---- Folder mismatches ------------------------------------------
	p("## Folders whose contents do not add up\n\n")
	if len(r.Folders) == 0 {
		p("None found.\n\n")
	} else {
		p("Each folder reports how many objects it holds. For these folders, listing the documents and sub-folders through the API returns a different number. A positive difference means objects the API does not list.\n\n")
		p("| Folder | Object count | Documents listed | Sub-folders listed | Difference | Note | Folder id |\n|---|---:|---:|---:|---:|---|---|\n")
		for _, f := range r.Folders {
			p("| %s | %s | %s | %s | %+d | %s | %s |\n", cell(f.Path), num(f.ObjectCount), num(f.Items), num(f.Subfolders),
				f.ObjectCount-f.Items-f.Subfolders, cell(orDash(f.Note)), code(f.FolderID))
		}
		p("\n")
	}

	// ---- Unread -----------------------------------------------------
	p("## Could not be read\n\n")
	if len(r.Unread) == 0 {
		p("Nothing. Every list in scope was read.\n\n")
	} else {
		p("These lists or rows failed after retrying. Whatever they hold is not in this report.\n\n")
		p("| What | Location | Reason | Id |\n|---|---|---|---|\n")
		for _, u := range r.Unread {
			p("| %s | %s | %s | %s |\n", cell(u.What), cell(orDash(u.Path)), cell(u.Reason), code(orDash(u.ID)))
		}
		p("\n")
	}

	// ---- Skipped ----------------------------------------------------
	if len(r.Skipped) > 0 {
		p("## Projects not scanned\n\n")
		p("| Project | Status | Reason | Project id |\n|---|---|---|---|\n")
		for _, s := range r.Skipped {
			p("| %s | %s | %s | %s |\n", cell(s.Name), cell(orDash(s.Status)), cell(s.Reason), code(s.ID))
		}
		p("\n")
	}

	// ---- Method -----------------------------------------------------
	st := r.Requests
	p("## How this was collected\n\n")
	p("| Measure | Value |\n|---|---:|\n")
	p("| API requests | %s |\n", num(st.Requests))
	p("| Query points spent | %s |\n", num(st.Points))
	p("| Throttled responses (HTTP 429) | %s |\n", num(st.Throttled))
	p("| Time held back by throttling | %s |\n", st.ThrottleWait.Round(time.Second))
	p("| Worker time spent waiting for points (summed over workers) | %s |\n", st.PacingWait.Round(time.Second))
	p("| Points-per-minute budget at the end | %s |\n", num(st.PointsPerMinute))
	p("| Server errors retried (HTTP 5xx / 408) | %s |\n", num(st.ServerErrors))
	p("| Network failures retried | %s |\n", num(st.TransportErrors))
	p("| Token refreshes | %s |\n", num(st.TokenRefreshes))
	p("| Requests abandoned after every retry | %s |\n", num(st.GaveUp))
	p("| Lowest concurrency reached | %s |\n", num(st.MinConcurrency))
	p("| Documents flagged from list pages | %s |\n", num(r.Flagged))
	p("| Flags withdrawn on recheck | %s |\n", num(r.Cleared))
	p("| List pages blanked by a broken document | %s |\n", num(r.LeanPages))
	p("| Documents on those pages checked one by one | %s |\n", num(r.Isolated))
	p("| Folder mismatches withdrawn on re-count | %s |\n", num(r.FoldersCleared))
	p("| Duplicate rows ignored | %s |\n", num(r.Duplicates))
	p("\n")
	p("- **Coverage.** Every active project is walked from its root through every sub-folder. Each folder is asked for its documents and its sub-folders, including folders that report an object count of zero.\n")
	p("- **Second opinion.** A document flagged from a list page is asked for again on its own, by id, before it is reported. \"confirmed\" in the Recheck column means the second answer agreed. A folder whose counts disagree is listed a second time and its object count re-read.\n")
	p("- **Broken documents.** When the API fails on one document's data, it can return the whole page of the list empty. Such a page is listed again without the data fields and every document on it is then asked for by id, so one broken document cannot hide its neighbours.\n")
	p("- **Throttling.** The API meters query points per minute. The crawl spends points at a steady rate under a per-minute budget. If the API throttles anyway (HTTP 429), every worker pauses, the budget is cut, and the request is repeated. No throttled or failed response is ever treated as an answer.\n")
	p("- **Errors.** Every error the API returns is attributed to the list or row it belongs to. Anything still failing after retries is under [Could not be read](#could-not-be-read).\n")
	p("- **Limits.** The report shows what the signed-in account can see. Object counts come from the API and are a cross-check, not ground truth. The hub can change while the crawl runs.\n")

	_, err := io.WriteString(w, b.String())
	return err
}

func typeLabel(t string) string {
	switch t {
	case "DesignItem":
		return "Designs"
	case "DrawingItem":
		return "Drawings"
	case "ConfiguredDesignItem":
		return "Configured designs"
	case "BasicItem":
		return "Other files"
	case "(unreadable)":
		return "Unreadable rows"
	}
	return t
}

// typeOrder lists the document types present, CAD data first.
func typeOrder(byType map[string]int) []string {
	known := []string{"DesignItem", "ConfiguredDesignItem", "DrawingItem", "BasicItem"}
	var out []string
	seen := map[string]bool{}
	for _, t := range known {
		if _, ok := byType[t]; ok {
			out = append(out, t)
			seen[t] = true
		}
	}
	for _, t := range sortedKeys(byType) {
		if !seen[t] {
			out = append(out, t)
		}
	}
	return out
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// cell makes text safe for a markdown table cell.
func cell(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	for _, ch := range []string{"<", ">", "*", "_", "`", "[", "]", "#", "~"} {
		s = strings.ReplaceAll(s, ch, "\\"+ch)
	}
	return s
}

// code renders an id as inline code. Ids are URNs: no backticks, but a
// pipe would still break a table.
func code(s string) string {
	if s == "—" {
		return s
	}
	s = strings.ReplaceAll(s, "`", "'")
	s = strings.ReplaceAll(s, "|", "\\|")
	return "`" + s + "`"
}

// size renders a byte count as the API gives it (a decimal string).
func size(s string) string {
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return orDash(cell(s))
	}
	return num(n) + " B"
}

// day keeps the date of an RFC 3339 timestamp.
func day(s string) string {
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
		return s[:10]
	}
	return orDash(cell(s))
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func num(n int) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return s
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func pct(n, total int) string {
	if total == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(total))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return num(n) + " " + many
}
