package auth

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- test helpers ---------------------------------------------------------

var credEnvKeys = []string{
	"KVM_URL", "GLKVM_URL", "KVM_USERNAME", "GLKVM_USERNAME",
	"KVM_PASSWORD", "GLKVM_PASSWORD", "KVM_TIMEOUT", "GLKVM_TIMEOUT",
	"KVM_INSECURE", "GLKVM_INSECURE", "KVM_TLS_STRICT", "GLKVM_TLS_STRICT",
	"KVM_CONFIG", "GLKVM_CONFIG",
}

// isolate clears credential env, points HOME at a fresh temp dir and disables
// interactive prompting. It returns the temp HOME.
func isolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range credEnvKeys {
		t.Setenv(k, "")
	}
	old := interactivePrompt
	interactivePrompt = func() bool { return false }
	t.Cleanup(func() { interactivePrompt = old })
	return home
}

// noGopass points PATH at an empty directory so gopass lookups resolve to
// nothing.
func noGopass(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// fakeGopass installs a stub `gopass` binary on PATH. values maps bare entry
// names (e.g. "GLKVM_URL") to outputs; unknown entries exit non-zero.
func fakeGopass(t *testing.T, values map[string]string) {
	t.Helper()
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("case \"$3\" in\n")
	for k, v := range values {
		fmt.Fprintf(&b, "\tenv/%s) printf %%s %s ;;\n", k, shellQuote(v))
	}
	b.WriteString("\t*) exit 1 ;;\n")
	b.WriteString("esac\n")

	path := filepath.Join(dir, "gopass")
	if err := os.WriteFile(path, []byte(b.String()), 0o755); err != nil {
		t.Fatalf("write fake gopass: %v", err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod fake gopass: %v", err)
	}
	t.Setenv("PATH", dir)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// loginHandler serves POST /api/auth/login with a standard envelope.
func loginHandler(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/login" {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		if r.PostFormValue("user") == "" || r.PostFormValue("passwd") == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"ok":false,"result":{"error":"bad_request","error_msg":"missing credentials"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     true,
			"result": map[string]any{"token": token},
		})
	}
}

