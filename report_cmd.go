package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/schneik80/FusionDataCLI/api"
	"github.com/schneik80/FusionDataCLI/audit"
	"github.com/schneik80/FusionDataCLI/auth"
	"github.com/schneik80/FusionDataCLI/config"
)

// reportFlags are the options of `fusiondatacli -mfgdm-report`.
type reportFlags struct {
	run             bool
	hub             string
	projects        string
	out             string
	concurrency     int
	includeInactive bool
	pointsPerMinute int
	debug           bool
}

func registerReportFlags(fs *flag.FlagSet) *reportFlags {
	f := &reportFlags{}
	fs.BoolVar(&f.run, "mfgdm-report", false, "walk a whole hub and write a Markdown report of documents and folders missing MFGDM data (no interactive browser)")
	fs.StringVar(&f.hub, "hub", "", "with -mfgdm-report: hub name or id (required when the account sees more than one hub)")
	fs.StringVar(&f.projects, "project", "", "with -mfgdm-report: only these projects, comma-separated names or ids (default: every project)")
	fs.StringVar(&f.out, "out", "", "with -mfgdm-report: report file (default: mfgdm-report-<hub>-<timestamp>.md in the current directory)")
	fs.IntVar(&f.concurrency, "concurrency", 4, "with -mfgdm-report: most API requests in flight at once; lowered automatically when the API throttles")
	fs.IntVar(&f.pointsPerMinute, "points-per-minute", audit.DefaultPointsPerMinute, "with -mfgdm-report: API query points to spend per minute; lowered automatically when the API throttles")
	fs.BoolVar(&f.includeInactive, "include-inactive", false, "with -mfgdm-report: also walk projects that are not active (the API usually refuses them)")
	fs.BoolVar(&f.debug, "debug", false, "with -mfgdm-report: log every request and response to debug.log in the config directory")
	return f
}

// runReport is the whole -mfgdm-report command. It returns the process
// exit code: 0 for a complete report, 1 for a failure, 3 when a report
// was written but something could not be read.
func runReport(f *reportFlags, cfg *config.Config, cfgErr error) int {
	if cfgErr != nil || cfg == nil || cfg.ClientID == "" {
		fmt.Fprintf(os.Stderr, "No APS client id configured (%v). Run fusiondatacli once without flags to set up.\n", cfgErr)
		return 1
	}
	if f.concurrency < 1 || f.concurrency > 16 {
		fmt.Fprintln(os.Stderr, "-concurrency must be between 1 and 16")
		return 1
	}
	if f.pointsPerMinute < 100 {
		fmt.Fprintln(os.Stderr, "-points-per-minute must be at least 100")
		return 1
	}
	api.SetRegion(cfg.Region)
	if f.debug {
		api.EnableDebug()
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

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
	hub, err := pickHub(hubs, f.hub)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	var projects []string
	for _, p := range strings.Split(f.projects, ",") {
		if p = strings.TrimSpace(p); p != "" {
			projects = append(projects, p)
		}
	}

	started := time.Now()
	out := f.out
	if out == "" {
		out = fmt.Sprintf("mfgdm-report-%s-%s.md", slug(hub.Name), started.Format("20060102-150405"))
	}
	// Fail on an unwritable destination now, not after an hour's crawl.
	fh, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot write report: %v\n", err)
		return 1
	}
	defer fh.Close()

	fmt.Fprintf(os.Stderr, "MFGDM data report for hub %q\n", hub.Name)
	res, err := audit.Run(ctx, tokens, audit.Options{
		HubID:           hub.ID,
		Projects:        projects,
		IncludeInactive: f.includeInactive,
		Concurrency:     f.concurrency,
		PointsPerMinute: f.pointsPerMinute,
		Progress: func(p audit.Progress) {
			line := fmt.Sprintf("[%s] %s: %d/%d folders and projects listed, %d documents, %d flagged, %d requests",
				time.Since(started).Round(time.Second), p.Phase, p.Done, p.Containers, p.Items, p.Flagged, p.Requests)
			if p.Throttled > 0 {
				line += fmt.Sprintf(", throttled %d times (now %d points/min, concurrency %d)", p.Throttled, p.Budget, p.Limit)
			}
			fmt.Fprintln(os.Stderr, line)
		},
	})
	if err != nil {
		fh.Close()
		os.Remove(out)
		if ctx.Err() != nil {
			fmt.Fprintln(os.Stderr, "Interrupted; no report written.")
		} else {
			fmt.Fprintf(os.Stderr, "Report failed: %v\n", err)
		}
		return 1
	}
	if err := audit.WriteMarkdown(fh, res, version); err != nil {
		fmt.Fprintf(os.Stderr, "Writing report failed: %v\n", err)
		return 1
	}
	if err := fh.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "Writing report failed: %v\n", err)
		return 1
	}

	abs, _ := filepath.Abs(out)
	fmt.Fprintf(os.Stderr, "\n%d of %d documents are missing MFGDM data; folders whose contents do not add up: %d.\n", len(res.Findings), res.TotalItems, len(res.Folders))
	if !res.Complete() {
		fmt.Fprintf(os.Stderr, "INCOMPLETE: %d lists or rows could not be read; see the report.\n", len(res.Unread))
	}
	fmt.Fprintf(os.Stderr, "Report: %s\n", abs)
	if !res.Complete() {
		return 3
	}
	return 0
}

