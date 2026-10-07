package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient returns a client wired to a test server with pacing and retry
// delays disabled so tests run fast and deterministically.
func newTestClient(t *testing.T, base string) *Client {
	t.Helper()
	c := NewClient(base, "admin", "secret", Options{})
	c.minInterval = 0
	c.retryWait = time.Millisecond
	return c
}

func writeEnvelope(w http.ResponseWriter, ok bool, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": ok, "result": result})
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestGetEnvelopeSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/upgrade/version" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		writeEnvelope(w, true, map[string]string{"model": "RM1PE", "version": "V1.10.1 release2"})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	var v UpgradeVersion
	if err := c.Get("/api/upgrade/version", &v); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if v.Model != "RM1PE" || v.Version != "V1.10.1 release2" {
		t.Fatalf("unexpected result: %+v", v)
	}
}

func TestGetEnvelopeLogicalError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		writeEnvelope(w, false, map[string]any{"error": "ValidatorError", "error_msg": "bad username"})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	err := c.Get("/api/auth/login", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Code != "ValidatorError" {
		t.Errorf("Code = %q, want ValidatorError", apiErr.Code)
	}
	if apiErr.Message != "bad username" {
		t.Errorf("Message = %q", apiErr.Message)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Errorf("Status = %d, want 400", apiErr.Status)
	}
}

func TestGetLogicalErrorAtHTTP200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, false, map[string]any{"error": "NotAllowed", "error_msg": "nope"})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	err := c.Get("/api/atx/click", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Status != http.StatusOK || apiErr.Code != "NotAllowed" {
		t.Fatalf("unexpected error: %+v", apiErr)
	}
}

func TestGetNonJSONHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "totally not json")
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	err := c.Get("/api/info", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Errorf("Status = %d, want 400", apiErr.Status)
	}
	if apiErr.Code != http.StatusText(http.StatusBadRequest) {
		t.Errorf("Code = %q", apiErr.Code)
	}
}

func TestIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		writeEnvelope(w, false, map[string]any{"error": "NotFound", "error_msg": "no such thing"})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	err := c.Get("/api/wol/remove", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if !apiErr.IsNotFound() {
		t.Errorf("IsNotFound() = false for status %d", apiErr.Status)
	}
}

func TestTokenHeaderInjection(t *testing.T) {
	var gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("token")
		writeEnvelope(w, true, map[string]any{})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	c.SetToken("thistoken")

	if err := c.Get("/api/auth/check", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if gotToken != "thistoken" {
		t.Fatalf("token header = %q, want thistoken", gotToken)
	}
}

func TestLoginFormEncoding(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/auth/login" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		if got := r.PostFormValue("user"); got != "admin" {
			t.Errorf("user = %q, want admin", got)
		}
		if got := r.PostFormValue("passwd"); got != "secret" {
			t.Errorf("passwd = %q, want secret", got)
		}
		writeEnvelope(w, true, map[string]any{"token": "0123456789abcdef", "failed_since_last_success": 0})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if err := c.Login(); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if c.Token() != "0123456789abcdef" {
		t.Fatalf("token = %q", c.Token())
	}
}

func TestLoginMissingToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, true, map[string]any{})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if err := c.Login(); err == nil {
		t.Fatal("expected error when token is absent")
	}
}

func TestLogoutClearsToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/logout" {
			t.Errorf("path = %q", r.URL.Path)
		}
		writeEnvelope(w, true, map[string]any{})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	c.SetToken("abc")
	if err := c.Logout(); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if c.Token() != "" {
		t.Fatalf("token not cleared: %q", c.Token())
	}
}

func TestCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/check" {
			t.Errorf("path = %q", r.URL.Path)
		}
		writeEnvelope(w, true, map[string]any{})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	if err := c.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
}

func TestPostJSONBody(t *testing.T) {
	type payload struct {
		Enabled bool `json:"enabled"`
	}
	var got payload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		writeEnvelope(w, true, map[string]any{"ok": true})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	var out map[string]any
	if err := c.Post("/api/hid/set_params", payload{Enabled: true}, &out); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if !got.Enabled {
		t.Fatalf("server did not receive expected body: %+v", got)
	}
}

func TestPostFormContentType(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("ParseForm: %v", err)
		}
		got = r.PostForm
		writeEnvelope(w, true, map[string]any{})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	form := url.Values{"foo": {"bar"}}
	if err := c.PostForm("/api/2fa/create", form, nil); err != nil {
		t.Fatalf("PostForm: %v", err)
	}
	if got.Get("foo") != "bar" {
		t.Fatalf("form not received: %v", got)
	}
}

