package ui

import "github.com/charmbracelet/lipgloss"

// ---------------------------------------------------------------------------
// Themes
// ---------------------------------------------------------------------------

type colorTheme struct {
	name      string
	accent    lipgloss.Color
	subtle    lipgloss.Color
	muted     lipgloss.Color
	fg        lipgloss.Color
	errCol    lipgloss.Color
	detailKey lipgloss.Color // label color for detail panel metadata keys
}

var themes = []colorTheme{
	{
		// Rust — original warm orange palette
		name:      "Rust",
		accent:    lipgloss.Color("#C05A1F"),
		subtle:    lipgloss.Color("#555555"),
		muted:     lipgloss.Color("#888888"),
		fg:        lipgloss.Color("#FFFFFF"),
		errCol:    lipgloss.Color("#FF5555"),
		detailKey: lipgloss.Color("#888888"),
	},
	{
		// Mono — greyscale only
		name:      "Mono",
		accent:    lipgloss.Color("#CCCCCC"),
		subtle:    lipgloss.Color("#444444"),
		muted:     lipgloss.Color("#777777"),
		fg:        lipgloss.Color("#FFFFFF"),
		errCol:    lipgloss.Color("#AAAAAA"),
		detailKey: lipgloss.Color("#999999"),
	},
	{
		// System — ANSI color tokens; inherits the terminal's own color scheme
		// (same colors ls uses: blue for directories, bright-black for dim text)
		name:      "System",
		accent:    lipgloss.Color("4"),  // ANSI blue         — directories in ls
		subtle:    lipgloss.Color("8"),  // ANSI bright-black — dim / inactive
		muted:     lipgloss.Color("8"),  // ANSI bright-black
		fg:        lipgloss.Color("7"),  // ANSI white        — normal foreground
		errCol:    lipgloss.Color("1"),  // ANSI red
		detailKey: lipgloss.Color("6"),  // ANSI cyan         — high contrast label color
	},
}

var activeThemeIdx = 0

// cycleTheme advances to the next theme, rebuilds all styles, and returns the
// new theme name for display in the status bar.
func cycleTheme() string {
	activeThemeIdx = (activeThemeIdx + 1) % len(themes)
	applyTheme(themes[activeThemeIdx])
	return themes[activeThemeIdx].name
}

// ---------------------------------------------------------------------------
// Style variables — rebuilt on every theme change via applyTheme
// ---------------------------------------------------------------------------

var (
	colorAccent   lipgloss.Color
	colorSubtle   lipgloss.Color
	colorMuted    lipgloss.Color
	colorSelected lipgloss.Color

	styleHeader         lipgloss.Style
	styleStatus         lipgloss.Style
	styleColumnActive   lipgloss.Style
	styleColumnInactive lipgloss.Style
	styleColumnTitle    lipgloss.Style
	styleItemSelected   lipgloss.Style
	styleItemNormal     lipgloss.Style
	styleItemDim        lipgloss.Style
	styleFooter         lipgloss.Style
	styleLoading        lipgloss.Style
	styleError          lipgloss.Style
	styleKindBadge      lipgloss.Style
	styleDetailKey      lipgloss.Style
)

func applyTheme(t colorTheme) {
	colorAccent = t.accent
	colorSubtle = t.subtle
	colorMuted = t.muted
	colorSelected = t.fg

	styleHeader = lipgloss.NewStyle().
		Bold(true).
		Foreground(colorAccent).
		Padding(0, 1)

	styleStatus = lipgloss.NewStyle().
		Foreground(colorMuted).
		Italic(true)

	styleColumnActive = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorAccent).
		Padding(0, 1)

	styleColumnInactive = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorSubtle).
		Padding(0, 1)

	styleColumnTitle = lipgloss.NewStyle().
		Bold(true).
		Foreground(colorAccent).
		MarginBottom(1)

	styleItemSelected = lipgloss.NewStyle().
		Bold(true).
		Foreground(colorSelected).
		Background(colorAccent).
		Padding(0, 1)

	styleItemNormal = lipgloss.NewStyle().
		Foreground(colorSelected).
		Padding(0, 1)

	styleItemDim = lipgloss.NewStyle().
		Foreground(colorMuted).
		Padding(0, 1)

	styleFooter = lipgloss.NewStyle().
		Foreground(colorMuted).
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(colorSubtle).
		Padding(0, 1)

	styleLoading = lipgloss.NewStyle().
		Foreground(colorMuted).
		Italic(true).
		Padding(0, 1)

	styleError = lipgloss.NewStyle().
		Foreground(t.errCol).
		Bold(true).
		Padding(1, 2)

	styleKindBadge = lipgloss.NewStyle().
		Foreground(colorMuted).
		Faint(true)

	styleDetailKey = lipgloss.NewStyle().
		Foreground(t.detailKey)
}

func init() {
	applyTheme(themes[0])
}

// ---------------------------------------------------------------------------
// Icons
// ---------------------------------------------------------------------------

// kindIcon returns a short prefix icon for the item kind.
func kindIcon(kind string) string {
	switch kind {
	case "hub":
		return "⬡ "
	case "project":
		return "◈ "
	case "folder":
		return "  "
	case "design", "configured":
		return "  "
	case "drawing":
		return "  "
	default:
		return "  "
	}
}
