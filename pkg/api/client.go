// Package api provides an HTTP client for the GL.iNet Comet (GL-RM1PE) remote
// KVM REST API.
//
// Almost every endpoint wraps its payload in a JSON envelope:
//
//	{"ok": true, "result": {...}}
//	{"ok": false, "result": {"error": "...", "error_msg": "..."}}
//
// Requests authenticate with a `token` header whose value is obtained from
// POST /api/auth/login. A handful of endpoints (for example the JPEG snapshot)
// bypass the envelope entirely and return raw bytes; those are handled by
// GetRaw.
//
// The client is safe for concurrent use: the auth token is guarded by a mutex
// and reads may be issued from multiple goroutines.
package api

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/roboalchemist/kvm-cli/pkg/redact"
)

const (
	// defaultTimeout is used when Options.Timeout is unset.
	defaultTimeout = 30 * time.Second
	// defaultMaxRetries is the number of retries after the initial attempt.
	defaultMaxRetries = 3
	// defaultRetryWait is the base delay for exponential backoff between retries.
	defaultRetryWait = 500 * time.Millisecond
	// maxBackoff caps the exponential backoff delay.
	maxBackoff = 30 * time.Second
	// defaultMinInterval is the minimum spacing between successive requests.
	defaultMinInterval = 100 * time.Millisecond
	// clientVersion is embedded in the default User-Agent header.
	clientVersion = "dev"
)

// Options configures a Client.
type Options struct {
	// Insecure disables TLS certificate verification. The Comet may present a
	// self-signed certificate, so callers often need this enabled.
	Insecure bool
	// Timeout is the per-request HTTP timeout. A non-positive value uses 30s.
	Timeout time.Duration
	// UserAgent overrides the default User-Agent header.
	UserAgent string
	// Debug enables verbose request/response logging to stderr.
	Debug bool
}

// Client is a concurrency-safe HTTP client for the KVM REST API.
type Client struct {
	baseURL    string
	username   string
	password   string
	httpClient *http.Client
	userAgent  string
	debug      bool
	debugFn    func(string, ...any)

	// mu guards the auth token.
	mu    sync.RWMutex
	token string

	// rateMu guards the simple request pacing state.
	rateMu      sync.Mutex
	minInterval time.Duration
	lastRequest time.Time

	maxRetries int
	retryWait  time.Duration
}

// NewClient creates a client for the given base URL (for example
// "https://glkvm.example.com"). The client does not authenticate until Login
// is called; externally supplied tokens may be injected with SetToken.
func NewClient(baseURL, username, password string, opts Options) *Client {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ua := opts.UserAgent
	if ua == "" {
		ua = "kvm-cli/" + clientVersion
	}

	transport := &http.Transport{
		//nolint:gosec // The device commonly uses a self-signed certificate.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: opts.Insecure},
	}

	return &Client{
		baseURL:     normalizeBaseURL(baseURL),
		username:    username,
		password:    password,
		httpClient:  &http.Client{Timeout: timeout, Transport: transport},
		userAgent:   ua,
		debug:       opts.Debug,
		minInterval: defaultMinInterval,
		maxRetries:  defaultMaxRetries,
		retryWait:   defaultRetryWait,
	}
}

// normalizeBaseURL trims whitespace and a trailing slash and prepends a scheme
// when the caller passed a bare host.
func normalizeBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	return strings.TrimRight(raw, "/")
}

// BaseURL returns the normalised base URL.
func (c *Client) BaseURL() string { return c.baseURL }

// SetDebug installs a custom debug logger, overriding the Options.Debug flag.
func (c *Client) SetDebug(fn func(string, ...any)) { c.debugFn = fn }

// SetRateLimit overrides the minimum spacing between requests. A non-positive
// duration disables pacing.
func (c *Client) SetRateLimit(d time.Duration) { c.minInterval = d }

func (c *Client) debugf(format string, args ...any) {
	if c.debugFn != nil {
		c.debugFn(format, args...)
		return
	}
	if c.debug {
		// Build the message first so vet never sees a non-constant format string.
		fmt.Fprintln(os.Stderr, "[debug] "+fmt.Sprintf(format, args...))
	}
}

// Token returns the current auth token (empty before login).
func (c *Client) Token() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.token
}

// SetToken injects an auth token directly, bypassing Login.
func (c *Client) SetToken(tok string) {
	c.mu.Lock()
	c.token = tok
	c.mu.Unlock()
}

