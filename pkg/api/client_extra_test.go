package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// captureStderr redirects os.Stderr for the duration of fn and returns what was
// written to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = old

	data, _ := io.ReadAll(r)
	_ = r.Close()
	return string(data)
}

// errReader fails every Read, exercising the body-read error path.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }

// countingCtx is a context whose Err() returns nil for the first call and the
// configured error thereafter. It lets a test drive execute past its initial
// context check and then observe a cancellation inside throttle.
type countingCtx struct {
	context.Context
	calls int
}

func (c *countingCtx) Err() error {
	c.calls++
	if c.calls >= 2 {
		return context.Canceled
	}
	return nil
}

// --- accessors / debug ----------------------------------------------------

func TestSetRateLimit(t *testing.T) {
	c := NewClient("https://example.com", "u", "p", Options{})
	c.SetRateLimit(0)
	if c.minInterval != 0 {
		t.Fatalf("minInterval = %v, want 0", c.minInterval)
	}
	c.SetRateLimit(250 * time.Millisecond)
	if c.minInterval != 250*time.Millisecond {
		t.Fatalf("minInterval = %v, want 250ms", c.minInterval)
	}
}

func TestNormalizeBaseURLEmpty(t *testing.T) {
	if got := NewClient("   ", "u", "p", Options{}).BaseURL(); got != "" {
		t.Fatalf("blank base URL normalized to %q, want empty", got)
	}
	if got := NewClient("", "u", "p", Options{}).BaseURL(); got != "" {
		t.Fatalf("empty base URL normalized to %q, want empty", got)
	}
	if got := NewClient("  https://glkvm.example.com/  ", "u", "p", Options{}).BaseURL(); got != "https://glkvm.example.com" {
		t.Fatalf("whitespace/trailing slash not trimmed: %q", got)
	}
}

func TestDebugFlagWritesStderr(t *testing.T) {
	c := NewClient("https://example.com", "u", "p", Options{Debug: true})
	out := captureStderr(t, func() { c.debugf("hello %s", "world") })
	if !strings.Contains(out, "[debug] hello world") {
		t.Fatalf("stderr = %q, want debug line", out)
	}

	// A custom logger takes precedence over the Debug flag and must not write
	// to stderr.
	var got string
	c.SetDebug(func(format string, args ...any) { got = format })
	out = captureStderr(t, func() { c.debugf("custom %d", 1) })
	if got != "custom %d" {
		t.Fatalf("debugFn received %q", got)
	}
	if out != "" {
		t.Fatalf("custom logger should suppress stderr output, got %q", out)
	}
}

func TestDebugDisabledWritesNothing(t *testing.T) {
	c := NewClient("https://example.com", "u", "p", Options{})
	out := captureStderr(t, func() { c.debugf("nope") })
	if out != "" {
		t.Fatalf("debug disabled should write nothing, got %q", out)
	}
}

// --- contextual helpers ---------------------------------------------------

func TestPostContext(t *testing.T) {
	type payload struct {
		Enabled bool `json:"enabled"`
	}
	var got payload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		writeEnvelope(w, true, map[string]any{"enabled": true})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	var out map[string]any
	if err := c.PostContext(context.Background(), "/api/hid/set_params", payload{Enabled: true}, &out); err != nil {
		t.Fatalf("PostContext: %v", err)
	}
	if !got.Enabled || out["enabled"] != true {
		t.Fatalf("body/result mismatch: got=%+v out=%+v", got, out)
	}
}