func TestGetRawJPEGPassthrough(t *testing.T) {
	jpeg := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0xFF, 0xD9}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/streamer/snapshot" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpeg)
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	data, contentType, err := c.GetRaw("/api/streamer/snapshot")
	if err != nil {
		t.Fatalf("GetRaw: %v", err)
	}
	if contentType != "image/jpeg" {
		t.Errorf("contentType = %q", contentType)
	}
	if string(data) != string(jpeg) {
		t.Fatalf("raw bytes mismatch: %v", data)
	}
}

func TestGetRawError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		writeEnvelope(w, false, map[string]any{"error": "Forbidden", "error_msg": "denied"})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	_, _, err := c.GetRaw("/api/streamer/snapshot")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Code != "Forbidden" {
		t.Errorf("Code = %q", apiErr.Code)
	}
}

func TestRetryOn5xxThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "boom")
			return
		}
		writeEnvelope(w, true, map[string]any{"model": "RM1PE"})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	var v UpgradeVersion
	if err := c.Get("/api/upgrade/version", &v); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
	if v.Model != "RM1PE" {
		t.Fatalf("unexpected result: %+v", v)
	}
}

func TestRetryExhausted(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "upstream down")
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	c.maxRetries = 2 // 1 initial + 2 retries

	err := c.Get("/api/info", nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Status != http.StatusBadGateway {
		t.Errorf("Status = %d", apiErr.Status)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
}

func TestRetryOnTransportError(t *testing.T) {
	var calls int32
	c := newTestClient(t, "https://example.invalid")
	c.httpClient.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if atomic.AddInt32(&calls, 1) <= 2 {
			return nil, errors.New("temporary network failure")
		}
		return jsonResponse(http.StatusOK, `{"ok":true,"result":{"model":"RM1PE"}}`), nil
	})

	var v UpgradeVersion
	if err := c.Get("/api/upgrade/version", &v); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("calls = %d, want 3", got)
	}
	if v.Model != "RM1PE" {
		t.Fatalf("unexpected result: %+v", v)
	}
}

func TestContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err := c.GetContext(ctx, "/api/info", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestContextAlreadyCancelled(t *testing.T) {
	c := newTestClient(t, "https://example.invalid")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.GetContext(ctx, "/api/info", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestAuthQueryParam(t *testing.T) {
	c := NewClient("https://example.com", "u", "p", Options{})
	c.SetToken("abc123")
	if got := c.AuthQueryParam(); got != "auth_token=abc123" {
		t.Fatalf("AuthQueryParam() = %q", got)
	}
	if got := NewClient("https://example.com", "u", "p", Options{}).AuthQueryParam(); got != "auth_token=" {
		t.Fatalf("empty token: %q", got)
	}
}

func TestInsecureTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, true, map[string]any{"model": "RM1PE"})
	}))
	defer srv.Close()

	secure := NewClient(srv.URL, "u", "p", Options{})
	secure.minInterval = 0
	secure.retryWait = time.Millisecond
	secure.maxRetries = 0
	if err := secure.Get("/api/upgrade/version", nil); err == nil {
		t.Fatal("expected TLS verification failure with default options")
	}

	insecure := NewClient(srv.URL, "u", "p", Options{Insecure: true})
	insecure.minInterval = 0
	insecure.retryWait = time.Millisecond
	var v UpgradeVersion
	if err := insecure.Get("/api/upgrade/version", &v); err != nil {
		t.Fatalf("insecure Get: %v", err)
	}
	if v.Model != "RM1PE" {
		t.Fatalf("unexpected result: %+v", v)
	}
}

func TestBaseURLNormalization(t *testing.T) {
	cases := map[string]string{
		"glkvm.example.com":          "https://glkvm.example.com",
		"https://glkvm.example.com/": "https://glkvm.example.com",
		"http://glkvm.example.com":   "http://glkvm.example.com",
	}
	for in, want := range cases {
		if got := NewClient(in, "u", "p", Options{}).BaseURL(); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDebugLogger(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, true, map[string]any{})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	var logged []string
	c.SetDebug(func(format string, args ...any) {
		logged = append(logged, format)
	})
	if err := c.Get("/api/auth/check", nil); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(logged) == 0 {
		t.Fatal("expected debug calls")
	}
}

func TestRateLimitPacing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeEnvelope(w, true, map[string]any{})
	}))
	defer srv.Close()

	c := newTestClient(t, srv.URL)
	c.minInterval = 30 * time.Millisecond

	start := time.Now()
	if err := c.Get("/api/auth/check", nil); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if err := c.Get("/api/auth/check", nil); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
		t.Fatalf("requests were not paced: elapsed %v", elapsed)
	}
}

