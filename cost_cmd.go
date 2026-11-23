package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/schneik80/FusionDataCLI/api"
	"github.com/schneik80/FusionDataCLI/auth"
	"github.com/schneik80/FusionDataCLI/config"
	"github.com/schneik80/FusionDataCLI/costwatch"
)

// costWatchFlags are the options of `fusiondatacli -cost-watch`. The
// hub, project, concurrency and debug flags are shared with
// -mfgdm-report.
type costWatchFlags struct {
	run       bool
	interval  time.Duration
	once      bool
	reprice   bool
	dryRun    bool
	clearAsm  bool
	estimator string
}

func registerCostWatchFlags(fs *flag.FlagSet) *costWatchFlags {
	f := &costWatchFlags{}
	fs.BoolVar(&f.run, "cost-watch", false, "watch one project (-project) and write an Estimated Cost to every newly saved design (no interactive browser)")
	fs.DurationVar(&f.interval, "interval", 20*time.Second, "with -cost-watch: time between checks of the project")
	fs.BoolVar(&f.once, "once", false, "with -cost-watch: check the project once and exit")
	fs.BoolVar(&f.reprice, "reprice", false, "with -cost-watch: also price designs that already have an Estimated Cost")
	fs.BoolVar(&f.dryRun, "dry-run", false, "with -cost-watch: estimate and print, but write nothing")
	fs.BoolVar(&f.clearAsm, "clear-assemblies", false, "with -cost-watch: remove an Estimated Cost stored on a design that has child components, so its cost is the roll-up of its children again")
	fs.StringVar(&f.estimator, "estimator", "auto", "with -cost-watch: where prices come from: claude (web search read by Claude Sonnet), random, or auto (claude when an Anthropic API key is given, else random)")
	return f
}

