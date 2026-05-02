package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// STEP derivative status values returned by the Manufacturing Data Model API.
const (
	StepStatusPending = "PENDING"
	StepStatusSuccess = "SUCCESS"
	StepStatusFailed  = "FAILED"
)

// RequestSTEPDerivative asks the Manufacturing Data Model API to generate
// (or report progress on) a STEP derivative for the given component
// version. The same query both kicks off generation (the first call) and
// reports current status thereafter — APS keeps the work going between
// calls, so callers should poll until status is SUCCESS or FAILED.
//
// Returns the derivative status, the signed download URL once status is
// SUCCESS (empty otherwise), and any transport / parse error.
func RequestSTEPDerivative(ctx context.Context, token, componentVersionID string) (status, signedURL string, err error) {
	const q = `
		query GetGeometry($componentVersionId: ID!) {
			componentVersion(componentVersionId: $componentVersionId) {
				derivatives(derivativeInput: {outputFormat: STEP, generate: true}) {
					expires
					signedUrl
					status
					outputFormat
				}
			}
		}`

	data, err := gqlQuery(ctx, token, q, map[string]any{"componentVersionId": componentVersionID})
	if err != nil {
		return "", "", fmt.Errorf("step derivative: %w", err)
	}

	var raw struct {
		ComponentVersion struct {
			Derivatives []struct {
				Status       string `json:"status"`
				SignedURL    string `json:"signedUrl"`
				OutputFormat string `json:"outputFormat"`
			} `json:"derivatives"`
		} `json:"componentVersion"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", "", fmt.Errorf("step derivative decode: %w", err)
	}
	if len(raw.ComponentVersion.Derivatives) == 0 {
		return "", "", fmt.Errorf("no STEP derivative returned")
	}
	d := raw.ComponentVersion.Derivatives[0]
	return strings.ToUpper(d.Status), d.SignedURL, nil
}

// DownloadFile streams an HTTP GET response to destPath. APS signed URLs
// are self-authenticated (the signature is embedded in the URL itself), so
// the user's bearer token is intentionally NOT attached to this request.
// If a poisoned or MITM'd GraphQL response ever returned a non-Autodesk
// URL, sending the bearer would leak the user's APS access token to the
// attacker — withholding it confines the blast radius to the (already
// untrusted) signed URL itself.
func DownloadFile(ctx context.Context, url, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("download HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	f, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return nil
}

// userHomeDir and nowFunc are package vars so tests can inject a temp
// directory and a fixed time for deterministic StepDownloadPath output.
// Production code uses the stdlib defaults.
var (
	userHomeDir = os.UserHomeDir
	nowFunc     = time.Now
)

// StepDownloadPath returns a sensible local destination for a STEP file
// derived from name. Prefers ~/Downloads, falling back to the OS temp dir
// if the home directory cannot be determined. A timestamp suffix avoids
// clobbering prior exports of the same design.
func StepDownloadPath(name string) string {
	safe := sanitizeFilename(name)
	if safe == "" {
		safe = "design"
	}
	fname := fmt.Sprintf("%s-%s.stp", safe, nowFunc().Format("20060102-150405"))
	if home, err := userHomeDir(); err == nil && home != "" {
		return filepath.Join(home, "Downloads", fname)
	}
	return filepath.Join(os.TempDir(), fname)
}

// sanitizeFilename keeps a conservative whitelist of characters safe on
// every supported OS (Linux, macOS, Windows).
func sanitizeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == ' ', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return strings.TrimSpace(b.String())
}