func TestAPIErrorMessage(t *testing.T) {
	e := &APIError{Code: "ValidatorError", Message: "bad", Status: 400}
	if !strings.Contains(e.Error(), "ValidatorError") || !strings.Contains(e.Error(), "400") {
		t.Fatalf("Error() = %q", e.Error())
	}
	if !strings.Contains((&APIError{Code: "X"}).Error(), "X") {
		t.Fatalf("Error() without message = %q", (&APIError{Code: "X"}).Error())
	}
	if (&APIError{Status: 401}).IsUnauthorized() != true {
		t.Fatal("IsUnauthorized should be true")
	}
}

func TestTypeDecoding(t *testing.T) {
	t.Run("info retains raw", func(t *testing.T) {
		var info Info
		body := `{"auth":{"enabled":true},"extras":{"ipmi":{"port":623}}}`
		if err := json.Unmarshal([]byte(body), &info); err != nil {
			t.Fatal(err)
		}
		if info.Auth["enabled"] != true {
			t.Fatalf("auth not decoded: %+v", info)
		}
		if _, ok := info.Raw["extras"]; !ok {
			t.Fatalf("Raw missing keys: %+v", info.Raw)
		}
	})

	t.Run("capability nested", func(t *testing.T) {
		var c Capability
		body := `{"capability":{"cpu_model":"rv1126b"},"success":true}`
		if err := json.Unmarshal([]byte(body), &c); err != nil {
			t.Fatal(err)
		}
		if !c.Success || c.Capability["cpu_model"] != "rv1126b" {
			t.Fatalf("decoded %+v", c)
		}
		if c.Raw["capability"] == nil {
			t.Fatalf("Raw missing: %+v", c.Raw)
		}
	})

	t.Run("hostname object and string", func(t *testing.T) {
		var h Hostname
		if err := json.Unmarshal([]byte(`{"hostname":"glkvm","success":true}`), &h); err != nil {
			t.Fatal(err)
		}
		if h.Hostname != "glkvm" || !h.Success {
			t.Fatalf("object form: %+v", h)
		}
		h = Hostname{}
		if err := json.Unmarshal([]byte(`"glkvm"`), &h); err != nil {
			t.Fatal(err)
		}
		if h.Hostname != "glkvm" {
			t.Fatalf("string form: %+v", h)
		}
	})

	t.Run("otg flat object", func(t *testing.T) {
		var o OTGFunctions
		body := `{"applying":false,"enable_camera":false,"enable_keyboard":true,"enable_mouse":true,"ready":true,"start_cdrom":false}`
		if err := json.Unmarshal([]byte(body), &o); err != nil {
			t.Fatal(err)
		}
		if !o.EnableKeyboard || !o.EnableMouse || !o.Ready || o.EnableCamera {
			t.Fatalf("decoded %+v", o)
		}
		if _, ok := o.Raw["enable_camera"]; !ok {
			t.Fatalf("Raw missing: %+v", o.Raw)
		}
	})

	t.Run("network config nested", func(t *testing.T) {
		var n NetworkConfig
		body := `{"config":{"dns_servers":["192.168.1.1"],"gateway":"192.168.1.1","interface":"eth0","ip_address":"192.168.1.176","is_dhcp":true,"mac_address":"94:83:C4:C4:A2:7D","netmask":"255.255.252.0","state":"online"},"success":true}`
		if err := json.Unmarshal([]byte(body), &n); err != nil {
			t.Fatal(err)
		}
		if n.Config.IPAddress != "192.168.1.176" || n.Config.IsDHCP == nil || !*n.Config.IsDHCP {
			t.Fatalf("decoded %+v", n)
		}
		if !n.Success || n.Config.Raw["state"] != "online" {
			t.Fatalf("raw/success not decoded: %+v", n)
		}
	})

	t.Run("wol list object and array", func(t *testing.T) {
		var l WOLList
		if err := json.Unmarshal([]byte(`{"devices":[{"name":"host","mac_address":"AA:BB"}]}`), &l); err != nil {
			t.Fatal(err)
		}
		if len(l.Devices) != 1 || l.Devices[0].MACAddress() != "AA:BB" {
			t.Fatalf("object form: %+v", l)
		}
		var l2 WOLList
		if err := json.Unmarshal([]byte(`[{"name":"x"}]`), &l2); err != nil {
			t.Fatal(err)
		}
		if len(l2.Devices) != 1 {
			t.Fatalf("array form: %+v", l2)
		}
	})
}

func TestMarshalJSONBodyError(t *testing.T) {
	c := newTestClient(t, "https://example.invalid")
	// A channel cannot be marshalled to JSON.
	if err := c.Post("/api/x", make(chan int), nil); err == nil {
		t.Fatal("expected marshal error")
	}
}
