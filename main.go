package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/schneik80/apsnav/config"
	"github.com/schneik80/apsnav/ui"
)

func main() {
	cfg, cfgErr := config.Load()

	p := tea.NewProgram(
		ui.New(cfg, cfgErr),
		tea.WithAltScreen(),
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
