package ui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/schneik80/FusionDataCLI/api"
)

// TestBrowserLayoutFitsTerminal is a regression test for the bug where the
// footer help text — measured with len() (bytes) instead of lipgloss.Width
// (display columns) — wrapped to a second line on narrow terminals, pushing
// the header off the top of the screen.
func TestBrowserLayoutFitsTerminal(t *testing.T) {
	sizes := []struct{ w, h int }{
		{120, 40}, {120, 30}, {120, 20},
		{100, 30}, {100, 24},
		{80, 24}, {80, 20},
		{60, 20},
	}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			m := sampleBrowsingModel(size.w, size.h)
			out := m.viewBrowser()
			h := lipgloss.Height(out)
			if h > size.h {
				t.Fatalf("output %d lines exceeds terminal %d (header will scroll off)", h, size.h)
			}
			first := strings.Split(out, "\n")[0]
			if !strings.Contains(first, "FusionDataCLI") {
				t.Fatalf("header missing from first line: %q", first)
			}
		})
	}
}

// TestFitFooterLineNeverWraps covers the fitFooterLine helper directly: for
// any width down to a single column, the returned line must never exceed
// the requested display width.
func TestFitFooterLineNeverWraps(t *testing.T) {
	help := "[↑↓/jk] move  [←→/l] nav  [h] hubs  [o] open  [r] refresh  [t] theme  [m] mouse:on  [a] about  [q] quit"
	version := "2.0.3"
	for w := 1; w <= 200; w++ {
		got := fitFooterLine(help, version, w)
		if lipgloss.Width(got) > w {
			t.Errorf("width=%d: fitFooterLine returned %q (display width %d) which exceeds %d", w, got, lipgloss.Width(got), w)
		}
	}
}

func sampleBrowsingModel(width, height int) Model {
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	return Model{
		state:         stateBrowsing,
		width:         width,
		height:        height,
		spinner:       sp,
		version:       "2.0.3",
		activeCol:     colProjects,
		hubs:          []api.NavItem{{ID: "H1", Name: "ADSK-Schneik", Kind: "hub"}},
		selectedHubID: "H1",
		cols: [numCols][]api.NavItem{
			{{ID: "P1", Name: "4WD Buggy", Kind: "project", IsContainer: true}},
			{{ID: "D1", Name: "MyDesign", Kind: "design"}},
		},
		details:      &api.ItemDetails{Name: "MyDesign", Size: "12345"},
		detailsCache: make(map[string]*api.ItemDetails),
		styleCache:   &styleCache{},
	}
}