// AuthQueryParam returns the token as a URL query component
// ("auth_token=<tok>") for endpoints that authenticate via the query string,
// such as WebSocket channels and raw download URLs.
func (c *Client) AuthQueryParam() string {
	return "auth_token=" + url.QueryEscape(c.Token())
}

// Login authenticates with POST /api/auth/login and stores the returned token.
func (c *Client) Login() error {
	form := url.Values{}
	form.Set("user", c.username)
	form.Set("passwd", c.password)

	var res LoginResult
	if err := c.PostForm("/api/auth/login", form, &res); err != nil {
		return err
	}
	if res.Token == "" {
		return fmt.Errorf("login: response contained no token")
	}
	c.SetToken(res.Token)
	c.debugf("login ok (token length %d)", len(res.Token))
	return nil
}

// Logout terminates the device session and clears the stored token on success.
func (c *Client) Logout() error {
	if err := c.Post("/api/auth/logout", nil, nil); err != nil {
		return err
	}
	c.SetToken("")
	return nil
}

// Check verifies that the current token is still valid via GET /api/auth/check.
func (c *Client) Check() error {
	return c.Get("/api/auth/check", nil)
}

// Get performs an authenticated GET and decodes the envelope result into out.
// A nil out discards the result.
func (c *Client) Get(path string, out any) error {
	return c.doRequest(context.Background(), http.MethodGet, path, nil, "", out)
}

// GetContext is Get with a caller-supplied context.
func (c *Client) GetContext(ctx context.Context, path string, out any) error {
	return c.doRequest(ctx, http.MethodGet, path, nil, "", out)
}

// Post performs an authenticated POST with a JSON body and decodes the envelope
// result into out. A nil body sends an empty request.
func (c *Client) Post(path string, body any, out any) error {
	payload, contentType, err := marshalJSONBody(body)
	if err != nil {
		return err
	}
	return c.doRequest(context.Background(), http.MethodPost, path, payload, contentType, out)
}

// PostContext is Post with a caller-supplied context.
func (c *Client) PostContext(ctx context.Context, path string, body any, out any) error {
	payload, contentType, err := marshalJSONBody(body)
	if err != nil {
		return err
	}
	return c.doRequest(ctx, http.MethodPost, path, payload, contentType, out)
}

// PostForm performs an authenticated POST with an
// application/x-www-form-urlencoded body and decodes the envelope result.
func (c *Client) PostForm(path string, form url.Values, out any) error {
	return c.doRequest(context.Background(), http.MethodPost, path, encodeForm(form), formContentType, out)
}

// PostFormContext is PostForm with a caller-supplied context.
func (c *Client) PostFormContext(ctx context.Context, path string, form url.Values, out any) error {
	return c.doRequest(ctx, http.MethodPost, path, encodeForm(form), formContentType, out)
}

// GetRaw performs an authenticated GET against an endpoint that returns raw
// bytes rather than a JSON envelope (for example the JPEG snapshot). It returns
// the body and the response Content-Type.
func (c *Client) GetRaw(path string) ([]byte, string, error) {
	return c.GetRawContext(context.Background(), path)
}

// GetRawContext is GetRaw with a caller-supplied context.
func (c *Client) GetRawContext(ctx context.Context, path string) ([]byte, string, error) {
	resp, err := c.execute(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return nil, "", err
	}
	if resp.status < 200 || resp.status >= 300 {
		return nil, "", c.errorFromResponse(resp)
	}
	return resp.body, resp.header.Get("Content-Type"), nil
}

const formContentType = "application/x-www-form-urlencoded"

func encodeForm(form url.Values) []byte {
	if form == nil {
		return nil
	}
	return []byte(form.Encode())
}

func marshalJSONBody(body any) ([]byte, string, error) {
	if body == nil {
		return nil, "", nil
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, "", fmt.Errorf("marshal request body: %w", err)
	}
	return data, "application/json", nil
}

// response captures the parts of an HTTP response the client needs after the
// body has been drained (so retries can re-issue requests cheaply).
type response struct {
	body   []byte
	status int
	header http.Header
}

// doRequest executes a request and decodes the JSON envelope into out.
func (c *Client) doRequest(ctx context.Context, method, path string, body []byte, contentType string, out any) error {
	resp, err := c.execute(ctx, method, path, body, contentType)
	if err != nil {
		return err
	}
	return c.decodeEnvelope(resp, out)
}

