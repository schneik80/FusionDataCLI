package auth

import (
	"context"
	"fmt"
	"net"
	"net/http"
)

const (
	callbackPort = 7879
	// CallbackURL is the redirect URI that must be registered in your APS app settings.
	CallbackURL = "http://localhost:7879/callback"
)

// WaitForCallback starts a local HTTP server on localhost:7879, waits for the
// OAuth redirect, and returns the authorization code.
func WaitForCallback(ctx context.Context) (string, error) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	srv := &http.Server{Handler: mux}

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if e := r.URL.Query().Get("error"); e != "" {
			desc := r.URL.Query().Get("error_description")
			fmt.Fprintf(w, "<html><body><h2>Authentication failed</h2><p>%s: %s</p><p>You can close this window.</p></body></html>", e, desc)
			errCh <- fmt.Errorf("oauth error: %s — %s", e, desc)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			fmt.Fprintf(w, "<html><body><h2>Authentication failed</h2><p>No code received.</p><p>You can close this window.</p></body></html>")
			errCh <- fmt.Errorf("no authorization code in callback")
			return
		}
		fmt.Fprintf(w, "<html><body><h2>Authentication successful!</h2><p>Return to your terminal — you can close this window.</p></body></html>")
		codeCh <- code
	})

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", callbackPort))
	if err != nil {
		return "", fmt.Errorf("cannot start callback server on port %d: %w\n(Is another instance already running?)", callbackPort, err)
	}

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			select {
			case errCh <- err:
			default:
			}
		}
	}()
	defer srv.Shutdown(context.Background()) //nolint:errcheck

	select {
	case code := <-codeCh:
		return code, nil
	case err := <-errCh:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}
