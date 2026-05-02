package auth

import (
	"net/url"
	"testing"
)

func TestNewVerifier_Length(t *testing.T) {
	v, err := newVerifier()
	if err != nil {
		t.Fatalf("newVerifier() returned error: %v", err)
	}
	if got, want := len(v), 86; got != want {
		t.Errorf("newVerifier() length = %d, want %d (verifier=%q)", got, want, v)
	}
}

func TestNewVerifier_Uniqueness(t *testing.T) {
	const n = 100
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		v, err := newVerifier()
		if err != nil {
			t.Fatalf("newVerifier() iteration %d returned error: %v", i, err)
		}
		if seen[v] {
			t.Fatalf("newVerifier() returned duplicate value at iteration %d: %q", i, v)
		}
		seen[v] = true
	}
	if len(seen) != n {
		t.Errorf("expected %d unique verifiers, got %d", n, len(seen))
	}
}

func TestVerifierToChallenge_RFCExample(t *testing.T) {
	// RFC 7636 Appendix B test vector.
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const expected = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := verifierToChallenge(verifier); got != expected {
		t.Errorf("verifierToChallenge(%q) = %q, want %q", verifier, got, expected)
	}
}

func TestBuildAuthURL_Shape(t *testing.T) {
	raw := buildAuthURL("my-client-id", "my-challenge")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q) returned error: %v", raw, err)
	}

	if got, want := u.Host, "developer.api.autodesk.com"; got != want {
		t.Errorf("host = %q, want %q", got, want)
	}
	if got, want := u.Path, "/authentication/v2/authorize"; got != want {
		t.Errorf("path = %q, want %q", got, want)
	}

	q := u.Query()
	wantParams := map[string]string{
		"client_id":             "my-client-id",
		"response_type":         "code",
		"redirect_uri":          CallbackURL,
		"scope":                 "data:read user-profile:read",
		"code_challenge":        "my-challenge",
		"code_challenge_method": "S256",
	}
	for k, want := range wantParams {
		if got := q.Get(k); got != want {
			t.Errorf("query[%q] = %q, want %q", k, got, want)
		}
	}
}
