package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/schneik80/FusionDataCLI/config"
	"github.com/schneik80/FusionDataCLI/ui"
	"github.com/schneik80/FusionDataCLI/web"
)

// version is set at build time via -ldflags "-X main.version=x.y.z".
var version = "dev"

func main() {
	// `fusiondatacli serve` → HTTP Fasteners Enrichment UI.
	// `fusiondatacli`       → existing interactive TUI (unchanged).
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		if err := web.Run(os.Args[2:], version); err != nil {
			fmt.Fprintf(os.Stderr, "serve: %v\n", err)
			os.Exit(1)
		}
		return
	}

	cfg, cfgErr := config.Load()

	p := tea.NewProgram(
		ui.New(cfg, cfgErr, version),
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
