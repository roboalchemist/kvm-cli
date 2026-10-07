package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Distinctive marker so a leak is unambiguous in the rendered output.
const testDryRunSecret = "DRYRUN_SECRET_VALUE"

// TestDryRunConfigSetMasksSecretValue is the D8 acceptance: the positional
// value of 'config set password <value>' is masked in the --dry-run preview in
// both table and JSON modes.
func TestDryRunConfigSetMasksSecretValue(t *testing.T) {
	saveGlobals(t)

	modes := []struct {
		name  string
		setup func()
	}{
		{"table", func() { flagFormat, flagJSON, flagPlaintext = "table", false, false }},
		{"json", func() { flagFormat, flagJSON, flagPlaintext = "table", true, false }},
	}
	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			mode.setup()
			out := captureStdout(t, func() {
				if err := renderDryRunPreview(configSetCmd, []string{"password", testDryRunSecret}); err != nil {
					t.Fatalf("renderDryRunPreview: %v", err)
				}
			})
			if strings.Contains(out, testDryRunSecret) {
				t.Fatalf("dry-run preview leaked the password:\n%s", out)
			}
			if !strings.Contains(out, "***") {
				t.Fatalf("dry-run preview did not mask the password:\n%s", out)
			}
			if !strings.Contains(out, "config set password") {
				t.Fatalf("dry-run preview lost the command/key:\n%s", out)
			}
		})
	}
}

// TestDryRunConfigSetNonSecretKeepsValue verifies the mask is conditional:
// a non-secret key (url) still shows its value.
func TestDryRunConfigSetNonSecretKeepsValue(t *testing.T) {
	saveGlobals(t)
	flagFormat, flagJSON, flagPlaintext = "table", false, false

	out := captureStdout(t, func() {
		if err := renderDryRunPreview(configSetCmd, []string{"url", "https://glkvm.example.com"}); err != nil {
			t.Fatalf("renderDryRunPreview: %v", err)
		}
	})
	if !strings.Contains(out, "https://glkvm.example.com") {
		t.Fatalf("dry-run preview dropped the url value:\n%s", out)
	}
	if strings.Contains(out, "***") {
		t.Fatalf("dry-run preview masked a non-secret url:\n%s", out)
	}
}

// TestDryRunConfigSetURLUserInfoRedacted covers the D8 sibling: URL
// credentials embedded in a config value are masked.
func TestDryRunConfigSetURLUserInfoRedacted(t *testing.T) {
	saveGlobals(t)
	flagFormat, flagJSON, flagPlaintext = "table", false, false

	out := captureStdout(t, func() {
		if err := renderDryRunPreview(configSetCmd, []string{"url", "https://admin:s3cr3t@glkvm.example.com"}); err != nil {
			t.Fatalf("renderDryRunPreview: %v", err)
		}
	})
	if strings.Contains(out, "s3cr3t") || strings.Contains(out, "admin:") {
		t.Fatalf("dry-run preview leaked URL userinfo:\n%s", out)
	}
	if !strings.Contains(out, "https://***@glkvm.example.com") {
		t.Fatalf("dry-run preview did not mask URL userinfo:\n%s", out)
	}
}

// TestDryRunConfigSetWired exercises the annotation through the real
// --dry-run path, so MarkSecretKV is proven wired to 'config set'.
func TestDryRunConfigSetWired(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")
	saveGlobals(t)
	saveAnnotationGlobals(t)
	flagVersion, flagFormat, flagJSON, flagPlaintext, flagVerbose = false, "table", false, false, false
	flagDryRun = true

	cmd := findCommandByPath(t, "config set")
	out := captureStdout(t, func() {
		if err := GetRootCmd().PersistentPreRunE(cmd, []string{"password", testDryRunSecret}); err != errStopSuccess {
			t.Fatalf("PersistentPreRunE = %v, want errStopSuccess", err)
		}
	})
	if strings.Contains(out, testDryRunSecret) {
		t.Fatalf("--dry-run config set leaked the password:\n%s", out)
	}
	if !strings.Contains(out, "***") {
		t.Fatalf("--dry-run config set did not mask:\n%s", out)
	}
}

