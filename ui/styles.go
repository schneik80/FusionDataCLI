package ui

import "github.com/charmbracelet/lipgloss"

var (
	colorAccent   = lipgloss.Color("#C05A1F") // rust orange
	colorSubtle   = lipgloss.Color("#555555")
	colorMuted    = lipgloss.Color("#888888")
	colorSelected = lipgloss.Color("#FFFFFF")
	colorBg       = lipgloss.Color("#1a1a2e")

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
			Foreground(lipgloss.Color("#FF5555")).
			Bold(true).
			Padding(1, 2)

	styleKindBadge = lipgloss.NewStyle().
			Foreground(colorMuted).
			Faint(true)
)

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