func TestPostContextMarshalError(t *testing.T) {
	c := newTestClient(t, "https://example.invalid")
	if err := c.PostContext(context.Background(), "/api/x", make(chan int), nil); err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestPostFormContext(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got = r.PostForm
		writeEnvelope(w, true, map[string]any{"ok": true})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	form := url.Values{"code": {"123456"}}
	var out map[string]any
	if err := c.PostFormContext(context.Background(), "/api/2fa/verify", form, &out); err != nil {
		t.Fatalf("PostFormContext: %v", err)
	}
	if got.Get("code") != "123456" {
		t.Fatalf("form not received: %v", got)
	}
}

func TestPostFormNilBody(t *testing.T) {
	if got := encodeForm(nil); got != nil {
		t.Fatalf("encodeForm(nil) = %v, want nil", got)
	}
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		if ct := r.Header.Get("Content-Type"); ct != formContentType {
			t.Errorf("Content-Type = %q", ct)
		}
		writeEnvelope(w, true, map[string]any{})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if err := c.PostForm("/api/auth/logout", nil, nil); err != nil {
		t.Fatalf("PostForm: %v", err)
	}
	if len(body) != 0 {
		t.Fatalf("expected empty body, got %q", body)
	}
}

// --- login / logout error paths -------------------------------------------

func TestLoginRequestError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeEnvelope(w, false, map[string]any{"error": "bad_request", "error_msg": "nope"})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if err := c.Login(); err == nil {
		t.Fatal("expected Login to surface the request error")
	}
}

func TestLoginDebugLogging(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, true, map[string]any{"token": "abcdef"})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	var logged []string
	c.SetDebug(func(format string, args ...any) { logged = append(logged, format) })
	if err := c.Login(); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if c.Token() != "abcdef" {
		t.Fatalf("token = %q", c.Token())
	}
	if len(logged) == 0 {
		t.Fatal("expected debug logging during login")
	}
}

func TestLogoutErrorKeepsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		writeEnvelope(w, false, map[string]any{"error": "Forbidden", "error_msg": "denied"})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	c.SetToken("keep")
	if err := c.Logout(); err == nil {
		t.Fatal("expected logout error")
	}
	if c.Token() != "keep" {
		t.Fatalf("token should be retained on failed logout, got %q", c.Token())
	}
}

// --- raw endpoints --------------------------------------------------------

func TestGetRawContextSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("PNGDATA"))
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	data, ct, err := c.GetRawContext(context.Background(), "/api/streamer/snapshot")
	if err != nil {
		t.Fatalf("GetRawContext: %v", err)
	}
	if string(data) != "PNGDATA" || ct != "image/png" {
		t.Fatalf("data=%q ct=%q", data, ct)
	}
}

func TestGetRawContextExecuteError(t *testing.T) {
	c := newTestClient(t, "https://example.invalid")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.GetRawContext(ctx, "/api/streamer/snapshot"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// --- envelope edge cases --------------------------------------------------

func TestDecodeEnvelopeMalformedJSONSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "this is not json")
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	err := c.Get("/api/info", nil)
	if err == nil || !strings.Contains(err.Error(), "parse response") {
		t.Fatalf("err = %v, want parse response error", err)
	}
}

func TestDecodeEnvelopeOKTrueButHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeEnvelope(w, true, map[string]any{"unexpected": true})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	err := c.Get("/api/info", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Fatalf("Status = %d, want 400", apiErr.Status)
	}
	if apiErr.Code != http.StatusText(http.StatusBadRequest) {
		t.Fatalf("Code = %q, want %q", apiErr.Code, http.StatusText(http.StatusBadRequest))
	}
}

func TestDecodeEnvelopeMissingAndNullResult(t *testing.T) {
	for _, body := range []string{
		`{"ok":true}`,
		`{"ok":true,"result":null}`,
	} {
		body := body
		t.Run(body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, body)
			}))
			defer srv.Close()

			c := newTestClient(t, srv.URL)
			var out map[string]any
			if err := c.Get("/api/info", &out); err != nil {
				t.Fatalf("Get: %v", err)
			}
			if out != nil {
				t.Fatalf("out = %v, want untouched nil map", out)
			}
		})
	}
}

func TestDecodeEnvelopeResultUnmarshalError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, true, "a string, not an object")
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	var out struct {
		Field string `json:"field"`
	}
	err := c.Get("/api/info", &out)
	if err == nil || !strings.Contains(err.Error(), "parse result") {
		t.Fatalf("err = %v, want parse result error", err)
	}
}

// --- error builders -------------------------------------------------------

