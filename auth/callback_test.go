package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// freePort binds an ephemeral listener on 127.0.0.1, reads the chosen port,
// and closes the listener. There's a brief race window between Close and the
// callback server's rebind, but for a single-process test it's fine.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("ln.Close: %v", err)
	}
	return port
}

// waitForServer polls 127.0.0.1:<port> with a short interval until a TCP
// connection succeeds, or fails the test after ~1s.
func waitForServer(t *testing.T, port int) {
	t.Helper()
	for i := 0; i < 100; i++ {
		c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("callback server didn't come up within 1s")
}

// useFreeCallbackPort swaps callbackPort and CallbackURL to a freshly chosen
// ephemeral port, restoring both on cleanup. Returns the chosen port.
func useFreeCallbackPort(t *testing.T) int {
	t.Helper()
	port := freePort(t)

	prevPort := callbackPort
	prevURL := CallbackURL
	t.Cleanup(func() {
		callbackPort = prevPort
		CallbackURL = prevURL
	})
	callbackPort = port
	CallbackURL = fmt.Sprintf("http://127.0.0.1:%d/callback", port)
	return port
}

func TestWaitForCallback_HappyPath(t *testing.T) {
	port := useFreeCallbackPort(t)

	type result struct {
		code string
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		code, err := WaitForCallback(context.Background())
		resCh <- result{code: code, err: err}
	}()

	waitForServer(t, port)

	resp, err := http.Get(CallbackURL + "?code=abc")
	if err != nil {
		t.Fatalf("http.Get: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "Authentication successful") {
		t.Errorf("body = %q, want substring %q", string(body), "Authentication successful")
	}

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Errorf("WaitForCallback returned error: %v", r.err)
		}
		if r.code != "abc" {
			t.Errorf("code = %q, want %q", r.code, "abc")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForCallback did not return within 2s")
	}
}

func TestWaitForCallback_OAuthError(t *testing.T) {
	port := useFreeCallbackPort(t)

	type result struct {
		code string
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		code, err := WaitForCallback(context.Background())
		resCh <- result{code: code, err: err}
	}()

	waitForServer(t, port)

	// url.QueryEscape("<script>alert(1)</script>") — spelled out so the
	// security intent is obvious in the source.
	const encodedDesc = "%3Cscript%3Ealert%281%29%3C%2Fscript%3E"
	resp, err := http.Get(CallbackURL + "?error=access_denied&error_description=" + encodedDesc)
	if err != nil {
		t.Fatalf("http.Get: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	bodyStr := string(body)
	if !strings.Contains(bodyStr, "&lt;script&gt;") {
		t.Errorf("body does not contain HTML-escaped %q; body=%q", "&lt;script&gt;", bodyStr)
	}
	if strings.Contains(bodyStr, "<script>alert(1)</script>") {
		t.Errorf("body contains unescaped script tag (XSS regression!); body=%q", bodyStr)
	}

	select {
	case r := <-resCh:
		if r.err == nil {
			t.Fatalf("WaitForCallback returned nil error; code=%q", r.code)
		}
		if !strings.Contains(r.err.Error(), "access_denied") {
			t.Errorf("error %q does not contain %q", r.err.Error(), "access_denied")
		}
		if !strings.Contains(r.err.Error(), "<script>alert(1)</script>") {
			// The error message itself isn't HTML-rendered, so the raw
			// description is fine — what matters is the HTTP body escaped it.
			t.Errorf("error %q does not contain raw description %q", r.err.Error(), "<script>alert(1)</script>")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForCallback did not return within 2s")
	}
}

func TestWaitForCallback_NoCode(t *testing.T) {
	port := useFreeCallbackPort(t)

	type result struct {
		code string
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		code, err := WaitForCallback(context.Background())
		resCh <- result{code: code, err: err}
	}()

	waitForServer(t, port)

	resp, err := http.Get(CallbackURL + "?")
	if err != nil {
		t.Fatalf("http.Get: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	select {
	case r := <-resCh:
		if r.err == nil {
			t.Fatalf("WaitForCallback returned nil error; code=%q", r.code)
		}
		if !strings.Contains(r.err.Error(), "no authorization code") {
			t.Errorf("error %q does not contain %q", r.err.Error(), "no authorization code")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForCallback did not return within 2s")
	}
}

func TestWaitForCallback_ContextCancel(t *testing.T) {
	port := useFreeCallbackPort(t)

	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		code string
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		code, err := WaitForCallback(ctx)
		resCh <- result{code: code, err: err}
	}()

	waitForServer(t, port)
	cancel()

	select {
	case r := <-resCh:
		if r.err == nil {
			t.Fatalf("WaitForCallback returned nil error after cancel; code=%q", r.code)
		}
		if !errors.Is(r.err, context.Canceled) {
			t.Errorf("error = %v, want wraps context.Canceled", r.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForCallback did not return within 2s after cancel")
	}
}
