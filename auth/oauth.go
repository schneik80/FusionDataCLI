package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	authEndpoint  = "https://developer.api.autodesk.com/authentication/v2/authorize"
	tokenEndpoint = "https://developer.api.autodesk.com/authentication/v2/token"
	authScope     = "data:read user-profile:read"
)

// Login performs the full 3-legged PKCE OAuth flow.
// It opens the system browser, starts a local callback server, exchanges the
// authorization code for tokens, saves them to disk, and returns them.
// clientSecret may be empty for public clients; confidential APS apps require it.
func Login(ctx context.Context, clientID, clientSecret string) (*TokenData, error) {
	verifier, err := newVerifier()
	if err != nil {
		return nil, fmt.Errorf("generating PKCE verifier: %w", err)
	}
	challenge := verifierToChallenge(verifier)

	authURL := buildAuthURL(clientID, challenge)
	if err := OpenBrowser(authURL); err != nil {
		// Non-fatal: user can open manually.
		fmt.Printf("Open this URL in your browser to authenticate:\n\n  %s\n\n", authURL)
	}

	code, err := WaitForCallback(ctx)
	if err != nil {
		return nil, fmt.Errorf("waiting for OAuth callback: %w", err)
	}

	td, err := exchangeCode(ctx, clientID, clientSecret, code, verifier)
	if err != nil {
		return nil, fmt.Errorf("exchanging code for token: %w", err)
	}
	if err := SaveTokens(td); err != nil {
		return nil, fmt.Errorf("saving tokens: %w", err)
	}
	return td, nil
}

// Refresh exchanges a refresh token for a new access token and saves it.
func Refresh(ctx context.Context, clientID, clientSecret, refreshToken string) (*TokenData, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	td, err := doTokenRequest(ctx, clientID, clientSecret, form)
	if err != nil {
		return nil, err
	}
	if err := SaveTokens(td); err != nil {
		return nil, fmt.Errorf("saving refreshed tokens: %w", err)
	}
	return td, nil
}

// OpenBrowser opens url in the default system browser.
func OpenBrowser(u string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", u).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", u).Start()
	default:
		return exec.Command("xdg-open", u).Start()
	}
}

func newVerifier() (string, error) {
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func verifierToChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func buildAuthURL(clientID, challenge string) string {
	p := url.Values{}
	p.Set("client_id", clientID)
	p.Set("response_type", "code")
	p.Set("redirect_uri", CallbackURL)
	p.Set("scope", authScope)
	p.Set("code_challenge", challenge)
	p.Set("code_challenge_method", "S256")
	return authEndpoint + "?" + p.Encode()
}

func exchangeCode(ctx context.Context, clientID, clientSecret, code, verifier string) (*TokenData, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", CallbackURL)
	form.Set("code_verifier", verifier)
	return doTokenRequest(ctx, clientID, clientSecret, form)
}

// doTokenRequest posts to the APS token endpoint, authenticating the client via
// HTTP Basic Auth (client_id:client_secret) when a secret is present, or by
// including client_id in the form body for public clients.
func doTokenRequest(ctx context.Context, clientID, clientSecret string, form url.Values) (*TokenData, error) {
	if clientSecret != "" {
		// Confidential client: authenticate via Basic Auth.
		// Do not include client_id in the body.
	} else {
		// Public client: include client_id in the body.
		form.Set("client_id", clientID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if clientSecret != "" {
		req.SetBasicAuth(clientID, clientSecret)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading token response: %w", err)
	}

	var tr struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		ErrorDesc    string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("parsing token response (HTTP %d): %w\nbody: %s", resp.StatusCode, err, raw)
	}
	if tr.Error != "" {
		return nil, fmt.Errorf("token error %s: %s", tr.Error, tr.ErrorDesc)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token request failed (HTTP %d): %s", resp.StatusCode, raw)
	}
	return &TokenData{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
	}, nil
}
