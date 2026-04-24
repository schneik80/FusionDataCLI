package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/schneik80/FusionDataCLI/api"
	"github.com/schneik80/FusionDataCLI/enrich"
	"github.com/schneik80/FusionDataCLI/enrich/vendors"
)

// registerRoutes wires every URL. HTMX-friendly design: each POST /items/...
// returns a fragment, not a full page, so the client can swap in place.
func (a *App) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/", a.handleRoot)
	mux.HandleFunc("/project", a.handleProject)
	mux.HandleFunc("/items/", a.handleItem) // matches /items/{id}/{action}
	mux.HandleFunc("/ingest", a.handleIngest)
}

// handleIngest receives data from the browser bookmarklet. Two modes:
//
//   GET /ingest?data=<url-encoded-JSON>  — used by the bookmarklet; opens
//     in a new tab, renders a confirmation page. This is the preferred
//     path because Chrome blocks cross-protocol fetch (HTTPS → HTTP) as
//     mixed content, but navigation via window.open is allowed.
//
//   POST /ingest with JSON body           — reserved for future use from
//     same-origin scripts that don't need cross-protocol tolerance.
//
// Either way, the payload becomes the server's "last scrape" which the
// Fill-from-SKU flow then consumes.
func (a *App) handleIngest(w http.ResponseWriter, r *http.Request) {
	var p IngestedScrape
	switch r.Method {
	case http.MethodGet:
		raw := r.URL.Query().Get("data")
		if raw == "" {
			http.Error(w, "missing ?data= param", http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			http.Error(w, "bad JSON in ?data: "+err.Error(), http.StatusBadRequest)
			return
		}
	case http.MethodPost:
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}

	a.StoreScrape(p)
	log.Printf("ingest: sku=%s manufacturer=%q price=%.2f url=%s", p.SKU, p.Manufacturer, p.Price, p.URL)

	// GET responses render a tiny self-closing confirmation page.
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, ingestConfirmPage, p.SKU, p.Manufacturer, p.Price, p.Currency)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ingestConfirmPage is the minimal HTML rendered after a bookmarklet
// capture, matching the Fusion dark theme and auto-closing after 2s.
const ingestConfirmPage = `<!DOCTYPE html>
<html><head><meta charset="utf-8"><title>Captured</title>
<style>
body{background:#2A3442;color:#fff;font:14px/1.5 -apple-system,sans-serif;padding:40px;text-align:center;margin:0;}
.card{background:#323E50;border:1px solid #4a5568;border-left:3px solid #0696d7;border-radius:6px;padding:24px 32px;max-width:420px;margin:80px auto;box-shadow:0 4px 16px rgba(0,0,0,0.4);}
h1{font-size:16px;color:#0696d7;margin:0 0 12px;letter-spacing:0.3px;}
dl{display:grid;grid-template-columns:auto 1fr;gap:6px 16px;margin:16px 0;text-align:left;font-size:13px;}
dt{color:#a0aec0;font-size:11px;text-transform:uppercase;letter-spacing:0.4px;align-self:center;}
dd{margin:0;font-family:'SF Mono',Menlo,monospace;}
p{color:#a0aec0;font-size:12px;margin:16px 0 0;}
</style></head><body>
<div class="card">
  <h1>✓ Captured from Fastenal</h1>
  <dl>
    <dt>SKU</dt><dd>%s</dd>
    <dt>Manufacturer</dt><dd>%s</dd>
    <dt>Price</dt><dd>%.2f %s</dd>
  </dl>
  <p>Return to the Enrichment tab and click <strong>Fill from SKU</strong>. This tab will close.</p>
</div>
<script>setTimeout(function(){window.close();},2000);</script>
</body></html>`

// ---------------------------------------------------------------------------
// GET /
// ---------------------------------------------------------------------------

func (a *App) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/project", http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// GET /project — top-level review list
// ---------------------------------------------------------------------------

func (a *App) handleProject(w http.ResponseWriter, r *http.Request) {
	// Walk the whole project tree (items at root + recursively in folders).
	// Library-style projects like Standard Components usually have zero items
	// at project root and everything nested in folders by fastener type.
	items, err := api.V3WalkProjectItems(r.Context(), a.Token(), a.hubID, a.projectID)
	if err != nil && len(items) == 0 {
		httpError(w, "walk project", err)
		return
	}
	if err != nil {
		log.Printf("walk project: partial data — %v", err)
	}

	counts := map[string]int{}
	for _, it := range items {
		counts[it.Typename]++
	}
	log.Printf("project %s: %d total items, by typename: %v", a.projectID, len(items), counts)

	// Filter to what looks like a design (anything with "Design" in the
	// typename — more forgiving than a strict equality check so we pick up
	// DesignItem / ConfiguredDesignItem / similar v3 variants). Drawings are
	// still skipped.
	var designs []api.V3Item
	for _, it := range items {
		if it.Typename == "DrawingItem" {
			continue
		}
		designs = append(designs, it)
	}
	sort.Slice(designs, func(i, j int) bool { return designs[i].Name < designs[j].Name })

	data := projectView{
		Version:         a.version,
		HubID:           a.hubID,
		ProjectID:       a.projectID,
		Items:           designs,
		DefCount:        len(a.defs),
		BookmarkletLink: bookmarkletAnchor("↳ Fasteners Enrich (drag to bookmarks bar)", "bookmarklet-install"),
	}
	a.render(w, "project.html", data)
}

// ---------------------------------------------------------------------------
// /items/{id}/{action}  —  analyze | accept
// ---------------------------------------------------------------------------

func (a *App) handleItem(w http.ResponseWriter, r *http.Request) {
	// /items/{id}/{action} — id may contain URL-encoded slashes from URNs.
	path := strings.TrimPrefix(r.URL.Path, "/items/")
	if path == "" || path == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	// Split on the LAST slash so URNs (which contain colons but not slashes)
	// round-trip cleanly.
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		http.NotFound(w, r)
		return
	}
	itemID := path[:idx]
	action := path[idx+1:]

	switch action {
	case "analyze":
		a.analyzeItem(w, r, itemID)
	case "accept":
		a.acceptItem(w, r, itemID)
	case "status":
		a.itemStatus(w, r, itemID)
	case "sku-lookup":
		a.skuLookup(w, r, itemID)
	default:
		http.NotFound(w, r)
	}
}

