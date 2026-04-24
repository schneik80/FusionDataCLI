package vendors

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/schneik80/FusionDataCLI/enrich"
)

// Fastenal (https://www.fastenal.com) — deep-link into their site search.
// Fastenal does have a partner API but it requires a business account; we
// stay link-only for the open tool.
type Fastenal struct{}

func (f *Fastenal) Name() string { return "Fastenal" }

func (f *Fastenal) Search(ctx context.Context, fa *enrich.NormalizedFastener) ([]Candidate, error) {
	if fa == nil {
		return nil, nil
	}
	keyword := buildFastenalKeyword(fa)
	if keyword == "" {
		return nil, nil
	}
	// Real Fastenal search URL shape (observed 2026-04):
	//   /product/Fasteners?query=<keyword>&sortBy=Best%20Match
	// Their search is case-insensitive and wants thread+length fused
	// ("M22x270") rather than separated ("M22 x 270") or spaced ("M22 270").
	u := fmt.Sprintf(
		"https://www.fastenal.com/product/Fasteners?query=%s&sortBy=Best%%20Match",
		url.QueryEscape(keyword),
	)

	reasons := []string{"link:search"}
	if fa.Info != nil {
		reasons = append(reasons, "std:"+fa.Info.Type)
	}
	return []Candidate{{
		Vendor:       f.Name(),
		URL:          u,
		Score:        0.5,
		MatchReasons: reasons,
	}}, nil
}

// SKUSearchURL returns the canonical Fastenal URL that opens a product
// detail page for a SKU. The `fsi=1` parameter tells their search to treat
// the query as a Fastenal SKU / item number rather than a free-text term.
//
//	39101 → https://www.fastenal.com/product?query=39101&fsi=1
func (f *Fastenal) SKUSearchURL(sku string) string {
	return fmt.Sprintf(
		"https://www.fastenal.com/product?query=%s&fsi=1",
		url.QueryEscape(strings.TrimSpace(sku)),
	)
}

// SKULookup is the result of a FetchBySKU call. Fields are best-effort —
// any field may be empty if Fastenal blocks the fetch (403) or their HTML
// shape changes. Callers should treat `Error` as "fall back to manual entry,
// show the URL to the user so they can copy fields themselves".
type SKULookup struct {
	SKU          string
	URL          string
	PartNumber   string  // Fastenal SKU (echoed) or manufacturer PN if different
	Manufacturer string
	Description  string
	Price        float64
	Currency     string
	Package      string // e.g. "EACH", "100 per box"
	RawBody      string // set only when ErrDetails is non-empty, for debugging
	Error        string // empty on success; set on 403 or parse miss
}

// FetchBySKU tries to fetch a product page for a Fastenal SKU and scrape
// its visible fields. Fastenal's edge (Akamai) blocks automated clients
// with a hard 403 on most paths, so this method is expected to fail in
// production and the caller should surface the SKU URL for the user to
// open in their own browser.
//
// Implementation tries the /product?query=<SKU>&fsi=1 redirect path first
// since that URL is canonical for SKU lookup. If the redirect lands on a
// product page we can reach, we scrape JSON-LD or common meta tags.
func (f *Fastenal) FetchBySKU(ctx context.Context, sku string) (*SKULookup, error) {
	sku = strings.TrimSpace(sku)
	if sku == "" {
		return nil, fmt.Errorf("empty sku")
	}
	out := &SKULookup{SKU: sku, URL: f.SKUSearchURL(sku)}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, out.URL, nil)
	if err != nil {
		return out, err
	}
	// Browser-like headers to reduce (but not eliminate) edge blocking.
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		out.Error = err.Error()
		return out, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		out.Error = fmt.Sprintf("Fastenal returned HTTP %d (edge-blocked likely). Open the URL in your browser and copy fields manually.", resp.StatusCode)
		return out, nil
	}

	body := string(raw)
	parseFastenalProduct(out, body)
	if out.Description == "" && out.Manufacturer == "" && out.Price == 0 {
		out.Error = "Could not parse product fields from the page — Fastenal may have changed its markup. Open the URL and copy manually."
		if len(body) > 1500 {
			body = body[:1500]
		}
		out.RawBody = body
	}
	return out, nil
}