func TestErrorFromRawBranches(t *testing.T) {
	c := &Client{}

	if e := c.errorFromRaw(nil, http.StatusBadRequest).(*APIError); e.Code != http.StatusText(400) || e.Message != e.Code {
		t.Fatalf("empty raw: %+v", e)
	}
	// A status with no canonical text falls back to the literal "error".
	if e := c.errorFromRaw(nil, 599).(*APIError); e.Code != "error" || e.Message != "error" {
		t.Fatalf("unknown status: %+v", e)
	}
	// A non-object result cannot be decoded into the payload struct.
	if e := c.errorFromRaw(json.RawMessage(`"nope"`), http.StatusBadRequest).(*APIError); e.Code != http.StatusText(400) {
		t.Fatalf("non-object raw: %+v", e)
	}
	// Error code present but no message: message falls back to the code.
	if e := c.errorFromRaw(json.RawMessage(`{"error":"CodeOnly"}`), http.StatusOK).(*APIError); e.Code != "CodeOnly" || e.Message != "CodeOnly" {
		t.Fatalf("code-only raw: %+v", e)
	}
}

func TestErrorFromResponseBranches(t *testing.T) {
	c := &Client{}

	// A non-canonical status yields "HTTP <n>".
	e := c.errorFromResponse(&response{status: 599, body: []byte("upstream junk")})
	if e.Code != "HTTP 599" || e.Message != "upstream junk" {
		t.Fatalf("unknown status: %+v", e)
	}
	// An empty body makes the message default to the code.
	e = c.errorFromResponse(&response{status: http.StatusBadRequest})
	if e.Message != e.Code || e.Code != http.StatusText(http.StatusBadRequest) {
		t.Fatalf("empty body: %+v", e)
	}
	// A body that is valid but successful JSON is not treated as an envelope
	// error; the HTTP status still governs.
	e = c.errorFromResponse(&response{status: http.StatusTeapot, body: []byte(`{"ok":true}`)})
	if e.Status != http.StatusTeapot {
		t.Fatalf("status = %d", e.Status)
	}
}

func TestAPIErrorErrorBranches(t *testing.T) {
	if got := (&APIError{}).Error(); !strings.Contains(got, "unknown") {
		t.Fatalf("zero APIError = %q, want it to mention unknown", got)
	}
	if got := (&APIError{Code: "C", Message: "M"}).Error(); !strings.Contains(got, "C: M") {
		t.Fatalf("statusless APIError = %q", got)
	}
	if got := (&APIError{Code: "C", Status: 500}).Error(); !strings.Contains(got, "API error 500") {
		t.Fatalf("statusful APIError = %q", got)
	}
}

// --- retry / execute ------------------------------------------------------

func TestExecuteAttemptsClamp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, true, map[string]any{"model": "RM1PE"})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	c.maxRetries = -5 // would make attempts negative without the clamp
	var v UpgradeVersion
	if err := c.Get("/api/upgrade/version", &v); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v.Model != "RM1PE" {
		t.Fatalf("unexpected result: %+v", v)
	}
}

func TestExecuteTransportErrorExhausted(t *testing.T) {
	c := newTestClient(t, "https://example.invalid")
	c.maxRetries = 0
	c.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp: refused")
	})
	err := c.Get("/api/info", nil)
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("err = %v, want transport error", err)
	}
}

func TestExecuteRetryContextCancelDuringBackoff(t *testing.T) {
	c := newTestClient(t, "https://example.invalid")
	c.maxRetries = 2
	c.retryWait = 200 * time.Millisecond
	c.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("temporary")
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	err := c.GetContext(ctx, "/api/info", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestExecuteThrottleContextError(t *testing.T) {
	c := newTestClient(t, "https://example.invalid")
	c.minInterval = 0 // throttle short-circuits to ctx.Err()
	c.maxRetries = 0

	ctx := &countingCtx{Context: context.Background()}
	if err := c.GetContext(ctx, "/api/info", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled from throttle", err)
	}
}

func TestAttemptCreateRequestError(t *testing.T) {
	c := newTestClient(t, "https://example.invalid")
	_, retryable, err := c.attempt(context.Background(), "GET WITH SPACE", "https://example.invalid/x", nil, "")
	if err == nil {
		t.Fatal("expected request-construction error")
	}
	if retryable {
		t.Fatal("a malformed request is not retryable")
	}
	if !strings.Contains(err.Error(), "create request") {
		t.Fatalf("err = %v, want create request error", err)
	}
}

func TestAttemptReadBodyError(t *testing.T) {
	c := newTestClient(t, "https://example.invalid")
	c.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(errReader{}),
			Header:     make(http.Header),
		}, nil
	})

	_, retryable, err := c.attempt(context.Background(), http.MethodGet, "https://example.invalid/x", nil, "")
	if err == nil || !strings.Contains(err.Error(), "read response") {
		t.Fatalf("err = %v, want read response error", err)
	}
	if !retryable {
		t.Fatal("a body read failure should be retryable")
	}
}