// skuLookup: user has pasted a Fastenal SKU and wants us to pre-fill the
// remaining review fields. Preference order:
//  1. Bookmarklet ingest (if the user has clicked the bookmarklet on a
//     Fastenal product page and its SKU matches what was pasted here, OR
//     if the SKU field is empty and an ingest is recent).
//  2. Server-side HTTP scrape (blocked at Fastenal's Akamai edge in
//     production — usually returns an error + link).
//  3. Graceful fallback — empty fields, link to open the product manually.
func (a *App) skuLookup(w http.ResponseWriter, r *http.Request, itemID string) {
	if err := r.ParseForm(); err != nil {
		httpError(w, "parse form", err)
		return
	}
	sku := strings.TrimSpace(r.FormValue("sku"))
	fast := &vendors.Fastenal{}

	// 1. Check for a recent bookmarklet ingest. If SKU was pasted, require
	// a match; if not, use whatever the latest ingest has.
	var res *vendors.SKULookup
	if ing := a.LastScrape(); ing != nil {
		if sku == "" || sku == ing.SKU {
			if sku == "" {
				sku = ing.SKU
			}
			res = &vendors.SKULookup{
				SKU:          sku,
				URL:          ing.URL,
				Manufacturer: ing.Manufacturer,
				Description:  ing.Description,
				Price:        ing.Price,
				Currency:     ing.Currency,
				Package:      ing.Package,
			}
		}
	}

	if sku == "" {
		fmt.Fprint(w, `<span class="status-warn">No capture yet. Click the Fasteners Enrich bookmark on a Fastenal product page first, then come back and click Apply.</span>`)
		return
	}

	// 2. Fall back to scrape when no ingest matched.
	if res == nil {
		res, _ = fast.FetchBySKU(r.Context(), sku)
		if res == nil {
			res = &vendors.SKULookup{SKU: sku, URL: fast.SKUSearchURL(sku)}
		}
	}

	// Merge scraped results into a fresh proposed-writes map so the user
	// can review the full seven-field form with what we found pre-filled.
	// Manufacturer Part Number is always the SKU (that's what they wrote).
	compProps, err := api.V3GetComponentProperties(r.Context(), a.Token(), a.hubID, itemID)
	if err != nil && compProps == nil {
		httpError(w, "read component", err)
		return
	}
	proposed := map[string]string{
		"Vendor":                   "Fastenal",
		"Manufacturer Part Number": sku,
	}
	if res.Manufacturer != "" {
		proposed["Manufacturer"] = res.Manufacturer
	} else {
		proposed["Manufacturer"] = "Fastenal"
	}
	if res.Description != "" {
		proposed["Category"] = res.Description
	}
	if res.Price > 0 {
		proposed["Estimated Cost"] = fmt.Sprintf("%.4f", res.Price)
	}
	if res.Package != "" {
		proposed["Package Type"] = res.Package
	}

	data := skuLookupView{
		ItemID:      itemID,
		ComponentID: compProps.ComponentID,
		SKU:         sku,
		Lookup:      res,
		Current:     compProps.Properties,
		Proposed:    proposed,
		Targets:     enrichmentTargets,
		Definitions: a.defs,
	}
	a.renderFragment(w, "sku_result.html", data)
}

