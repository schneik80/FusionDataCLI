package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/schneik80/FusionDataCLI/config"
	"github.com/schneik80/FusionDataCLI/ui"
)

// version is set at build time via -ldflags "-X main.version=x.y.z".
var version = "dev"

func main() {
	// Catch any panic that propagates out of the BubbleTea event loop and
	// write the cause + full goroutine stack to <config.Dir()>/panic.log.
	// Without this, the alternate-screen render is restored but the panic
	// dump scrolls past in the terminal and is hard to recover, especially
	// when the goroutine that panicked is something other than the main
	// loop. Re-panic afterward so the process still exits non-zero.
	defer func() {
		if r := recover(); r != nil {
			writePanicLog(r)
			panic(r)
		}
	}()

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

func writePanicLog(r any) {
	dir, err := config.Dir()
	if err != nil {
		return
	}
	path := filepath.Join(dir, "panic.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "=== panic at %s — version=%s ===\n", time.Now().Format(time.RFC3339), version)
	fmt.Fprintf(f, "%v\n\n%s\n", r, debug.Stack())
}