// --- URL and small helpers ------------------------------------------------

func TestResolveURL(t *testing.T) {
	c := newTestClient(t, "https://glkvm.example.com/")
	if got := c.resolveURL("/api/info"); got != "https://glkvm.example.com/api/info" {
		t.Fatalf("relative path = %q", got)
	}
	if got := c.resolveURL("api/info"); got != "https://glkvm.example.com/api/info" {
		t.Fatalf("path without slash = %q", got)
	}
	if got := c.resolveURL("http://other.example.com/x"); got != "http://other.example.com/x" {
		t.Fatalf("absolute http URL = %q", got)
	}
	if got := c.resolveURL("https://other.example.com/x"); got != "https://other.example.com/x" {
		t.Fatalf("absolute https URL = %q", got)
	}

	empty := NewClient("", "u", "p", Options{})
	if got := empty.resolveURL("/api/info"); got != "/api/info" {
		t.Fatalf("empty base = %q, want passthrough", got)
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"hello world", 5, "hello..."},
		{"abc", 0, "abc"},
		{"abc", -1, "abc"},
	}
	for _, tc := range cases {
		if got := truncate(tc.in, tc.max); got != tc.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}

func TestBackoff(t *testing.T) {
	c := &Client{retryWait: 100 * time.Millisecond}
	want := []time.Duration{0, 100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond}
	// attempt 1 returns the base wait; each subsequent attempt doubles.
	for i, w := range want {
		if i == 0 {
			continue
		}
		if got := c.backoff(i); got != w {
			t.Errorf("backoff(%d) = %v, want %v", i, got, w)
		}
	}
	// The delay is capped at maxBackoff.
	if got := c.backoff(50); got != maxBackoff {
		t.Errorf("backoff(50) = %v, want %v", got, maxBackoff)
	}
	// A non-positive base disables backoff entirely.
	off := &Client{retryWait: 0}
	if got := off.backoff(3); got != 0 {
		t.Errorf("backoff with zero base = %v, want 0", got)
	}
}

func TestSleepCtx(t *testing.T) {
	// Non-positive duration returns the context error (nil here).
	if err := sleepCtx(context.Background(), 0); err != nil {
		t.Fatalf("sleepCtx(0) = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(cancelled, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepCtx(cancelled, 0) = %v, want context.Canceled", err)
	}
	// A positive duration elapses normally.
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("sleepCtx = %v", err)
	}
	// Cancellation during the wait is observed promptly.
	waitCtx, waitCancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		waitCancel()
	}()
	if err := sleepCtx(waitCtx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleepCtx during wait = %v, want context.Canceled", err)
	}
}

func TestAuthQueryParamEscaping(t *testing.T) {
	c := NewClient("https://example.com", "u", "p", Options{})
	tok := "a b&c=d/e+f?g#h%i"
	c.SetToken(tok)
	want := "auth_token=" + url.QueryEscape(tok)
	if got := c.AuthQueryParam(); got != want {
		t.Fatalf("AuthQueryParam() = %q, want %q", got, want)
	}
	// The escaped value must round-trip back to the original token.
	vals, err := url.ParseQuery(c.AuthQueryParam())
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if vals.Get("auth_token") != tok {
		t.Fatalf("token round-trip = %q, want %q", vals.Get("auth_token"), tok)
	}
}