// decodeEnvelope interprets the standard {"ok", "result"} wrapper.
func (c *Client) decodeEnvelope(resp *response, out any) error {
	var env envelope
	if err := json.Unmarshal(resp.body, &env); err != nil {
		if resp.status < 200 || resp.status >= 300 {
			return c.errorFromResponse(resp)
		}
		// Redact the body before truncating so a secret straddling the
		// truncation boundary is still masked; redactDecodeError re-redacts
		// the composed message (catching secrets embedded in err's text).
		return redactDecodeError(err, "parse response: %v (body: %s)",
			err, truncate(redact.String(string(resp.body)), 200))
	}

	if !env.OK {
		return c.errorFromRaw(env.Result, resp.status)
	}
	if resp.status < 200 || resp.status >= 300 {
		return c.errorFromResponse(resp)
	}
	if out == nil {
		return nil
	}

	result := bytes.TrimSpace(env.Result)
	if len(result) == 0 || bytes.Equal(result, []byte("null")) {
		return nil
	}
	if err := json.Unmarshal(result, out); err != nil {
		return redactDecodeError(err, "parse result: %v (result: %s)",
			err, truncate(redact.String(string(result)), 200))
	}
	return nil
}

// redactDecodeError builds a decode-failure error whose fully-rendered message
// has been passed through pkg/redact, while preserving cause for
// errors.Is/errors.As via Unwrap.
//
// Redacting only the appended payload is not sufficient: a custom
// json.Unmarshaler can embed the raw payload in its own error text (for example
// api.Hostname), and that text is what the %v verb inserts. Passing the whole
// composed message through redact.String catches secrets regardless of which
// part of the message carried them.
func redactDecodeError(cause error, format string, args ...any) error {
	return &redactedMessageError{
		msg: redact.String(fmt.Sprintf(format, args...)),
		err: cause,
	}
}

// redactedMessageError renders a pre-redacted message while preserving the
// original error for errors.Is/errors.As.
type redactedMessageError struct {
	msg string
	err error
}

func (e *redactedMessageError) Error() string { return e.msg }

func (e *redactedMessageError) Unwrap() error { return e.err }

// errorFromRaw converts an envelope error payload into a typed *APIError.
func (c *Client) errorFromRaw(raw json.RawMessage, status int) error {
	e := &APIError{Status: status}
	if len(raw) > 0 {
		var payload apiErrorPayload
		if err := json.Unmarshal(raw, &payload); err == nil {
			e.Code = payload.Error
			e.Message = payload.ErrorMsg
		}
	}
	if e.Code == "" {
		if text := http.StatusText(status); text != "" {
			e.Code = text
		} else {
			e.Code = "error"
		}
	}
	if e.Message == "" {
		e.Message = e.Code
	}
	return e
}

// errorFromResponse builds an *APIError from a response that was not a
// successful envelope, preferring a decoded envelope error when present.
func (c *Client) errorFromResponse(resp *response) *APIError {
	var env envelope
	if err := json.Unmarshal(resp.body, &env); err == nil && !env.OK {
		if e, ok := c.errorFromRaw(env.Result, resp.status).(*APIError); ok {
			return e
		}
	}
	code := http.StatusText(resp.status)
	if code == "" {
		code = fmt.Sprintf("HTTP %d", resp.status)
	}
	message := truncate(redact.String(strings.TrimSpace(string(resp.body))), 500)
	if message == "" {
		message = code
	}
	return &APIError{Code: code, Message: message, Status: resp.status}
}

// APIError is returned when the device reports a logical failure (ok=false) or
// responds with a non-2xx HTTP status.
//
// The envelope carries the error as {"result": {"error": "...", "error_msg":
// "..."}}. Code holds the machine-readable error name (result.error) and
// Message holds the human-readable text (result.error_msg). Status is the HTTP
// status code (200 for logical failures carried inside a successful HTTP
// response).
//
// Note: the error name is exposed as Code rather than Error because an
// exported Error field would shadow the Error method required by the error
// interface.
type APIError struct {
	Code    string
	Message string
	Status  int
}

// Error implements the error interface.
func (e *APIError) Error() string {
	code := e.Code
	if code == "" {
		code = "unknown"
	}
	message := e.Message
	if message == "" {
		message = code
	}
	if e.Status > 0 {
		return fmt.Sprintf("API error %d: %s: %s", e.Status, code, message)
	}
	return fmt.Sprintf("API error: %s: %s", code, message)
}

