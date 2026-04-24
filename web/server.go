// Package web implements the `fusiondatacli serve` subcommand — a local
// HTTP server that renders an HTMX review UI for the Fasteners Enrichment
// workflow, scoped to one Autodesk project.
//
// All API calls go through the v3 client in `github.com/schneik80/FusionDataCLI/api`.
// Fastener parsing + standards lookup come from `github.com/schneik80/FusionDataCLI/enrich`.
// Templates are embedded so the binary is self-contained.
package web

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/schneik80/FusionDataCLI/api"
	"github.com/schneik80/FusionDataCLI/auth"
	"github.com/schneik80/FusionDataCLI/config"
)

//go:embed templates/*.html
var templatesFS embed.FS

// Defaults pin to the ADSK-Schneik / Standard Components scope described in
// the project memory. Override with -hub-id / -project-id flags.
const (
	defaultHubID     = "a.YnVzaW5lc3M6YXV0b2Rlc2s4MDgz"
	defaultProjectID = "a.YnVzaW5lc3M6YXV0b2Rlc2s4MDgzI0QyMDI1MDgxMjk2NDg1NTU1Ng"
	defaultAddr      = "127.0.0.1:8787"
)

// Run parses flags and starts the HTTP server. Returns when the server stops
// (or a setup error occurs). Called from main() when argv[1] == "serve".
func Run(args []string, version string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", defaultAddr, "bind address")
	hubID := fs.String("hub-id", defaultHubID, "hub ID (Data Management API form or native)")
	projectID := fs.String("project-id", defaultProjectID, "project ID (Data Management API form or native)")
	preferredVendor := fs.String("vendor", "Fastenal", "default vendor name to write into Vendor + Manufacturer when blank")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Load token — same flow the TUI uses. If expired, attempt a refresh.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	token, err := loadToken(ctx)
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}

	// One-time resolution of native hub + project IDs and definition cache.
	fmt.Printf("Resolving hub + project IDs ...\n")
	nativeHubID, err := api.V3ResolveNativeHubID(ctx, token, *hubID)
	if err != nil {
		return fmt.Errorf("resolve hub: %w", err)
	}
	nativeProjectID, err := api.V3ResolveNativeProjectID(ctx, token, *projectID)
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}
	fmt.Printf("  hub:     %s\n  project: %s\n", nativeHubID, nativeProjectID)

	fmt.Printf("Caching property definitions ...\n")
	defs, defErr := api.V3GetHubPropertyDefinitions(ctx, token, nativeHubID)
	if defErr != nil && len(defs) == 0 {
		return fmt.Errorf("load definitions: %w", defErr)
	}
	if defErr != nil {
		fmt.Printf("  warn: %v (continuing with %d defs)\n", defErr, len(defs))
	}
	fmt.Printf("  loaded %d definitions\n", len(defs))

	// Diagnostic: log the argument shapes of the listing resolvers so we can
	// see whether v3 requires hubId (etc.) that v1/v2 didn't.
	if sig, err := api.V3DescribeResolvers(ctx, token, []string{
		"itemsByProject", "foldersByProject", "foldersByFolder", "itemsByFolder",
	}); err == nil {
		for name, args := range sig {
			fmt.Printf("  schema: %s(%s)\n", name, args)
		}
	}

	tpl, err := parseTemplates()
	if err != nil {
		return fmt.Errorf("parse templates: %w", err)
	}

	app := &App{
		version:         version,
		hubID:           nativeHubID,
		projectID:       nativeProjectID,
		defs:            defs,
		tpl:             tpl,
		token:           token,
		preferredVendor: *preferredVendor,
	}

	mux := http.NewServeMux()
	app.registerRoutes(mux)

	fmt.Printf("\nFasteners Enrichment serving on http://%s/\n", *addr)
	fmt.Printf("Press Ctrl+C to stop.\n\n")
	srv := &http.Server{
		Addr:              *addr,
		Handler:           logRequest(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return srv.ListenAndServe()
}

// App is the shared per-server state that each handler closes over.
// Single-token design: we don't refresh during a server session. If the user
// hits 401 mid-session they restart the server (same as the TUI flow).
type App struct {
	version         string
	hubID           string
	projectID       string
	defs            map[string]api.V3PropertyDefinition
	tpl             *template.Template
	preferredVendor string

	mu         sync.RWMutex // guards token + ingestedScrapes below
	token      string
	lastScrape *IngestedScrape // most recent POST from the bookmarklet
}

// IngestedScrape is whatever the bookmarklet POSTs back from a Fastenal
// product page. All fields are best-effort — depends on what the page
// exposes in JSON-LD / meta / URL. The handler consumes this on the next
// Fill-from-SKU click and uses it to pre-fill the review form.
type IngestedScrape struct {
	ReceivedAt   time.Time      `json:"-"`
	URL          string         `json:"url"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	SKU          string         `json:"sku"`
	Manufacturer string         `json:"manufacturer"`
	Price        float64        `json:"price"`
	Currency     string         `json:"currency"`
	Package      string         `json:"package"`
	Diag         map[string]any `json:"_diag,omitempty"` // bookmarklet diagnostic
}

func (a *App) Token() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.token
}

// LastScrape returns a snapshot of the most recent bookmarklet ingest
// (or nil if none received yet).
func (a *App) LastScrape() *IngestedScrape {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.lastScrape == nil {
		return nil
	}
	cp := *a.lastScrape
	return &cp
}

// StoreScrape overwrites the "last scrape" slot with new data from the
// bookmarklet. Single-slot design — if the user works two enrichments in
// parallel they'd confuse themselves, but for a one-at-a-time flow this is
// simpler than per-item keying (the bookmarklet doesn't know the item ID).
func (a *App) StoreScrape(s IngestedScrape) {
	s.ReceivedAt = time.Now()
	a.mu.Lock()
	a.lastScrape = &s
	a.mu.Unlock()
}

// loadToken reuses the TUI's auth flow: load from disk, refresh if expired.
// Missing tokens are a hard error here — user must sign in via the TUI once.
func loadToken(ctx context.Context) (string, error) {
	td, err := auth.LoadTokens()
	if err != nil {
		return "", err
	}
	if td == nil {
		return "", errors.New("no saved token — run `fusiondatacli` (TUI) once to sign in")
	}
	if td.Valid() {
		return td.AccessToken, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}
	refreshed, err := auth.Refresh(ctx, cfg.ClientID, cfg.ClientSecret, td.RefreshToken)
	if err != nil {
		return "", fmt.Errorf("refresh token: %w (token scope may be stale — sign out and back in via TUI)", err)
	}
	return refreshed.AccessToken, nil
}

// parseTemplates loads every template from the embedded FS and returns one
// *Template with all files parsed. Lookup uses base filename (e.g. "project").
func parseTemplates() (*template.Template, error) {
	funcs := template.FuncMap{
		"truncate": truncate,
		"yesno":    func(b bool, y, n string) string { if b { return y }; return n },
		// domID sanitizes a URN into a string safe to use as an HTML id /
		// CSS selector target — URNs contain ":" and "~" which break
		// querySelector-based targeting.
		"domID": func(s string) string {
			return strings.NewReplacer(":", "_", ".", "_", "~", "_", "/", "_").Replace(s)
		},
	}
	return template.New("").Funcs(funcs).ParseFS(templatesFS, "templates/*.html")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// logRequest is a tiny access log — enough to debug HTMX swaps without pulling
// in a router library.
func logRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t0 := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%-6s %s  (%s)", r.Method, r.URL.Path, time.Since(t0).Round(time.Millisecond))
	})
}
