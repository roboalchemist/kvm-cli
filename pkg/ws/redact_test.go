package ws

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// captureStderr redirects os.Stderr while fn runs and returns what was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = old
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	return string(data)
}

// TestRedactURLMasksUserInfo is the regression for the ws client: the
// auth_token redactor must also strip URL userinfo.
func TestRedactURLMasksUserInfo(t *testing.T) {
	got := redactURL("wss://user:s3cr3t@h/api/ws?auth_token=secret&stream=1")
	for _, bad := range []string{"s3cr3t", "secret", "user:"} {
		if strings.Contains(got, bad) {
			t.Fatalf("redactURL leaked %q: %q", bad, got)
		}
	}
	if !strings.Contains(got, "wss://***@h/api/ws") {
		t.Fatalf("redactURL did not mask userinfo: %q", got)
	}
	if !strings.Contains(got, "auth_token=***") {
		t.Fatalf("redactURL did not mask auth_token: %q", got)
	}
}

// TestBuildWSURLInvalidBaseRedactsUserInfo verifies the parse error — which
// url.Parse renders with the raw URL — does not leak userinfo.
func TestBuildWSURLInvalidBaseRedactsUserInfo(t *testing.T) {
	_, err := buildWSURL("https://user:s3cr3t@[::1", "tok", true)
	if err == nil {
		t.Fatal("expected an error for the malformed URL")
	}
	if strings.Contains(err.Error(), "s3cr3t") {
		t.Fatalf("buildWSURL error leaked userinfo: %v", err)
	}
}

// TestRestPostDebugRedactsUserInfo verifies the REST debug line (which includes
// the full base URL) masks userinfo.
func TestRestPostDebugRedactsUserInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()

	c := &Client{
		baseURL: strings.Replace(srv.URL, "http://", "http://user:s3cr3t@", 1),
		token:   "tok",
		opts:    Options{Debug: true},
		http:    srv.Client(),
	}
	stderr := captureStderr(t, func() {
		if _, err := c.restPost("/api/hid/print", nil, []byte("hi"), "text/plain"); err != nil {
			t.Fatalf("restPost: %v", err)
		}
	})
	if strings.Contains(stderr, "s3cr3t") {
		t.Fatalf("restPost debug leaked userinfo: %q", stderr)
	}
	if !strings.Contains(stderr, "http://***@") {
		t.Fatalf("restPost debug did not mask the URL: %q", stderr)
	}
}

// TestRestPostTransportErrorRedactsUserInfo covers the transport-error path:
// http.Client.Do returns a *url.Error embedding the full URL (with userinfo).
func TestRestPostTransportErrorRedactsUserInfo(t *testing.T) {
	c := &Client{
		baseURL: "http://user:s3cr3t@127.0.0.1:1",
		token:   "tok",
		http:    &http.Client{Timeout: time.Second},
	}
	_, err := c.restPost("/api/hid/print", nil, []byte("hi"), "text/plain")
	if err == nil {
		t.Fatal("expected a transport error for the dead URL")
	}
	if strings.Contains(err.Error(), "s3cr3t") || strings.Contains(err.Error(), "user:") {
		t.Fatalf("restPost transport error leaked userinfo: %v", err)
	}
}
