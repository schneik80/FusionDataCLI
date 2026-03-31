package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/schneik80/FusionDataCLI/config"
	"github.com/schneik80/FusionDataCLI/ui"
)

// version is set at build time via -ldflags "-X main.version=x.y.z".
var version = "dev"

func main() {
	cfg, cfgErr := config.Load()

	p := tea.NewProgram(
		ui.New(cfg, cfgErr, version),
		tea.WithAltScreen(),
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