// pickHub resolves -hub against the hubs the account can see.
func pickHub(hubs []api.NavItem, want string) (api.NavItem, error) {
	list := func() string {
		var b strings.Builder
		for _, h := range hubs {
			fmt.Fprintf(&b, "  %s\n", h.Name)
		}
		return b.String()
	}
	if len(hubs) == 0 {
		return api.NavItem{}, fmt.Errorf("this account sees no hubs")
	}
	if want == "" {
		if len(hubs) == 1 {
			return hubs[0], nil
		}
		return api.NavItem{}, fmt.Errorf("-hub is required; this account sees %d hubs:\n%s", len(hubs), list())
	}
	var exact, partial []api.NavItem
	for _, h := range hubs {
		switch {
		case h.ID == want || strings.EqualFold(h.Name, want):
			exact = append(exact, h)
		case strings.Contains(strings.ToLower(h.Name), strings.ToLower(want)):
			partial = append(partial, h)
		}
	}
	switch {
	case len(exact) == 1:
		return exact[0], nil
	case len(exact) == 0 && len(partial) == 1:
		return partial[0], nil
	case len(exact) > 1 || len(partial) > 1:
		return api.NavItem{}, fmt.Errorf("-hub %q matches more than one hub; use the full name or the id:\n%s", want, list())
	}
	return api.NavItem{}, fmt.Errorf("no hub matches %q; this account sees:\n%s", want, list())
}

// newTokenSource hands out the cached access token, refreshing it when
// it is about to expire or was just rejected. A crawl of a large hub
// outlives one access token, so this is called before every request.
func newTokenSource(cfg *config.Config) audit.TokenSource {
	var (
		mu sync.Mutex
		td *auth.TokenData
	)
	return func(ctx context.Context, force bool) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if td == nil {
			loaded, err := auth.LoadTokens()
			if err != nil {
				return "", err
			}
			td = loaded
		}
		if td.Valid() && !force {
			return td.AccessToken, nil
		}
		if td != nil && td.RefreshToken != "" {
			rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			refreshed, err := auth.Refresh(rctx, cfg.ClientID, cfg.ClientSecret, td.RefreshToken)
			cancel()
			if err == nil {
				td = refreshed
				return td.AccessToken, nil
			}
			if td.AccessToken != "" && !force && time.Now().Before(td.ExpiresAt) {
				// Refresh failed but the token has a few seconds left.
				return td.AccessToken, nil
			}
		}
		// No usable login: sign in through the browser, as the
		// interactive app does on first run.
		fmt.Fprintln(os.Stderr, "Opening the browser to sign in to Autodesk…")
		lctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		fresh, err := auth.Login(lctx, cfg.ClientID, cfg.ClientSecret)
		if err != nil {
			return "", err
		}
		td = fresh
		return td.AccessToken, nil
	}
}

// slug turns a hub name into something safe for a file name.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "hub"
	}
	return out
}
