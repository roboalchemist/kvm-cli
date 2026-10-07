// Package models is a small client for the personal models platform
// (https://models.example.com).
//
// It speaks only the slice of the platform needed for computer-use grounding:
// the catalog (GET /api/models), OmniParser grounding
// (POST /model/<id>/v1/ground), the Microsoft omniparserserver compatibility
// endpoints (POST /model/<id>/parse/, GET /model/<id>/probe/), and any
// OpenAI-compatible chat model used as the element chooser
// (POST /model/<id>/v1/chat/completions).
//
// Every model call goes through the control-plane proxy (/model/<id>/...), so
// the caller never needs to know which host currently serves a model. Outbound
// URLs and transport errors are passed through pkg/redact before they can reach
// a log or an error string.
package models

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/roboalchemist/kvm-cli/pkg/redact"
)

const (
	// DefaultBaseURL is the hosted models platform.
	DefaultBaseURL = "https://models.example.com"
	// DefaultGroundingModel is the catalog's screen-parsing model.
	DefaultGroundingModel = "omniparser"
	// defaultTimeout bounds a single request. Grounding is comparatively slow,
	// so the ceiling is generous.
	defaultTimeout = 120 * time.Second
	// maxErrorBody caps how much of a failing response body is surfaced.
	maxErrorBody = 500
)

// Options configures a Client.
type Options struct {
	// BaseURL is the platform root. Empty uses DefaultBaseURL.
	BaseURL string
	// GroundingModel is the screen parser id. Empty uses DefaultGroundingModel.
	GroundingModel string
	// PlannerModel is the default chat model for Plan. Empty requires the
	// caller to pass PlanOptions.Model.
	PlannerModel string
	// Timeout bounds each request. Non-positive uses defaultTimeout.
	Timeout time.Duration
	// Insecure disables TLS certificate verification.
	Insecure bool
	// HTTPClient, when set, is used verbatim (for example an httptest server's
	// client). Timeout is still enforced through the request context.
	HTTPClient *http.Client
}

// Client talks to the models platform. It is safe for concurrent use when the
// underlying HTTP client is.
type Client struct {
	BaseURL        string
	GroundingModel string
	PlannerModel   string
	Timeout        time.Duration
	Insecure       bool
	HTTPClient     *http.Client
}

// NewClient builds a Client, applying defaults for an empty base URL, grounding
// model and timeout.
func NewClient(opts Options) *Client {
	base := strings.TrimSpace(opts.BaseURL)
	if base == "" {
		base = DefaultBaseURL
	}

	gm := strings.TrimSpace(opts.GroundingModel)
	if gm == "" {
		gm = DefaultGroundingModel
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				//nolint:gosec // The platform may present a private CA.
				TLSClientConfig: &tls.Config{InsecureSkipVerify: opts.Insecure},
			},
		}
	}

	return &Client{
		BaseURL:        strings.TrimRight(base, "/"),
		GroundingModel: gm,
		PlannerModel:   strings.TrimSpace(opts.PlannerModel),
		Timeout:        timeout,
		Insecure:       opts.Insecure,
		HTTPClient:     hc,
	}
}

// Error is returned when the platform responds with a non-2xx status. The
// message body is truncated and redacted.
type Error struct {
	Status  int
	Message string
}

// HTTPStatus reports the HTTP status code of the failed platform request. It
// lets pkg/output classify a 5xx (or 4xx) platform error as a system/device
// failure (exit 3) instead of a generic user error (exit 1).
func (e *Error) HTTPStatus() int { return e.Status }

// Error implements the error interface.
func (e *Error) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	if msg == "" {
		return fmt.Sprintf("models: HTTP %d", e.Status)
	}
	return fmt.Sprintf("models: HTTP %d: %s", e.Status, msg)
}

// Probe checks the grounding model through the Microsoft omniparserserver
// health endpoint and returns its readiness message.
func (c *Client) Probe(ctx context.Context) (string, error) {
	var out struct {
		Message string `json:"message"`
	}
	path := "/model/" + url.PathEscape(c.GroundingModel) + "/probe/"
	if err := c.do(ctx, http.MethodGet, path, "", nil, &out); err != nil {
		return "", err
	}
	return out.Message, nil
}

// do issues one request and, on success, decodes the JSON body into out (nil
// discards it). A non-2xx response becomes a *Error.
func (c *Client) do(ctx context.Context, method, path, contentType string, body []byte, out any) error {
	target := c.resolve(path)

	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return redact.Error(fmt.Errorf("models: create request: %w", err))
	}
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return redact.Error(fmt.Errorf("models: %s %s: %w", method, redact.URL(target), err))
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return redact.Error(fmt.Errorf("models: read response from %s: %w", redact.URL(target), err))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &Error{
			Status:  resp.StatusCode,
			Message: truncate(redact.String(strings.TrimSpace(string(data))), maxErrorBody),
		}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return redact.Error(fmt.Errorf("models: parse response from %s: %w", redact.URL(target), err))
	}
	return nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// resolve joins the base URL and path, leaving absolute URLs untouched.
func (c *Client) resolve(path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	if c.BaseURL == "" {
		return path
	}
	return strings.TrimRight(c.BaseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