// parseFastenalProduct extracts what it can from a Fastenal product page.
// Best-effort regex against common patterns (JSON-LD, og: meta tags) — this
// WILL need adjustment over time, but keeping it in one small function makes
// that adjustment straightforward.
func parseFastenalProduct(out *SKULookup, html string) {
	// JSON-LD product blocks — single source of truth for schema.org Product.
	jsonldRE := regexp.MustCompile(`(?is)<script[^>]+application/ld\+json[^>]*>(.*?)</script>`)
	for _, m := range jsonldRE.FindAllStringSubmatch(html, -1) {
		blob := m[1]
		if !strings.Contains(blob, `"Product"`) && !strings.Contains(blob, `"@type"`) {
			continue
		}
		if v := firstGroup(blob, `"name"\s*:\s*"([^"]+)"`); v != "" && out.Description == "" {
			out.Description = v
		}
		if v := firstGroup(blob, `"sku"\s*:\s*"([^"]+)"`); v != "" && out.PartNumber == "" {
			out.PartNumber = v
		}
		if v := firstGroup(blob, `"brand"\s*:\s*(?:"([^"]+)"|\{[^}]*"name"\s*:\s*"([^"]+)")`); v != "" && out.Manufacturer == "" {
			out.Manufacturer = v
		}
		if v := firstGroup(blob, `"price"\s*:\s*"?([\d.]+)"?`); v != "" && out.Price == 0 {
			_, _ = fmt.Sscan(v, &out.Price)
		}
		if v := firstGroup(blob, `"priceCurrency"\s*:\s*"([A-Z]{3})"`); v != "" && out.Currency == "" {
			out.Currency = v
		}
	}
	// Open Graph meta fallbacks when JSON-LD is absent.
	if out.Description == "" {
		out.Description = firstGroup(html, `<meta[^>]+property="og:title"[^>]+content="([^"]+)"`)
	}
	if out.Price == 0 {
		if v := firstGroup(html, `<meta[^>]+property="product:price:amount"[^>]+content="([^"]+)"`); v != "" {
			_, _ = fmt.Sscan(v, &out.Price)
		}
	}
	if out.Currency == "" {
		out.Currency = firstGroup(html, `<meta[^>]+property="product:price:currency"[^>]+content="([A-Z]{3})"`)
	}
	// Package / pack-size hint (Fastenal-specific).
	if out.Package == "" {
		out.Package = firstGroup(html, `(?i)(?:Pack|Package)(?:\s+Qty)?\s*:\s*</[^>]+>\s*<[^>]+>\s*([A-Za-z0-9 /]+)`)
	}
}

func firstGroup(s, pattern string) string {
	m := regexp.MustCompile(pattern).FindStringSubmatch(s)
	for i := 1; i < len(m); i++ {
		if m[i] != "" {
			return strings.TrimSpace(m[i])
		}
	}
	return ""
}

// buildFastenalKeyword produces the exact format Fastenal's indexer matches:
// lowercase standard identifier, then thread+length tightly fused with an
// internal "x" (e.g. "M22x270"). Nuts + washers omit the length.
//
//	DIN 933, M22, 270  → "din 933 M22x270"
//	DIN 934, M20x2.5   → "din 934 M20x2.5"   (pitch already in Thread)
//	ISO 7089, Size=3   → "iso 7089 3"
func buildFastenalKeyword(f *enrich.NormalizedFastener) string {
	parts := []string{strings.ToLower(f.Standard)}
	switch {
	case f.Thread != "" && f.Length > 0:
		parts = append(parts, fmt.Sprintf("%sx%d", f.Thread, f.Length))
	case f.Thread != "":
		parts = append(parts, f.Thread)
	case f.Size > 0:
		parts = append(parts, fmt.Sprintf("%g", f.Size))
	}
	return strings.Join(parts, " ")
}
