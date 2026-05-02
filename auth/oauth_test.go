package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
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

// swapTokenEndpoint replaces the package-level tokenEndpoint var for the
// duration of the test, restoring it on cleanup.
func swapTokenEndpoint(t *testing.T, url string) {
	t.Helper()
	prev := tokenEndpoint
	t.Cleanup(func() { tokenEndpoint = prev })
	tokenEndpoint = url
}

func TestExchangeCode_PublicClient_PutsClientIDInBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if got, want := r.Header.Get("Content-Type"), "application/x-www-form-urlencoded"; got != want {
			t.Errorf("Content-Type = %q, want %q", got, want)
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorization header = %q, want empty (public client)", got)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		wantParams := map[string]string{
			"client_id":     "pub-client",
			"grant_type":    "authorization_code",
			"code":          "the-code",
			"redirect_uri":  CallbackURL,
			"code_verifier": "the-verifier",
		}
		for k, want := range wantParams {
			if got := r.PostForm.Get(k); got != want {
				t.Errorf("PostForm[%q] = %q, want %q", k, got, want)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token":"AT","refresh_token":"RT","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)
	swapTokenEndpoint(t, srv.URL)

	td, err := exchangeCode(context.Background(), "pub-client", "", "the-code", "the-verifier")
	if err != nil {
		t.Fatalf("exchangeCode returned error: %v", err)
	}
	if td.AccessToken != "AT" {
		t.Errorf("AccessToken = %q, want %q", td.AccessToken, "AT")
	}
	if td.RefreshToken != "RT" {
		t.Errorf("RefreshToken = %q, want %q", td.RefreshToken, "RT")
	}
	delta := time.Until(td.ExpiresAt) - time.Hour
	if delta < -5*time.Second || delta > 5*time.Second {
		t.Errorf("ExpiresAt offset from now = %v, want ~1h (±5s)", time.Until(td.ExpiresAt))
	}
}

func TestExchangeCode_ConfidentialClient_UsesBasicAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok {
			t.Errorf("expected Basic auth header to be present")
		}
		if user != "conf-client" || pass != "secret" {
			t.Errorf("BasicAuth = (%q, %q), want (%q, %q)", user, pass, "conf-client", "secret")
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got := r.PostForm.Get("client_id"); got != "" {
			t.Errorf("PostForm[client_id] = %q, want empty (confidential clients use Basic)", got)
		}
		if got, want := r.PostForm.Get("grant_type"), "authorization_code"; got != want {
			t.Errorf("PostForm[grant_type] = %q, want %q", got, want)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token":"AT","refresh_token":"RT","expires_in":3600}`))
	}))
	t.Cleanup(srv.Close)
	swapTokenEndpoint(t, srv.URL)

	td, err := exchangeCode(context.Background(), "conf-client", "secret", "the-code", "the-verifier")
	if err != nil {
		t.Fatalf("exchangeCode returned error: %v", err)
	}
	if td.AccessToken != "AT" {
		t.Errorf("AccessToken = %q, want %q", td.AccessToken, "AT")
	}
	if td.RefreshToken != "RT" {
		t.Errorf("RefreshToken = %q, want %q", td.RefreshToken, "RT")
	}
}

func TestRefresh_RefreshesAndSaves(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if got, want := r.PostForm.Get("grant_type"), "refresh_token"; got != want {
			t.Errorf("PostForm[grant_type] = %q, want %q", got, want)
		}
		if got, want := r.PostForm.Get("refresh_token"), "old-rt"; got != want {
			t.Errorf("PostForm[refresh_token] = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"access_token":"new-AT","refresh_token":"new-RT","expires_in":600}`))
	}))
	t.Cleanup(srv.Close)
	swapTokenEndpoint(t, srv.URL)

	td, err := Refresh(context.Background(), "pub", "", "old-rt")
	if err != nil {
		t.Fatalf("Refresh returned error: %v", err)
	}
	if td.AccessToken != "new-AT" {
		t.Errorf("AccessToken = %q, want %q", td.AccessToken, "new-AT")
	}
	if td.RefreshToken != "new-RT" {
		t.Errorf("RefreshToken = %q, want %q", td.RefreshToken, "new-RT")
	}

	loaded, err := LoadTokens()
	if err != nil {
		t.Fatalf("LoadTokens returned error: %v", err)
	}
	if loaded == nil {
		t.Fatal("LoadTokens returned nil; expected SaveTokens to have persisted")
	}
	if loaded.AccessToken != td.AccessToken {
		t.Errorf("loaded.AccessToken = %q, want %q", loaded.AccessToken, td.AccessToken)
	}
	if loaded.RefreshToken != td.RefreshToken {
		t.Errorf("loaded.RefreshToken = %q, want %q", loaded.RefreshToken, td.RefreshToken)
	}
	if !loaded.ExpiresAt.Equal(td.ExpiresAt) {
		t.Errorf("loaded.ExpiresAt = %v, want %v", loaded.ExpiresAt, td.ExpiresAt)
	}
}

func TestDoTokenRequest_ErrorPaths(t *testing.T) {
	tests := []struct {
		name        string
		handler     http.HandlerFunc
		useBadURL   bool
		wantSubstrs []string
	}{
		{
			name: "oauth_error_400",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"bad code"}`))
			},
			wantSubstrs: []string{"invalid_grant", "bad code"},
		},
		{
			name: "non_json_500",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("Internal Server Error"))
			},
			wantSubstrs: []string{"500", "Internal Server Error"},
		},
		{
			name: "empty_body_200",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				// no body
			},
			wantSubstrs: []string{"parsing"},
		},
		{
			name:      "network_error",
			useBadURL: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.useBadURL {
				prev := tokenEndpoint
				t.Cleanup(func() { tokenEndpoint = prev })
				tokenEndpoint = "http://127.0.0.1:1"
			} else {
				srv := httptest.NewServer(tc.handler)
				t.Cleanup(srv.Close)
				swapTokenEndpoint(t, srv.URL)
			}

			form := url.Values{}
			form.Set("grant_type", "refresh_token")
			form.Set("refresh_token", "x")

			td, err := doTokenRequest(context.Background(), "id", "", form)
			if err == nil {
				t.Fatalf("doTokenRequest returned nil error; td=%+v", td)
			}
			for _, sub := range tc.wantSubstrs {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q does not contain %q", err.Error(), sub)
				}
			}
		})
	}
}

