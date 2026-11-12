package audit

import (
	"strings"
	"testing"
)

func render(t *testing.T, r *Result) string {
	t.Helper()
	var b strings.Builder
	if err := WriteMarkdown(&b, r, "v-test"); err != nil {
		t.Fatalf("WriteMarkdown: %v", err)
	}
	return b.String()
}

func TestWriteMarkdown_ReportsEveryFindingWithItsID(t *testing.T) {
	r, err := sampleHub(t).crawl(Options{}, 4, 0)
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	md := render(t, r)

	for _, want := range []string{
		"# MFGDM data report: Test Hub",
		"| Hub id | `HUB` |",
		"**Incomplete.** 2 lists or rows could not be read",
		"**7 of 445** documents are missing MFGDM data (1.6%)",
		"**3 of 439** designs have no model (0.7%)",
		"**1 folder** has contents that do not add up",
		// Every finding with an id shows that id.
		"`ipt-1`", "`big-123`", "`err-1`", "`dwg-bad`", "`cfg-bad`", "`basic-f3d`",
		// The unreadable row has none, and says where it was.
		"(none returned)", "corr-ghost",
		// Folder ids for the locations and the mismatch.
		"folder id `deep`", "| Alpha / short | 5 | 4 | 0 | +1 | confirmed by re-count | `short` |",
		// The forbidden folder and the skipped project.
		"| documents of folder | Alpha / secret |", "| Old | INACTIVE |",
		// Per-extension breakdown: the .ipt design is 100% missing.
		"| `ipt` | 1 | 1 | 100.0% |",
		"| Alpha | 6 | 415 | 409 | 7 | 1.7% | 1 | 2 | `Alpha` |",
		"| Flags withdrawn on recheck | 1 |",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("report is missing %q", want)
		}
	}
	// The cleared flaky design must not appear anywhere.
	if strings.Contains(md, "flaky-1") {
		t.Error("report lists the design whose flag was cleared on recheck")
	}
}

func TestWriteMarkdown_CompleteAndEmpty(t *testing.T) {
	h := newFakeHub(t, project("Clean", "ACTIVE", folder("", designs("d", 3))))
	r, err := h.crawl(Options{}, 2, 0)
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	md := render(t, r)
	for _, want := range []string{
		"**Complete.** Every project, folder and document list in scope was read.",
		"**0 of 3** documents are missing MFGDM data (0.0%)",
		"## Documents missing MFGDM data\n\nNone found.",
		"## Folders whose contents do not add up\n\nNone found.",
		"## Could not be read\n\nNothing.",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("report is missing %q", want)
		}
	}
	if strings.Contains(md, "Projects not scanned\n\n|") {
		t.Error("empty skipped-projects table rendered")
	}
}

// Names come from users and go into markdown tables; a pipe or a
// newline in one must not break the row.
func TestWriteMarkdown_EscapesNames(t *testing.T) {
	bad := &fItem{id: "x-1", name: "a|b\n*c*", typ: "DesignItem", ext: "f3d", noData: true}
	h := newFakeHub(t, project("P|Q", "ACTIVE", folder("", []*fItem{bad})))
	h.projects[0].name = "P|Q"
	r, err := h.crawl(Options{}, 1, 0)
	if err != nil {
		t.Fatalf("crawl: %v", err)
	}
	md := render(t, r)
	if !strings.Contains(md, `| a\|b \*c\* | `+"`f3d`"+` | 0 B | 2019-05-29 | `+"`x-1`"+` | confirmed |`) {
		t.Errorf("document row not escaped:\n%s", md)
	}
	for _, line := range strings.Split(md, "\n") {
		if strings.Contains(line, "x-1") && strings.Count(strings.ReplaceAll(line, `\|`, ""), "|") != 8 {
			t.Errorf("row has the wrong number of cells: %q", line)
		}
	}
}

func TestFormatters(t *testing.T) {
	if got := num(1234567); got != "1,234,567" {
		t.Errorf("num = %q", got)
	}
	if got := num(999); got != "999" {
		t.Errorf("num = %q", got)
	}
	if got := pct(1, 3); got != "33.3%" {
		t.Errorf("pct = %q", got)
	}
	if got := pct(0, 0); got != "—" {
		t.Errorf("pct of nothing = %q", got)
	}
	if got := plural(1, "folder", "folders"); got != "1 folder" {
		t.Errorf("plural = %q", got)
	}
}