func writeRawConfig(t *testing.T, content string) {
	t.Helper()
	path, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// --- Resolve priority chain ----------------------------------------------

func TestResolveFlagPriority(t *testing.T) {
	isolate(t)
	fakeGopass(t, map[string]string{
		"GLKVM_URL": "gopass-url", "GLKVM_USERNAME": "gopass-user", "GLKVM_PASSWORD": "gopass-pass",
	})
	if err := SaveConfig(&Config{URL: "cfg-url", Username: "cfg-user", Password: "cfg-pass"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KVM_URL", "env-url")
	t.Setenv("KVM_USERNAME", "env-user")
	t.Setenv("KVM_PASSWORD", "env-pass")

	creds, err := Resolve("flag-url", "flag-user", "flag-pass")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.URL != "flag-url" || creds.Username != "flag-user" || creds.Password != "flag-pass" {
		t.Fatalf("flags must win: %+v", creds)
	}
}

func TestResolveEnvPriority(t *testing.T) {
	isolate(t)
	fakeGopass(t, map[string]string{
		"GLKVM_URL": "gopass-url", "GLKVM_USERNAME": "gopass-user", "GLKVM_PASSWORD": "gopass-pass",
	})
	if err := SaveConfig(&Config{URL: "cfg-url", Username: "cfg-user", Password: "cfg-pass"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KVM_URL", "env-url")
	t.Setenv("KVM_USERNAME", "env-user")
	t.Setenv("KVM_PASSWORD", "env-pass")

	creds, err := Resolve("", "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.URL != "env-url" || creds.Username != "env-user" || creds.Password != "env-pass" {
		t.Fatalf("env must beat gopass/config: %+v", creds)
	}
}

func TestResolveKVMEnvBeatsAlias(t *testing.T) {
	isolate(t)
	noGopass(t)
	t.Setenv("KVM_URL", "kvm-url")
	t.Setenv("GLKVM_URL", "glkvm-url")

	creds, err := Resolve("", "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.URL != "kvm-url" {
		t.Fatalf("KVM_URL should beat GLKVM_URL, got %q", creds.URL)
	}

	// Alias is used when the primary is unset.
	t.Setenv("KVM_URL", "")
	if creds, _ = Resolve("", "", ""); creds.URL != "glkvm-url" {
		t.Fatalf("GLKVM_URL alias not honoured, got %q", creds.URL)
	}
}

func TestResolveGopassPriority(t *testing.T) {
	isolate(t)
	fakeGopass(t, map[string]string{
		"GLKVM_URL": "gopass-url", "GLKVM_USERNAME": "gopass-user", "GLKVM_PASSWORD": "gopass-pass",
	})
	if err := SaveConfig(&Config{URL: "cfg-url", Username: "cfg-user", Password: "cfg-pass"}); err != nil {
		t.Fatal(err)
	}

	creds, err := Resolve("", "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.URL != "gopass-url" || creds.Username != "gopass-user" || creds.Password != "gopass-pass" {
		t.Fatalf("gopass must beat config: %+v", creds)
	}
}

func TestResolveConfigFallback(t *testing.T) {
	isolate(t)
	noGopass(t)
	if err := SaveConfig(&Config{URL: "https://cfg", Username: "cfg-user", Password: "cfg-pass"}); err != nil {
		t.Fatal(err)
	}

	creds, err := Resolve("", "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.URL != "https://cfg" || creds.Username != "cfg-user" || creds.Password != "cfg-pass" {
		t.Fatalf("config fallback failed: %+v", creds)
	}
}

func TestResolveGopassEntryMissingFallsThrough(t *testing.T) {
	isolate(t)
	// gopass exists but only knows an unrelated entry.
	fakeGopass(t, map[string]string{"SOMETHING_ELSE": "x"})
	if err := SaveConfig(&Config{URL: "https://cfg", Username: "cfg-user", Password: "cfg-pass"}); err != nil {
		t.Fatal(err)
	}

	creds, err := Resolve("", "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.URL != "https://cfg" || creds.Username != "cfg-user" || creds.Password != "cfg-pass" {
		t.Fatalf("expected config fallback when gopass entry absent: %+v", creds)
	}
}

func TestResolveMissingIsNotAnError(t *testing.T) {
	isolate(t)
	noGopass(t)

	creds, err := Resolve("", "", "")
	if err != nil {
		t.Fatalf("Resolve with no sources must not error: %v", err)
	}
	if creds.URL != "" || creds.Username != "" || creds.Password != "" {
		t.Fatalf("expected empty credentials, got %+v", creds)
	}
	if creds.Timeout != defaultTimeout {
		t.Fatalf("timeout = %v, want default %v", creds.Timeout, defaultTimeout)
	}
	if creds.Insecure {
		t.Fatal("insecure should default to false")
	}
}

func TestResolveTimeoutAndInsecureFromConfig(t *testing.T) {
	isolate(t)
	noGopass(t)
	if err := SaveConfig(&Config{Timeout: "45s", Insecure: true}); err != nil {
		t.Fatal(err)
	}

	creds, err := Resolve("", "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.Timeout != 45*time.Second {
		t.Fatalf("timeout = %v, want 45s", creds.Timeout)
	}
	if !creds.Insecure {
		t.Fatal("insecure should be true from config")
	}

	// Environment overrides config.
	t.Setenv("KVM_TIMEOUT", "5s")
	t.Setenv("KVM_INSECURE", "false")
	creds, err = Resolve("", "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.Timeout != 5*time.Second {
		t.Fatalf("timeout = %v, want 5s (env)", creds.Timeout)
	}
	if creds.Insecure {
		t.Fatal("insecure should be false from env")
	}
}

// --- config read/write ----------------------------------------------------

func TestSaveLoadConfigRoundTrip(t *testing.T) {
	home := isolate(t)
	cfg := &Config{URL: "https://x", Username: "admin", Password: "pw", Timeout: "45s", Insecure: true}
	if err := SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}

	path, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if !strings.HasPrefix(path, home) {
		t.Fatalf("config path %q not under HOME %q", path, home)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("config mode = %o, want 0600", perm)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := di.Mode().Perm(); perm != 0o700 {
		t.Fatalf("config dir mode = %o, want 0700", perm)
	}

	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if *got != *cfg {
		t.Fatalf("round trip mismatch: got %+v want %+v", *got, *cfg)
	}
}

func TestLoadConfigMissing(t *testing.T) {
	isolate(t)
	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if *got != (Config{}) {
		t.Fatalf("expected empty config, got %+v", *got)
	}
}

func TestConfigPathOverride(t *testing.T) {
	isolate(t)
	custom := filepath.Join(t.TempDir(), "custom.json")
	t.Setenv("KVM_CONFIG", custom)

	got, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}
	if got != custom {
		t.Fatalf("ConfigPath = %q, want %q", got, custom)
	}
}

func TestLoadConfigFlexibleInsecure(t *testing.T) {
	isolate(t)
	writeRawConfig(t, `{"url":"https://x","insecure":"true","timeout":"10s"}`)

	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !got.Insecure {
		t.Fatal("string \"true\" should decode as insecure=true")
	}
	if got.URL != "https://x" || got.Timeout != "10s" {
		t.Fatalf("unexpected config: %+v", *got)
	}

	writeRawConfig(t, `{"insecure":false}`)
	got, err = LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.Insecure {
		t.Fatal("bool false should decode as insecure=false")
	}
}

func TestSetConfigValue(t *testing.T) {
	isolate(t)
	if err := SetConfigValue("url", "https://x"); err != nil {
		t.Fatalf("SetConfigValue url: %v", err)
	}
	if err := SetConfigValue("username", "admin"); err != nil {
		t.Fatalf("SetConfigValue username: %v", err)
	}
	if err := SetConfigValue("timeout", "10s"); err != nil {
		t.Fatalf("SetConfigValue timeout: %v", err)
	}
	if err := SetConfigValue("insecure", "true"); err != nil {
		t.Fatalf("SetConfigValue insecure: %v", err)
	}

	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.URL != "https://x" || got.Username != "admin" || got.Timeout != "10s" || !got.Insecure {
		t.Fatalf("unexpected config: %+v", *got)
	}

	m, err := ListConfig()
	if err != nil {
		t.Fatalf("ListConfig: %v", err)
	}
	if m["url"] != "https://x" || m["username"] != "admin" || m["timeout"] != "10s" || m["insecure"] != "true" {
		t.Fatalf("unexpected ListConfig: %v", m)
	}
}

func TestSetConfigValueInvalid(t *testing.T) {
	isolate(t)
	if err := SetConfigValue("nope", "x"); err == nil {
		t.Fatal("expected error for unknown key")
	}
	if err := SetConfigValue("timeout", "notaduration"); err == nil {
		t.Fatal("expected error for invalid timeout")
	}
	if err := SetConfigValue("insecure", "maybe"); err == nil {
		t.Fatal("expected error for invalid insecure")
	}
}

func TestConfigMalformed(t *testing.T) {
	isolate(t)
	writeRawConfig(t, "{not valid json")

	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected error for malformed config")
	}
	if _, err := Resolve("", "", ""); err == nil {
		t.Fatal("expected Resolve to surface malformed config")
	}
}

// --- session persistence --------------------------------------------------

func TestSaveLoadSession(t *testing.T) {
	home := isolate(t)
	if err := SaveSession("tok123"); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}

	path, err := SessionPath()
	if err != nil {
		t.Fatalf("SessionPath: %v", err)
	}
	if !strings.HasPrefix(path, home) {
		t.Fatalf("session path %q not under HOME %q", path, home)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat session: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("session mode = %o, want 0600", perm)
	}

	s, err := LoadSession()
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if s.Token != "tok123" {
		t.Fatalf("token = %q, want tok123", s.Token)
	}
	if s.SavedAt.IsZero() {
		t.Fatal("SavedAt should be populated")
	}
}

func TestLoadSessionMissing(t *testing.T) {
	isolate(t)
	_, err := LoadSession()
	if err == nil {
		t.Fatal("expected error for missing session")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error %v should wrap os.ErrNotExist", err)
	}
}

// --- client construction --------------------------------------------------

func TestGetClientMissingFields(t *testing.T) {
	isolate(t)
	cases := []struct {
		name  string
		creds Credentials
		want  string
	}{
		{"url", Credentials{}, "URL"},
		{"username", Credentials{URL: "https://x"}, "username"},
		{"password", Credentials{URL: "https://x", Username: "u"}, "password"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := GetClient(tc.creds)
			if err == nil {
				t.Fatalf("expected error mentioning %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestIsCertError(t *testing.T) {
	positives := []error{
		x509.UnknownAuthorityError{},
		x509.CertificateInvalidError{Reason: x509.Expired},
		fmt.Errorf("login: %w", x509.HostnameError{Host: "x"}),
		&tls.CertificateVerificationError{},
		errors.New("tls: failed to verify certificate: x509: certificate has expired"),
	}
	for _, err := range positives {
		if !isCertError(err) {
			t.Fatalf("expected cert error for %v", err)
		}
	}
	if isCertError(nil) {
		t.Fatal("nil is not a cert error")
	}
	if isCertError(errors.New("connection refused")) {
		t.Fatal("connection refused is not a cert error")
	}
	if got := certReason(x509.CertificateInvalidError{Reason: x509.Expired}); !strings.Contains(got, "expired") {
		t.Fatalf("certReason = %q, want it to mention expiry", got)
	}
	if got := certReason(errors.New("boom")); got == "" {
		t.Fatal("certReason should be non-empty")
	}
}

func TestGetClientPlainSuccess(t *testing.T) {
	isolate(t)
	noGopass(t)
	srv := httptest.NewServer(loginHandler("plain-token"))
	defer srv.Close()

	client, err := GetClient(Credentials{URL: srv.URL, Username: "admin", Password: "pw"})
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if client.Token() != "plain-token" {
		t.Fatalf("token = %q, want plain-token", client.Token())
	}
}

func TestGetClientBadCredentials(t *testing.T) {
	isolate(t)
	noGopass(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":     false,
			"result": map[string]any{"error": "unauthorized", "error_msg": "invalid credentials"},
		})
	}))
	defer srv.Close()

	_, err := GetClient(Credentials{URL: srv.URL, Username: "a", Password: "b", Insecure: true})
	if err == nil {
		t.Fatal("expected login error")
	}
	if !strings.Contains(err.Error(), "login to") {
		t.Fatalf("error %q should mention login", err)
	}
}

func TestGetClientTLSFallback(t *testing.T) {
	isolate(t)
	noGopass(t)
	srv := httptest.NewTLSServer(loginHandler("tls-token"))
	defer srv.Close()

	// No Insecure flag: the client must discover the untrusted cert and fall
	// back automatically.
	client, err := GetClient(Credentials{URL: srv.URL, Username: "admin", Password: "pw"})
	if err != nil {
		t.Fatalf("expected insecure fallback to succeed, got %v", err)
	}
	if client.Token() != "tls-token" {
		t.Fatalf("token = %q, want tls-token", client.Token())
	}
}

func TestGetClientTLSStrict(t *testing.T) {
	isolate(t)
	noGopass(t)
	t.Setenv("KVM_TLS_STRICT", "1")

	srv := httptest.NewTLSServer(loginHandler("tls-token"))
	defer srv.Close()

	_, err := GetClient(Credentials{URL: srv.URL, Username: "admin", Password: "pw"})
	if err == nil {
		t.Fatal("expected an error under strict TLS")
	}
	if !isCertError(err) {
		t.Fatalf("expected cert error, got %v", err)
	}
}

func TestGetClientInsecureDirect(t *testing.T) {
	isolate(t)
	noGopass(t)
	srv := httptest.NewTLSServer(loginHandler("direct-token"))
	defer srv.Close()

	client, err := GetClient(Credentials{URL: srv.URL, Username: "admin", Password: "pw", Insecure: true})
	if err != nil {
		t.Fatalf("GetClient: %v", err)
	}
	if client.Token() != "direct-token" {
		t.Fatalf("token = %q, want direct-token", client.Token())
	}
}

func TestGetClientFromFlags(t *testing.T) {
	isolate(t)
	noGopass(t)
	srv := httptest.NewServer(loginHandler("flag-token"))
	defer srv.Close()

	client, err := GetClientFromFlags(srv.URL, "admin", "pw")
	if err != nil {
		t.Fatalf("GetClientFromFlags: %v", err)
	}
	if client.Token() != "flag-token" {
		t.Fatalf("token = %q, want flag-token", client.Token())
	}
}

// --- additional resolution branches --------------------------------------

func TestResolveTimeoutAndInsecureFromGopass(t *testing.T) {
	isolate(t)
	fakeGopass(t, map[string]string{
		"GLKVM_TIMEOUT":  "12s",
		"GLKVM_INSECURE": "true",
	})

	creds, err := Resolve("", "", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if creds.Timeout != 12*time.Second {
		t.Fatalf("timeout = %v, want 12s (gopass)", creds.Timeout)
	}
	if !creds.Insecure {
		t.Fatal("insecure should be true from gopass")
	}
}

func TestTLSStrictParsing(t *testing.T) {
	isolate(t)
	noGopass(t)

	t.Setenv("KVM_TLS_STRICT", "maybe")
	if !tlsStrict() {
		t.Fatal("unparseable strict value must fail safe (true)")
	}
	t.Setenv("KVM_TLS_STRICT", "0")
	if tlsStrict() {
		t.Fatal("0 should be false")
	}
	t.Setenv("KVM_TLS_STRICT", "")
	if tlsStrict() {
		t.Fatal("empty should be false")
	}
}

func TestCertReasonBranches(t *testing.T) {
	if got := certReason(x509.CertificateInvalidError{Reason: x509.NameMismatch}); !strings.Contains(got, "different host") {
		t.Fatalf("NameMismatch reason = %q", got)
	}
	if got := certReason(x509.UnknownAuthorityError{}); !strings.Contains(got, "unknown authority") {
		t.Fatalf("UnknownAuthority reason = %q", got)
	}
	if got := certReason(errors.New("boom: x509: something bad\ntrailing")); !strings.Contains(got, "x509: something bad") {
		t.Fatalf("x509 string reason = %q", got)
	}
	if got := certReason(errors.New("plain")); got != "certificate verification error" {
		t.Fatalf("fallback reason = %q", got)
	}
}

// --- config coercion and I/O errors --------------------------------------

func TestConfigRawTypes(t *testing.T) {
	isolate(t)
	writeRawConfig(t, `{"url":123,"username":true,"timeout":30}`)
	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got.URL != "123" || got.Username != "true" || got.Timeout != "30" {
		t.Fatalf("raw decoding failed: %+v", *got)
	}

	writeRawConfig(t, `{"insecure":[]}`)
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected error for invalid insecure value")
	}
}

func TestSaveConfigNil(t *testing.T) {
	if err := SaveConfig(nil); err == nil {
		t.Fatal("expected error for nil config")
	}
}

func TestConfigIOErrors(t *testing.T) {
	isolate(t)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KVM_CONFIG", filepath.Join(blocker, "config.json"))

	if err := SaveConfig(&Config{URL: "x"}); err == nil {
		t.Fatal("expected mkdir error when parent is a file")
	}
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected read error when parent is a file")
	}
}

func TestListConfigEmpty(t *testing.T) {
	isolate(t)
	m, err := ListConfig()
	if err != nil {
		t.Fatalf("ListConfig: %v", err)
	}
	if len(m) != 0 {
		t.Fatalf("expected empty map, got %v", m)
	}
}

// --- session error paths --------------------------------------------------

func TestLoadSessionParseError(t *testing.T) {
	isolate(t)
	sp, err := SessionPath()
	if err != nil {
		t.Fatalf("SessionPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sp), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sp, []byte("{bad json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSession(); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoadSessionReadError(t *testing.T) {
	isolate(t)
	sp, err := SessionPath()
	if err != nil {
		t.Fatalf("SessionPath: %v", err)
	}
	// A directory at the session path yields a non-ErrNotExist read error.
	if err := os.MkdirAll(sp, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = LoadSession()
	if err == nil {
		t.Fatal("expected read error")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatal("read error should not be ErrNotExist")
	}
}

func TestSaveSessionError(t *testing.T) {
	isolate(t)
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KVM_CONFIG", filepath.Join(blocker, "config.json"))
	if err := SaveSession("tok"); err == nil {
		t.Fatal("expected mkdir error when parent is a file")
	}
}

// --- interactive prompt ---------------------------------------------------

func TestPromptLine(t *testing.T) {
	got, err := promptLine(bufio.NewReader(strings.NewReader("hello\n")), "X")
	if err != nil || got != "hello" {
		t.Fatalf("promptLine = %q, %v", got, err)
	}
	got, err = promptLine(bufio.NewReader(strings.NewReader("noline")), "X")
	if err != nil || got != "noline" {
		t.Fatalf("promptLine EOF = %q, %v", got, err)
	}
}

func TestIsTerminal(t *testing.T) {
	if isTerminal(nil) {
		t.Fatal("nil is not a TTY")
	}
	f, err := os.CreateTemp("", "authterm")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(f.Name()) }()
	defer func() { _ = f.Close() }()
	if isTerminal(f) {
		t.Fatal("regular file is not a TTY")
	}
}

// TestIsTerminalDevNull is the regression: /dev/null is a character
// device, so the old os.ModeCharDevice check wrongly reported it as a TTY.
func TestIsTerminalDevNull(t *testing.T) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	defer func() { _ = f.Close() }()
	if isTerminal(f) {
		t.Fatalf("%s must not be classified as a TTY", os.DevNull)
	}
}

// TestResolveDevNullStdinDoesNotPrompt reproduces the failure end to end:
// with stdin at /dev/null and no credentials anywhere, Resolve must not attempt
// an interactive prompt (which previously died with a read-password error). The
// actionable "no ... configured" error is produced later by GetClient.
func TestResolveDevNullStdinDoesNotPrompt(t *testing.T) {
	isolate(t)
	noGopass(t)

	// Use the real TTY probe so /dev/null is actually examined rather than the
	// deterministic test stub installed by isolate.
	oldPrompt := interactivePrompt
	interactivePrompt = func() bool { return isTerminal(os.Stdin) }
	t.Cleanup(func() { interactivePrompt = oldPrompt })

	withStdin(t, os.DevNull)

	creds, err := Resolve("", "", "")
	if err != nil {
		t.Fatalf("Resolve with /dev/null stdin must not error, got %v", err)
	}
	if creds.URL != "" || creds.Username != "" || creds.Password != "" {
		t.Fatalf("expected empty credentials, got %+v", creds)
	}

	_, err = GetClient(creds)
	if err == nil || !strings.Contains(err.Error(), "no KVM URL configured") {
		t.Fatalf("GetClient error = %v, want a 'no KVM URL configured' message", err)
	}
}

// withStdin temporarily points os.Stdin at a file and restores it.
func withStdin(t *testing.T, path string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = old
		_ = f.Close()
	})
}

func TestPromptMissingInteractive(t *testing.T) {
	isolate(t)
	interactivePrompt = func() bool { return true }

	in := filepath.Join(t.TempDir(), "in")
	if err := os.WriteFile(in, []byte("https://prompted\nprompted-user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withStdin(t, in)

	creds := Credentials{Password: "keepme"}
	if err := promptMissing(&creds); err != nil {
		t.Fatalf("promptMissing: %v", err)
	}
	if creds.URL != "https://prompted" || creds.Username != "prompted-user" {
		t.Fatalf("prompted creds = %+v", creds)
	}
	if creds.Password != "keepme" {
		t.Fatalf("password should be untouched, got %q", creds.Password)
	}
}

func TestPromptMissingAllSet(t *testing.T) {
	isolate(t)
	interactivePrompt = func() bool { return true }
	creds := Credentials{URL: "u", Username: "n", Password: "p"}
	if err := promptMissing(&creds); err != nil {
		t.Fatalf("promptMissing: %v", err)
	}
}

func TestPromptPasswordReadError(t *testing.T) {
	isolate(t)
	in := filepath.Join(t.TempDir(), "in")
	if err := os.WriteFile(in, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withStdin(t, in)

	if _, err := promptPassword("Password"); err == nil {
		t.Fatal("expected error reading a password from a non-TTY")
	}
}

// --- error propagation ----------------------------------------------------

func TestConfigDirNoHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")

	if _, err := ConfigDir(); err == nil {
		t.Fatal("expected error when HOME is unset")
	}
	if _, err := ConfigPath(); err == nil {
		t.Fatal("expected ConfigPath error when HOME is unset")
	}
	if _, err := LoadConfig(); err == nil {
		t.Fatal("expected LoadConfig error when HOME is unset")
	}
	if _, err := SessionPath(); err == nil {
		t.Fatal("expected SessionPath error when HOME is unset")
	}
	if err := SaveConfig(&Config{}); err == nil {
		t.Fatal("expected SaveConfig error when HOME is unset")
	}
	if err := SaveSession("x"); err == nil {
		t.Fatal("expected SaveSession error when HOME is unset")
	}
	if _, err := ListConfig(); err == nil {
		t.Fatal("expected ListConfig error when HOME is unset")
	}
	if err := SetConfigValue("url", "x"); err == nil {
		t.Fatal("expected SetConfigValue error when HOME is unset")
	}
}

func TestGetClientFromFlagsResolveError(t *testing.T) {
	isolate(t)
	writeRawConfig(t, "{bad json")
	if _, err := GetClientFromFlags("https://x", "u", "p"); err == nil {
		t.Fatal("expected Resolve error to propagate")
	}
}

func TestSetConfigValueConfigError(t *testing.T) {
	isolate(t)
	writeRawConfig(t, "{bad json")
	if err := SetConfigValue("url", "x"); err == nil {
		t.Fatal("expected error loading malformed config")
	}
}

func TestResolvePromptError(t *testing.T) {
	isolate(t)
	noGopass(t)
	interactivePrompt = func() bool { return true }

	in := filepath.Join(t.TempDir(), "in")
	if err := os.WriteFile(in, []byte("https://prompted\nprompted-user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	withStdin(t, in)

	// Password cannot be read from a non-TTY fd, so Resolve must surface the
	// prompt error rather than silently returning partial credentials.
	if _, err := Resolve("", "", ""); err == nil {
		t.Fatal("expected prompt error to propagate from Resolve")
	}
}

// captureStderrPipe redirects os.Stderr for the duration of fn and returns what
// was written. It is used to assert that pkg/auth's informational warnings are
// redacted.
func captureStderrPipe(t *testing.T, fn func()) string {
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

// TestGetClientRedactsURLUserInfoInLoginError is the N3 regression: the
// "login to <url> failed" prefix must not leak URL userinfo.
func TestGetClientRedactsURLUserInfoInLoginError(t *testing.T) {
	isolate(t)
	noGopass(t)
	_, err := GetClient(Credentials{
		URL:      "http://user:s3cr3t@127.0.0.1:1",
		Username: "admin",
		Password: "pw",
		Insecure: true,
		Timeout:  time.Second,
	})
	if err == nil {
		t.Fatal("expected a login/transport error for the dead URL")
	}
	msg := err.Error()
	if strings.Contains(msg, "s3cr3t") || strings.Contains(msg, "user:") {
		t.Fatalf("login error leaked URL userinfo: %q", msg)
	}
	if !strings.Contains(msg, "login to http://***@127.0.0.1:1 failed") {
		t.Fatalf("login error did not mask the URL: %q", msg)
	}
}

// TestGetClientTLSWarningRedactsURLUserInfo is the N6 regression: the
// TLS-fallback warning must mask URL userinfo.
func TestGetClientTLSWarningRedactsURLUserInfo(t *testing.T) {
	isolate(t)
	noGopass(t)
	srv := httptest.NewTLSServer(loginHandler("tls-token"))
	defer srv.Close()

	withInfo := strings.Replace(srv.URL, "https://", "https://user:s3cr3t@", 1)
	stderr := captureStderrPipe(t, func() {
		client, err := GetClient(Credentials{URL: withInfo, Username: "admin", Password: "pw"})
		if err != nil {
			t.Fatalf("expected insecure fallback to succeed, got %v", err)
		}
		if client.Token() != "tls-token" {
			t.Fatalf("token = %q, want tls-token", client.Token())
		}
	})
	if strings.Contains(stderr, "s3cr3t") {
		t.Fatalf("TLS warning leaked URL userinfo: %q", stderr)
	}
	if !strings.Contains(stderr, "TLS certificate verification failed") {
		t.Fatalf("expected the TLS warning, got stderr: %q", stderr)
	}
	if !strings.Contains(stderr, "https://***@") {
		t.Fatalf("TLS warning did not mask the URL: %q", stderr)
	}
}
