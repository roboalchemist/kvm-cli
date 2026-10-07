package models

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	return NewClient(Options{BaseURL: srv.URL, HTTPClient: srv.Client()})
}

func TestNewClientDefaults(t *testing.T) {
	c := NewClient(Options{})
	if c.BaseURL != DefaultBaseURL {
		t.Errorf("BaseURL = %q, want %q", c.BaseURL, DefaultBaseURL)
	}
	if c.GroundingModel != DefaultGroundingModel {
		t.Errorf("GroundingModel = %q, want %q", c.GroundingModel, DefaultGroundingModel)
	}
	if c.Timeout != defaultTimeout {
		t.Errorf("Timeout = %v, want %v", c.Timeout, defaultTimeout)
	}
	if c.HTTPClient == nil {
		t.Error("HTTPClient is nil")
	}
}

func TestNewClientOverrides(t *testing.T) {
	hc := &http.Client{}
	c := NewClient(Options{
		BaseURL:        "http://host:1234/",
		GroundingModel: "other",
		PlannerModel:   "qwen",
		Timeout:        3 * time.Second,
		Insecure:       true,
		HTTPClient:     hc,
	})
	if c.BaseURL != "http://host:1234" {
		t.Errorf("BaseURL = %q", c.BaseURL)
	}
	if c.GroundingModel != "other" || c.PlannerModel != "qwen" {
		t.Errorf("models = %q/%q", c.GroundingModel, c.PlannerModel)
	}
	if c.Timeout != 3*time.Second {
		t.Errorf("Timeout = %v", c.Timeout)
	}
	if c.HTTPClient != hc {
		t.Error("HTTPClient override not used")
	}
	if !c.Insecure {
		t.Error("Insecure not set")
	}
}

func TestResolveAbsoluteAndRelative(t *testing.T) {
	c := NewClient(Options{BaseURL: "http://base.example"})
	if got := c.resolve("/api/models"); got != "http://base.example/api/models" {
		t.Errorf("relative resolve = %q", got)
	}
	if got := c.resolve("https://abs.example/x"); got != "https://abs.example/x" {
		t.Errorf("absolute resolve = %q", got)
	}
	empty := &Client{}
	if got := empty.resolve("/x"); got != "/x" {
		t.Errorf("empty base resolve = %q", got)
	}
}

func TestHTTPErrorType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":"upstream down"}`))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).Catalog(context.Background())
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *models.Error", err)
	}
	if apiErr.Status != http.StatusBadGateway {
		t.Errorf("Status = %d", apiErr.Status)
	}
	if !strings.Contains(apiErr.Message, "upstream down") {
		t.Errorf("Message = %q", apiErr.Message)
	}
}

func TestHTTPErrorEmptyBodyUsesStatusText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv).Catalog(context.Background())
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *models.Error", err)
	}
	if !strings.Contains(apiErr.Error(), "404") {
		t.Errorf("Error() = %q", apiErr.Error())
	}
}

func TestErrorStringBranches(t *testing.T) {
	if got := (&Error{Status: 500}).Error(); !strings.Contains(got, "500") {
		t.Errorf("status-only error = %q", got)
	}
	if got := (&Error{}).Error(); !strings.Contains(got, "HTTP 0") {
		t.Errorf("empty error = %q", got)
	}
}

func TestMalformedJSONResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv).Catalog(context.Background()); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestProbe(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"message":"Omniparser API ready"}`))
	}))
	defer srv.Close()

	msg, err := newTestClient(t, srv).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if msg != "Omniparser API ready" {
		t.Errorf("message = %q", msg)
	}
	if gotPath != "/model/omniparser/probe/" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestProbeHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv).Probe(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestTimeoutHonored(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := NewClient(Options{BaseURL: srv.URL, HTTPClient: srv.Client(), Timeout: 40 * time.Millisecond})
	_, err := c.Catalog(context.Background())
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	c := newTestClient(t, srv)
	srv.Close()

	if _, err := c.Catalog(context.Background()); err == nil {
		t.Fatal("expected transport error")
	}
}

func TestInsecureTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	defer srv.Close()

	if _, err := NewClient(Options{BaseURL: srv.URL, Insecure: true}).Catalog(context.Background()); err != nil {
		t.Fatalf("insecure client failed: %v", err)
	}
	if _, err := NewClient(Options{BaseURL: srv.URL}).Catalog(context.Background()); err == nil {
		t.Fatal("expected TLS verification failure without Insecure")
	}
}

func TestCreateRequestError(t *testing.T) {
	c := NewClient(Options{BaseURL: "http://base.example"})
	// A control character in the path makes http.NewRequestWithContext fail.
	err := c.do(context.Background(), http.MethodGet, "/bad\x7fpath", "", nil, nil)
	if err == nil {
		t.Fatal("expected request creation error")
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("abcdef", 3); got != "abc..." {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("ab", 5); got != "ab" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("ab", 0); got != "ab" {
		t.Errorf("truncate zero max = %q", got)
	}
}
