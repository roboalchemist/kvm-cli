package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// testMaskURL is a distinctive userinfo-bearing URL used to prove that no site
// leaks the credentials.
const (
	testMaskUser = "testMaskUserLower"
	testMaskPass = "s3cr3t"
)

func testMaskURL(raw string) string {
	if strings.HasPrefix(raw, "https://") {
		return "https://" + testMaskUser + ":" + testMaskPass + "@" + strings.TrimPrefix(raw, "https://")
	}
	return "http://" + testMaskUser + ":" + testMaskPass + "@" + strings.TrimPrefix(raw, "http://")
}

func assertMasked(t *testing.T, label, out string) {
	t.Helper()
	for _, bad := range []string{testMaskPass, testMaskUser + ":"} {
		if strings.Contains(out, bad) {
			t.Fatalf("%s leaked URL userinfo (%q):\n%s", label, bad, out)
		}
	}
	if !strings.Contains(out, "://***@") {
		t.Fatalf("%s did not mask the URL userinfo:\n%s", label, out)
	}
}

// TestAuthStatusRedactsURLUserInfo is the N4 regression: 'auth status' must
// not print the raw url field, the raw table URL, or a raw login error.
func TestAuthStatusRedactsURLUserInfo(t *testing.T) {
	saveGlobals(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")
	t.Setenv("PATH", t.TempDir()) // no gopass lookups

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"result":{"error":"unauthorized","error_msg":"invalid credentials"}}`))
	}))
	defer srv.Close()

	t.Setenv("KVM_URL", testMaskURL(srv.URL))
	t.Setenv("KVM_USERNAME", "admin")
	t.Setenv("KVM_PASSWORD", "pw")

	flagFormat, flagJSON, flagPlaintext = "table", true, false
	out := captureStdout(t, func() {
		if err := runAuthStatus(authStatusCmd, nil); err != nil {
			t.Fatalf("runAuthStatus: %v", err)
		}
	})
	assertMasked(t, "auth status --json", out)

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("auth status --json is not valid JSON: %v\n%s", err, out)
	}
	if got["url"] == testMaskURL(srv.URL) {
		t.Fatalf("auth status --json url was not masked: %v", got["url"])
	}

	// Table mode too.
	flagFormat, flagJSON, flagPlaintext = "table", false, false
	out = captureStdout(t, func() {
		if err := runAuthStatus(authStatusCmd, nil); err != nil {
			t.Fatalf("runAuthStatus (table): %v", err)
		}
	})
	assertMasked(t, "auth status (table)", out)
}

// TestAuthLoginRedactsURLUserInfo covers the url field/row of 'auth login'.
func TestAuthLoginRedactsURLUserInfo(t *testing.T) {
	saveGlobals(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")
	t.Setenv("PATH", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"token":"tok"}}`))
	}))
	defer srv.Close()

	t.Setenv("KVM_URL", testMaskURL(srv.URL))
	t.Setenv("KVM_USERNAME", "admin")
	t.Setenv("KVM_PASSWORD", "pw")

	flagFormat, flagJSON, flagPlaintext = "table", true, false
	out := captureStdout(t, func() {
		if err := runAuthLogin(authLoginCmd, nil); err != nil {
			t.Fatalf("runAuthLogin: %v", err)
		}
	})
	assertMasked(t, "auth login --json", out)
}

// TestConfigGetListRedactsURLUserInfo is the N5 regression: 'config get url'
// and 'config list' must mask userinfo while leaving ordinary URLs unmasked.
func TestConfigGetListRedactsURLUserInfo(t *testing.T) {
	saveGlobals(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")

	_ = captureStdout(t, func() {
		if err := runConfigSet(nil, []string{"url", "https://" + testMaskUser + ":" + testMaskPass + "@glkvm.example.com"}); err != nil {
			t.Fatalf("config set url (userinfo): %v", err)
		}
	})

	// config list --json.
	flagFormat, flagJSON, flagPlaintext = "table", true, false
	out := captureStdout(t, func() {
		if err := runConfigList(nil, nil); err != nil {
			t.Fatalf("config list: %v", err)
		}
	})
	assertMasked(t, "config list --json", out)
	var listed map[string]string
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("config list --json is not valid JSON: %v\n%s", err, out)
	}
	if listed["url"] != "https://***@glkvm.example.com" {
		t.Fatalf("config list url = %q, want masked", listed["url"])
	}

	// config get url --json.
	out = captureStdout(t, func() {
		if err := runConfigGet(nil, []string{"url"}); err != nil {
			t.Fatalf("config get url: %v", err)
		}
	})
	assertMasked(t, "config get url --json", out)

	// config get url in the default (table) mode prints just the value.
	flagFormat, flagJSON, flagPlaintext = "table", false, false
	out = captureStdout(t, func() {
		if err := runConfigGet(nil, []string{"url"}); err != nil {
			t.Fatalf("config get url (table): %v", err)
		}
	})
	assertMasked(t, "config get url (table)", out)

	// A plain URL remains unmasked.
	_ = captureStdout(t, func() {
		if err := runConfigSet(nil, []string{"url", "https://glkvm.example.com"}); err != nil {
			t.Fatalf("config set url (plain): %v", err)
		}
	})
	out = captureStdout(t, func() {
		if err := runConfigGet(nil, []string{"url"}); err != nil {
			t.Fatalf("config get url (plain): %v", err)
		}
	})
	if strings.Contains(out, "***") {
		t.Fatalf("plain URL was masked: %q", out)
	}
	if !strings.Contains(out, "https://glkvm.example.com") {
		t.Fatalf("plain URL not printed verbatim: %q", out)
	}
}