// runCostWatch is the whole -cost-watch command. It returns the
// process exit code.
func runCostWatch(f *costWatchFlags, shared *reportFlags, cfg *config.Config, cfgErr error) int {
	if cfgErr != nil || cfg == nil || cfg.ClientID == "" {
		fmt.Fprintf(os.Stderr, "No APS client id configured (%v). Run fusiondatacli once without flags to set up.\n", cfgErr)
		return 1
	}
	if shared.concurrency < 1 || shared.concurrency > 16 {
		fmt.Fprintln(os.Stderr, "-concurrency must be between 1 and 16")
		return 1
	}
	if f.interval < 5*time.Second {
		fmt.Fprintln(os.Stderr, "-interval must be at least 5s")
		return 1
	}
	if strings.TrimSpace(shared.projects) == "" || strings.Contains(shared.projects, ",") {
		fmt.Fprintln(os.Stderr, "-cost-watch needs exactly one -project (name or id)")
		return 1
	}
	api.SetRegion(cfg.Region)
	if shared.debug {
		api.EnableDebug()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	estimator, label, err := pickEstimator(f.estimator)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// Writing a property needs data:write, which logins made before
	// this command existed do not carry. A refresh keeps the old
	// scopes, so only a new sign-in adds it.
	if !f.dryRun {
		if td, _ := auth.LoadTokens(); td != nil && !td.HasScope("data:write") {
			fmt.Fprintln(os.Stderr, "The saved Autodesk login cannot write properties. Opening the browser to sign in again…")
			lctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			_, err := auth.Login(lctx, cfg.ClientID, cfg.ClientSecret)
			cancel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Sign-in failed: %v\n", err)
				return 1
			}
		}
	}

	tokens := newTokenSource(cfg)
	token, err := tokens(ctx, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Sign-in failed: %v\n", err)
		return 1
	}
	hubs, err := api.GetHubs(ctx, token)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Listing hubs failed: %v\n", err)
		return 1
	}
	hub, err := pickHub(hubs, shared.hub)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	projects, err := api.GetProjects(ctx, token, hub.ID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Listing projects failed: %v\n", err)
		return 1
	}
	project, err := pickProject(projects, strings.TrimSpace(shared.projects))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	mode := fmt.Sprintf("checking every %s; Ctrl+C to stop", f.interval)
	if f.once {
		mode = "one check"
	}
	if f.dryRun {
		mode += "; dry run, nothing is written"
	}
	fmt.Printf("Watching %q in hub %q (%s). Prices: %s; up to %d at once.\n", project.Name, hub.Name, mode, label, shared.concurrency)

	failed := 0
	err = costwatch.Run(ctx, costwatch.Options{
		HubID:           hub.ID,
		ProjectID:       project.ID,
		Interval:        f.interval,
		Concurrency:     shared.concurrency,
		Once:            f.once,
		Reprice:         f.reprice,
		DryRun:          f.dryRun,
		ClearAssemblies: f.clearAsm,
		Estimator:       estimator,
		Tokens:          costwatch.TokenSource(tokens),
		OnPass: func(designs, changed int) {
			if changed > 0 {
				fmt.Printf("%s  %d designs, %d new or changed\n", time.Now().Format("15:04:05"), designs, changed)
			}
		},
		OnEvent: func(e costwatch.Event) {
			line := fmt.Sprintf("%s  %-11s %s", e.Time.Format("15:04:05.000"), e.Action, e.Design)
			if e.Action != "failed" && e.Price != 0 {
				line += fmt.Sprintf("  $%.2f", e.Price)
			}
			if e.Detail != "" {
				line += "  — " + e.Detail
			}
			if e.Action == "failed" {
				failed++
			}
			fmt.Println(line)
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cost watch failed: %v\n", err)
		return 1
	}
	if f.once && failed > 0 {
		return 3
	}
	return 0
}

// pickEstimator chooses where prices come from. In auto mode a user
// with no ANTHROPIC_API_KEY set is asked for a key at the terminal; the
// key is used for this run only and is not saved.
func pickEstimator(want string) (costwatch.Estimator, string, error) {
	const claudeLabel = "Claude Sonnet web search (random guess if a lookup fails)"
	key := os.Getenv("ANTHROPIC_API_KEY")
	switch want {
	case "random":
		return costwatch.RandomEstimator{}, "random guesses", nil
	case "claude", "auto":
		if key == "" && term.IsTerminal(os.Stdin.Fd()) {
			prompt := "Anthropic API key (not saved): "
			if want == "auto" {
				prompt = "Anthropic API key for Claude price lookups (not saved; Enter for random guesses): "
			}
			fmt.Fprint(os.Stderr, prompt)
			b, err := term.ReadPassword(os.Stdin.Fd())
			fmt.Fprintln(os.Stderr)
			if err != nil {
				return nil, "", fmt.Errorf("reading the API key: %w", err)
			}
			key = strings.TrimSpace(string(b))
		}
		if key == "" {
			if want == "claude" {
				return nil, "", fmt.Errorf("-estimator claude needs an Anthropic API key: set ANTHROPIC_API_KEY or enter one when asked")
			}
			return costwatch.RandomEstimator{}, "random guesses", nil
		}
		return costwatch.WithRandomFallback(costwatch.NewClaudeEstimator(key)), claudeLabel, nil
	}
	return nil, "", fmt.Errorf("-estimator must be claude, random or auto")
}

// pickProject resolves -project against a hub's projects: an id or
// exact name first, then a unique partial name.
func pickProject(projects []api.NavItem, want string) (api.NavItem, error) {
	var exact, partial []api.NavItem
	for _, p := range projects {
		switch {
		case p.ID == want || strings.EqualFold(p.Name, want):
			exact = append(exact, p)
		case strings.Contains(strings.ToLower(p.Name), strings.ToLower(want)):
			partial = append(partial, p)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(exact) == 0 && len(partial) == 1:
		return partial[0], nil
	case len(exact) > 1 || len(partial) > 1:
		return api.NavItem{}, fmt.Errorf("-project %q matches more than one project; use the full name or the id", want)
	}
	return api.NavItem{}, fmt.Errorf("no project in this hub matches %q", want)
}
