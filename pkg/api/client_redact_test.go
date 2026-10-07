package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/redact"
)

const Mask = redact.Mask

// TestDebugRedactsSecretQueryParams asserts that secret-bearing query values
// never reach the --debug log.
func TestDebugRedactsSecretQueryParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, true, map[string]any{})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	var logged []string
	c.SetDebug(func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})

	if err := c.Get("/api/ap/enable?enable=true&key=SUPERSECRET", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}

	joined := strings.Join(logged, "\n")
	if strings.Contains(joined, "SUPERSECRET") {
		t.Fatalf("debug log leaked the secret:\n%s", joined)
	}
	if !strings.Contains(joined, "key=***") {
		t.Fatalf("debug log did not mask the secret:\n%s", joined)
	}
	if !strings.Contains(joined, "enable=true") {
		t.Fatalf("debug log dropped the benign parameter:\n%s", joined)
	}
}

// TestTransportErrorRedactsSecretQueryParams asserts that a failed request
// (for example against a closed port) never surfaces the secret in its error
// message.
func TestTransportErrorRedactsSecretQueryParams(t *testing.T) {
	c := NewClient("http://127.0.0.1:1", "admin", "pw", Options{})
	c.maxRetries = 0
	c.SetRateLimit(0)
	defer c.SetDebug(func(string, ...any) {})

	err := c.Get("/api/modem/input_pin_code?pin=SUPERSECRET", nil)
	if err == nil {
		t.Fatal("expected a transport error against a closed port")
	}
	if strings.Contains(err.Error(), "SUPERSECRET") {
		t.Fatalf("transport error leaked the secret: %v", err)
	}
	if !strings.Contains(err.Error(), "pin=***") {
		t.Fatalf("transport error did not mask the secret: %v", err)
	}
}

// TestDecodeResultErrorRedactsBody is the D9 acceptance: when a result fails
// struct decoding, the raw body embedded in the error must not leak
// secret-named fields.
func TestDecodeResultErrorRedactsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, true, map[string]any{
			"success":  true,
			"keyboard": map[string]any{"nested": true}, // wrong type -> decode failure
			"ssl_key":  "SECRET_LEAK",
			"password": "SECRET_LEAK",
			"token":    "SECRET_LEAK",
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	var out struct {
		Success  bool   `json:"success"`
		Keyboard string `json:"keyboard"`
	}
	err := c.Get("/api/hid", &out)
	if err == nil || !strings.Contains(err.Error(), "parse result") {
		t.Fatalf("err = %v, want a parse result error", err)
	}
	if strings.Contains(err.Error(), "SECRET_LEAK") {
		t.Fatalf("decode error leaked a secret:\n%v", err)
	}
	if !strings.Contains(err.Error(), "ssl_key") || !strings.Contains(err.Error(), Mask) {
		t.Fatalf("decode error did not show a masked secret field:\n%v", err)
	}
}

// TestDecodeEnvelopeErrorRedactsBody covers the invalid-envelope path: even a
// malformed body with JSON-style secret pairs must be masked before it is
// embedded in the "parse response" error.
func TestDecodeEnvelopeErrorRedactsBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"ssl_key": "SECRET_LEAK", "password": "SECRET_LEAK"`)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	err := c.Get("/api/info", nil)
	if err == nil || !strings.Contains(err.Error(), "parse response") {
		t.Fatalf("err = %v, want a parse response error", err)
	}
	if strings.Contains(err.Error(), "SECRET_LEAK") {
		t.Fatalf("parse response error leaked a secret:\n%v", err)
	}
}

// TestErrorFromResponseRedactsBody covers the non-envelope HTTP-error path
// (line 367): the truncated body embedded in the message is redacted.
func TestErrorFromResponseRedactsBody(t *testing.T) {
	c := &Client{}

	e := c.errorFromResponse(&response{
		status: http.StatusInternalServerError,
		body:   []byte(`{"ok":true,"ssl_key":"SECRET_LEAK"}`),
	})
	if strings.Contains(e.Message, "SECRET_LEAK") {
		t.Fatalf("errorFromResponse leaked a secret: %q", e.Message)
	}
	if !strings.Contains(e.Message, Mask) {
		t.Fatalf("errorFromResponse did not mask: %q", e.Message)
	}

	// A malformed body is masked best-effort too.
	e = c.errorFromResponse(&response{status: 599, body: []byte(`{"token":"SECRET_LEAK"`)})
	if strings.Contains(e.Message, "SECRET_LEAK") {
		t.Fatalf("errorFromResponse leaked a secret in a malformed body: %q", e.Message)
	}
}

// echoUnmarshaler mimics a custom json.Unmarshaler (such as api.Hostname) that
// embeds the raw payload in its error text. The sentinel proves the client
// preserves errors.Is/errors.As for the wrapped cause.
type echoUnmarshaler struct{ Field string }

var errEchoSentinel = errors.New("echo failure")

func (e *echoUnmarshaler) UnmarshalJSON(data []byte) error {
	return fmt.Errorf("%w: got %s", errEchoSentinel, string(data))
}

// TestDecodeResultErrorRedactsCustomUnmarshalerPayload is the D9 follow-up: a
// custom UnmarshalJSON error (not just the appended result suffix) must be
// redacted, and errors.Is must still reach the wrapped cause.
func TestDecodeResultErrorRedactsCustomUnmarshalerPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, true, map[string]any{
			"ssl_key":  "SECRET_LEAK",
			"password": "SECRET_LEAK",
			"token":    "SECRET_LEAK",
		})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	var out echoUnmarshaler
	err := c.Get("/api/system/get_hostname", &out)
	if err == nil {
		t.Fatal("expected a decode error")
	}
	if strings.Contains(err.Error(), "SECRET_LEAK") {
		t.Fatalf("custom-unmarshaler decode error leaked a secret:\n%v", err)
	}
	if !strings.Contains(err.Error(), Mask) {
		t.Fatalf("custom-unmarshaler decode error did not mask:\n%v", err)
	}
	if !errors.Is(err, errEchoSentinel) {
		t.Fatalf("errors.Is lost the wrapped cause: %v", err)
	}
}

type testWrappedError struct{ msg string }

func (e *testWrappedError) Error() string { return e.msg }

// TestRedactedErrorPreservesUnwrap asserts errors.Is/As still reach the
// underlying error after redaction.
func TestRedactedErrorPreservesUnwrap(t *testing.T) {
	inner := &testWrappedError{msg: "boom?token=SECRET"}
	wrapped := redactError(fmt.Errorf("wrap: %w", inner))

	if strings.Contains(wrapped.Error(), "SECRET") {
		t.Fatalf("redacted error leaked: %v", wrapped)
	}
	if !strings.Contains(wrapped.Error(), "token=***") {
		t.Fatalf("redacted error did not mask: %v", wrapped)
	}
	var target *testWrappedError
	if !errors.As(wrapped, &target) {
		t.Fatalf("errors.As could not unwrap to *testWrappedError: %v", wrapped)
	}
	if target != inner {
		t.Fatalf("unwrapped %p, want %p", target, inner)
	}
}