// TestSkillAddWarnsOnExistingDir is the N7 regression: 'skill add' warns
// before overwriting an existing skill directory, and --force silences it.
func TestSkillAddWarnsOnExistingDir(t *testing.T) {
	saveGlobals(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	oldFS, oldForce := embeddedSkillFS, skillForce
	embeddedSkillFS = fstest.MapFS{
		"skill/SKILL.md":              {Data: []byte("name: kvm-cli\n")},
		"skill/reference/commands.md": {Data: []byte("commands\n")},
	}
	t.Cleanup(func() { embeddedSkillFS, skillForce = oldFS, oldForce })

	dest := skillInstallDir()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}

	// Default: a warning is printed before overwriting.
	skillForce = false
	stderr := captureStderr(t, func() {
		if err := runSkillAdd(skillAddCmd, nil); err != nil {
			t.Fatalf("runSkillAdd: %v", err)
		}
	})
	if !strings.Contains(stderr, "already exists") {
		t.Fatalf("skill add did not warn about the existing dir:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Installed skill to") {
		t.Fatalf("skill add did not install:\n%s", stderr)
	}
	if _, err := os.Stat(dest + "/SKILL.md"); err != nil {
		t.Fatalf("skill add did not write SKILL.md: %v", err)
	}

	// --force suppresses the warning.
	skillForce = true
	stderr = captureStderr(t, func() {
		if err := runSkillAdd(skillAddCmd, nil); err != nil {
			t.Fatalf("runSkillAdd --force: %v", err)
		}
	})
	if strings.Contains(stderr, "already exists") {
		t.Fatalf("--force did not silence the warning:\n%s", stderr)
	}

	// The flag is wired: 'skill add' advertises --force.
	if f := skillAddCmd.Flags().Lookup("force"); f == nil {
		t.Fatal("skill add is missing the --force flag")
	}
}

// TestShotFetchErrorRedactsURLUserInfo covers the raw snapshot GET path:
// a transport error embeds the full URL (with userinfo) and must be redacted.
func TestShotFetchErrorRedactsURLUserInfo(t *testing.T) {
	_, _, _, err := shotFetchOnce("http://"+testMaskUser+":"+testMaskPass+"@127.0.0.1:1", "tok", time.Second)
	if err == nil {
		t.Fatal("expected a transport error for the dead URL")
	}
	if strings.Contains(err.Error(), testMaskPass) || strings.Contains(err.Error(), testMaskUser+":") {
		t.Fatalf("shotFetchOnce leaked URL userinfo: %v", err)
	}
}

// TestAuthLogoutAndCheckRedactURLUserInfo covers the url field/row of
// 'auth logout' and 'auth check'.
func TestAuthLogoutAndCheckRedactURLUserInfo(t *testing.T) {
	saveGlobals(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")
	t.Setenv("PATH", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth/login":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"token":"tok"}}`))
		case "/api/auth/logout", "/api/auth/check":
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("KVM_URL", testMaskURL(srv.URL))
	t.Setenv("KVM_USERNAME", "admin")
	t.Setenv("KVM_PASSWORD", "pw")

	flagFormat, flagJSON, flagPlaintext = "table", true, false
	out := captureStdout(t, func() {
		if err := runAuthLogout(authLogoutCmd, nil); err != nil {
			t.Fatalf("runAuthLogout: %v", err)
		}
	})
	assertMasked(t, "auth logout --json", out)

	out = captureStdout(t, func() {
		if err := runAuthCheck(authCheckCmd, nil); err != nil {
			t.Fatalf("runAuthCheck: %v", err)
		}
	})
	assertMasked(t, "auth check --json", out)
}