type skuLookupView struct {
	ItemID      string
	ComponentID string
	SKU         string
	Lookup      *vendors.SKULookup
	Current     []api.V3Property
	Proposed    map[string]string
	Targets     []string
	Definitions map[string]api.V3PropertyDefinition
}

// itemStatus: lazy-loaded per-row badge. Counts how many of the seven target
// built-ins have non-empty values and returns an HTML pill classifying the
// item as unenriched / partial / enriched.
func (a *App) itemStatus(w http.ResponseWriter, r *http.Request, itemID string) {
	props, err := api.V3GetComponentProperties(r.Context(), a.Token(), a.hubID, itemID)
	if err != nil && props == nil {
		log.Printf("status %s: %v", itemID, err)
		fmt.Fprint(w, `<span class="badge badge-err">failed</span>`)
		return
	}

	total, filled := len(enrichmentTargets), 0
	for _, name := range enrichmentTargets {
		if v := lookupProp(props.Properties, name); v != "" {
			filled++
		}
	}
	class, label := "badge-unenriched", "unenriched"
	switch {
	case filled == total:
		class, label = "badge-enriched", fmt.Sprintf("enriched %d/%d", filled, total)
	case filled > 0:
		class, label = "badge-partial", fmt.Sprintf("partial %d/%d", filled, total)
	}
	fmt.Fprintf(w, `<span class="badge %s">%s</span>`, class, label)
}

// enrichmentTargets mirrors the seven target property names we write back.
// Keep in sync with proposedWrites() and the client-side button flow.
var enrichmentTargets = []string{
	"Category",
	"Estimated Cost",
	"Manufacturer",
	"Manufacturer Part Number",
	"Package Type",
	"Stock Number",
	"Vendor",
}

// analyzeItem: read current component props, parse the description, and
// render a candidate block. No vendor search yet — the candidate is a
// self-derived suggestion built from the parsed spec.
func (a *App) analyzeItem(w http.ResponseWriter, r *http.Request, itemID string) {
	log.Printf("analyze: item=%s", itemID)
	props, err := api.V3GetComponentProperties(r.Context(), a.Token(), a.hubID, itemID)
	if err != nil {
		log.Printf("analyze: err=%v  props_nil=%t", err, props == nil)
	}
	if err != nil && props == nil {
		httpError(w, "read component", err)
		return
	}
	log.Printf("analyze: componentId=%s name=%q desc=%q partNumber=%q material=%q baseProps=%d",
		props.ComponentID, props.Name, props.Description, props.PartNumber, props.Material, len(props.Properties))

	// Description priority: component.description → component.name → item name
	// (passed via hx-vals from the row, since library items put the full
	// fastener description in the file name, not the component's description
	// field).
	description := props.Description
	if description == "" {
		description = props.Name
	}
	if description == "" {
		_ = r.ParseForm()
		description = r.FormValue("name")
	}
	log.Printf("analyze: using description=%q", description)

	var parseErr string
	parsed, perr := enrich.Parse(description)
	if perr != nil {
		parseErr = perr.Error()
	}

	// Fastenal-only — one deep-link candidate the user clicks to find the
	// SKU, then pastes back into the SKU input below.
	var vendorCands []vendors.Candidate
	if parsed != nil && parsed.Info != nil {
		vendorCands = vendors.SearchAll(r.Context(), parsed)
	}

	// Build proposed writes — parser-derived only; SKU lookup fills the
	// remaining fields separately via its own endpoint.
	proposed := proposedWrites(parsed, a.preferredVendor, nil)

	data := candidatesView{
		ItemID:      itemID,
		ComponentID: props.ComponentID,
		Description: description,
		Parsed:      parsed,
		ParseError:  parseErr,
		Current:     props.Properties,
		Proposed:    proposed,
		Targets:     enrichmentTargets,
		Definitions: a.defs,
		Vendors:     vendorCands,
	}
	a.renderFragment(w, "candidates.html", data)
}

// acceptItem: run setProperties with the proposed writes for this item.
// Body is form-encoded; each `prop_<name>=value` pair represents a field.
func (a *App) acceptItem(w http.ResponseWriter, r *http.Request, itemID string) {
	if err := r.ParseForm(); err != nil {
		httpError(w, "parse form", err)
		return
	}
	componentID := r.FormValue("component_id")
	if componentID == "" {
		httpError(w, "missing component_id", nil)
		return
	}

	var inputs []api.V3PropertyInput
	for key, vals := range r.Form {
		if !strings.HasPrefix(key, "prop_") || len(vals) == 0 || vals[0] == "" {
			continue
		}
		name := strings.TrimPrefix(key, "prop_")
		def, ok := a.defs[name]
		if !ok || def.ReadOnly {
			continue
		}
		inputs = append(inputs, api.V3PropertyInput{
			PropertyDefinitionID: def.ID,
			Value:                vals[0],
		})
	}

	if len(inputs) == 0 {
		fmt.Fprintln(w, `<span class="status-warn">nothing to write</span>`)
		return
	}
	if err := api.V3SetComponentProperties(r.Context(), a.Token(), componentID, inputs); err != nil {
		httpError(w, "setProperties", err)
		return
	}
	// Show success briefly, then auto-collapse the candidates panel by
	// re-fetching the row's status badge (which now shows "enriched N/7").
	d := domID(itemID)
	fmt.Fprintf(w, `<span class="status-ok">✓ wrote %d properties</span>`+
		`<span hx-get="/items/%s/status" hx-target="#status-%s" hx-swap="innerHTML" hx-trigger="load delay:1500ms"></span>`,
		len(inputs), itemID, d)
}