// TestConfigSetConfirmationMasksSecretValue verifies the actual (non-dry-run)
// 'config set' output: non-secret values are shown, secret values are masked, in
// both table and structured modes.
func TestConfigSetConfirmationMasksSecretValue(t *testing.T) {
	saveGlobals(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")

	// Table mode.
	flagFormat, flagJSON, flagPlaintext = "table", false, false
	out := captureStdout(t, func() {
		if err := runConfigSet(nil, []string{"url", "https://glkvm.example.com"}); err != nil {
			t.Fatalf("config set url: %v", err)
		}
	})
	if !strings.Contains(out, "https://glkvm.example.com") {
		t.Fatalf("config set url did not show the value:\n%s", out)
	}
	out = captureStdout(t, func() {
		if err := runConfigSet(nil, []string{"password", testDryRunSecret}); err != nil {
			t.Fatalf("config set password: %v", err)
		}
	})
	if strings.Contains(out, testDryRunSecret) {
		t.Fatalf("config set password leaked the value:\n%s", out)
	}
	if !strings.Contains(out, "***") {
		t.Fatalf("config set password did not mask the value:\n%s", out)
	}

	// JSON mode.
	flagFormat, flagJSON, flagPlaintext = "table", true, false
	out = captureStdout(t, func() {
		if err := runConfigSet(nil, []string{"username", "admin"}); err != nil {
			t.Fatalf("config set username: %v", err)
		}
	})
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("config set --json is not valid JSON: %v\n%s", err, out)
	}
	if got["value"] != "admin" {
		t.Errorf("config set username --json value = %v, want admin", got["value"])
	}
	if got["key"] != "username" {
		t.Errorf("config set username --json key = %v", got["key"])
	}

	out = captureStdout(t, func() {
		if err := runConfigSet(nil, []string{"password", testDryRunSecret}); err != nil {
			t.Fatalf("config set password --json: %v", err)
		}
	})
	got = map[string]any{}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("config set password --json is not valid JSON: %v\n%s", err, out)
	}
	if got["value"] != "***" {
		t.Errorf("config set password --json value = %v, want ***", got["value"])
	}
	if got["key"] != "password" {
		t.Errorf("config set password --json key = %v, want password", got["key"])
	}
}

// TestSystemHostnameDecodeErrorRedactsSecrets is the D9 end-to-end
// acceptance: a real command ('system hostname') against a mock whose
// /api/system/get_hostname result fails decoding while carrying ssl_key/
// password/token must produce an error that contains no secret. This exercises
// api.Hostname.UnmarshalJSON's own error text, which the client wraps with %v.
func TestSystemHostnameDecodeErrorRedactsSecrets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth/login":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"token":"test-token"}}`))
		case "/api/system/get_hostname":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"hostname":12345,"ssl_key":"SECRET_LEAK","password":"SECRET_LEAK","token":"SECRET_LEAK"}}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		}
	}))
	defer srv.Close()

	setRedactEnv(t, srv.URL)
	saveGlobals(t)

	err := runSystemHostname(systemHostnameCmd, nil)
	if err == nil {
		t.Fatal("expected a decode error for a numeric hostname")
	}
	if strings.Contains(err.Error(), "SECRET_LEAK") {
		t.Fatalf("system hostname leaked a secret:\n%v", err)
	}
	if !strings.Contains(err.Error(), "***") {
		t.Fatalf("system hostname error did not mask the payload:\n%v", err)
	}
}

// TestConfigSetURLUserInfoRedactedReal is the secondary-leak regression:
// the REAL (non-dry-run) 'config set url' confirmation and --json envelope must
// mask credentials embedded in the URL.
func TestConfigSetURLUserInfoRedactedReal(t *testing.T) {
	saveGlobals(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")

	const raw = "https://user:s3cr3t@glkvm.example.com"

	// Table mode.
	flagFormat, flagJSON, flagPlaintext = "table", false, false
	out := captureStdout(t, func() {
		if err := runConfigSet(nil, []string{"url", raw}); err != nil {
			t.Fatalf("config set url: %v", err)
		}
	})
	if strings.Contains(out, "s3cr3t") || strings.Contains(out, "user:") {
		t.Fatalf("config set url (table) leaked userinfo:\n%s", out)
	}
	if !strings.Contains(out, "https://***@glkvm.example.com") {
		t.Fatalf("config set url (table) did not mask userinfo:\n%s", out)
	}

	// JSON mode.
	flagFormat, flagJSON, flagPlaintext = "table", true, false
	out = captureStdout(t, func() {
		if err := runConfigSet(nil, []string{"url", raw}); err != nil {
			t.Fatalf("config set url --json: %v", err)
		}
	})
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("config set url --json is not valid JSON: %v\n%s", err, out)
	}
	if got["value"] != "https://***@glkvm.example.com" {
		t.Errorf("config set url --json value = %v, want masked userinfo", got["value"])
	}
	if got["key"] != "url" {
		t.Errorf("config set url --json key = %v, want url", got["key"])
	}
}