// IsNotFound reports whether the failure was an HTTP 404.
func (e *APIError) IsNotFound() bool { return e.Status == http.StatusNotFound }

// IsUnauthorized reports whether the failure was an HTTP 401.
func (e *APIError) IsUnauthorized() bool { return e.Status == http.StatusUnauthorized }

// execute issues a request, retrying transient failures (transport errors, 5xx
// and 429) with exponential backoff.
func (c *Client) execute(ctx context.Context, method, path string, body []byte, contentType string) (*response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	fullURL := c.resolveURL(path)
	attempts := c.maxRetries + 1
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, c.backoff(attempt)); err != nil {
				return nil, err
			}
		}
		if err := c.throttle(ctx); err != nil {
			return nil, err
		}

		resp, retryable, err := c.attempt(ctx, method, fullURL, body, contentType)
		if err != nil {
			lastErr = err
			if retryable && attempt < attempts-1 {
				c.debugf("retry %d/%d after error: %v", attempt+1, c.maxRetries, err)
				continue
			}
			return nil, err
		}
		if retryable && attempt < attempts-1 {
			lastErr = c.errorFromResponse(resp)
			c.debugf("retry %d/%d after HTTP %d", attempt+1, c.maxRetries, resp.status)
			continue
		}
		return resp, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("request failed")
	}
	return nil, lastErr
}

// attempt performs a single HTTP exchange. retryable reports whether the
// caller should retry; it is true for transport errors, 5xx and 429.
func (c *Client) attempt(ctx context.Context, method, fullURL string, body []byte, contentType string) (*response, bool, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, fullURL, reader)
	if err != nil {
		return nil, false, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if tok := c.Token(); tok != "" {
		req.Header.Set("token", tok)
	}

	// Redact secret query parameters (for example ap key=, modem pin=,
	// zerotier token=) before the URL reaches the debug log or an error string.
	safeURL := redact.URL(fullURL)

	c.debugf("%s %s", method, safeURL)
	httpResp, err := c.httpClient.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, false, ctxErr
		}
		return nil, true, redactError(fmt.Errorf("%s %s: %w", method, safeURL, err))
	}
	defer func() { _ = httpResp.Body.Close() }()

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, true, fmt.Errorf("read response: %w", err)
	}

	c.debugf("%s %s -> %d (%d bytes)", method, safeURL, httpResp.StatusCode, len(data))

	retryable := httpResp.StatusCode >= 500 || httpResp.StatusCode == http.StatusTooManyRequests
	return &response{body: data, status: httpResp.StatusCode, header: httpResp.Header}, retryable, nil
}

// resolveURL joins the base URL and path, leaving absolute URLs untouched.
func (c *Client) resolveURL(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if c.baseURL == "" {
		return path
	}
	return c.baseURL + "/" + strings.TrimLeft(path, "/")
}

// throttle enforces the minimum spacing between requests. It reserves the next
// slot under the lock, then releases the lock while waiting so that a cancelled
// context is honoured promptly.
func (c *Client) throttle(ctx context.Context) error {
	if c.minInterval <= 0 {
		return ctx.Err()
	}

	c.rateMu.Lock()
	now := time.Now()
	var wait time.Duration
	if !c.lastRequest.IsZero() {
		if elapsed := now.Sub(c.lastRequest); elapsed < c.minInterval {
			wait = c.minInterval - elapsed
		}
	}
	c.lastRequest = now.Add(wait)
	c.rateMu.Unlock()

	if wait <= 0 {
		return ctx.Err()
	}
	return sleepCtx(ctx, wait)
}

// backoff returns the delay before the given retry attempt (1-based).
func (c *Client) backoff(attempt int) time.Duration {
	if c.retryWait <= 0 {
		return 0
	}
	d := c.retryWait
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= maxBackoff {
			return maxBackoff
		}
	}
	return d
}

// sleepCtx sleeps for d, returning early with ctx.Err() if the context is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// redactError wraps err so its message never contains a secret query value,
// while preserving the original error for errors.Is/errors.As via Unwrap. The
// standard library embeds the request URL in transport errors, so the raw error
// must be re-rendered with the URL redacted.
func redactError(err error) error {
	if err == nil {
		return nil
	}
	return &redactedError{err: err}
}

type redactedError struct{ err error }

func (e *redactedError) Error() string { return redact.URL(e.err.Error()) }

func (e *redactedError) Unwrap() error { return e.err }