// domID mirrors the template function of the same name — sanitizes a URN
// for use in an HTML id / CSS selector. Keep the two implementations
// aligned; they're intentionally separate so handler code doesn't reach
// into template internals.
func domID(s string) string {
	return strings.NewReplacer(":", "_", ".", "_", "~", "_", "/", "_").Replace(s)
}

// ---------------------------------------------------------------------------
// View models
// ---------------------------------------------------------------------------

type projectView struct {
	Version         string
	HubID           string
	ProjectID       string
	Items           []api.V3Item
	DefCount        int
	BookmarkletLink template.HTML // full <a> element — template.HTML bypasses URL sanitization
}

type candidatesView struct {
	ItemID      string
	ComponentID string
	Description string
	Parsed      *enrich.NormalizedFastener
	ParseError  string
	Current     []api.V3Property
	Proposed    map[string]string // fields we could auto-fill; others blank
	Targets     []string          // all seven target names, in display order
	Definitions map[string]api.V3PropertyDefinition
	Vendors     []vendors.Candidate
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// proposedWrites builds suggested values for the seven target properties.
// Each target may be filled or left blank — blanks mean "we can't infer this
// automatically; user fills manually".
//
// Sources, in priority order:
//  1. The highest-scored API candidate with a real part number (Digi-Key etc.)
//     fills Manufacturer, Manufacturer Part Number, Estimated Cost, and
//     (if the vendor carries packaging info) lays groundwork for Package Type.
//  2. The parsed fastener fills Category.
//  3. The preferred-vendor config fills Vendor (and Manufacturer when no API
//     match exists).
//
// Remaining blanks today: Package Type, Stock Number (internal).
func proposedWrites(f *enrich.NormalizedFastener, preferredVendor string, cands []vendors.Candidate) map[string]string {
	out := map[string]string{}
	if f == nil || f.Info == nil {
		return out
	}
	out["Category"] = fmt.Sprintf("Fastener / %s", f.Info.DisplayName)

	// Look for the best API candidate — one with a real part number.
	var best *vendors.Candidate
	for i := range cands {
		c := &cands[i]
		if c.PartNumber == "" {
			continue
		}
		if best == nil || c.Score > best.Score {
			best = c
		}
	}

	if best != nil {
		out["Manufacturer"] = best.Manufacturer
		out["Manufacturer Part Number"] = best.PartNumber
		out["Vendor"] = best.Vendor
		if best.Price > 0 {
			out["Estimated Cost"] = fmt.Sprintf("%.4f", best.Price)
		}
	} else {
		// No API hit — fall back to synthetic PN placeholder + config defaults.
		if f.Length > 0 {
			out["Manufacturer Part Number"] = fmt.Sprintf("%s-%s-%d", f.Info.Type, f.Thread, f.Length)
		} else {
			out["Manufacturer Part Number"] = fmt.Sprintf("%s-%s", f.Info.Type, f.Thread)
		}
		if preferredVendor != "" {
			out["Vendor"] = preferredVendor
			out["Manufacturer"] = preferredVendor
		}
	}
	return out
}

func lookupProp(ps []api.V3Property, name string) string {
	for _, p := range ps {
		if p.Name == name {
			if s, ok := p.Value.(string); ok {
				return s
			}
			if p.DisplayValue != "" {
				return p.DisplayValue
			}
		}
	}
	return ""
}

// render executes a full-page template.
func (a *App) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.tpl.ExecuteTemplate(w, name, data); err != nil {
		httpError(w, "render "+name, err)
	}
}

// renderFragment executes a template without the layout — used for HTMX swaps.
func (a *App) renderFragment(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.tpl.ExecuteTemplate(w, name, data); err != nil {
		httpError(w, "render "+name, err)
	}
}

func httpError(w http.ResponseWriter, ctx string, err error) {
	if err == nil {
		http.Error(w, ctx, http.StatusBadRequest)
		return
	}
	msg := fmt.Sprintf("%s: %s", ctx, err.Error())
	http.Error(w, msg, http.StatusInternalServerError)
}
