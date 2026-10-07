package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMaskConfigValue(t *testing.T) {
	if got := maskConfigValue("password", "hunter2"); got != "***" {
		t.Errorf("password mask = %q, want ***", got)
	}
	if got := maskConfigValue("password", ""); got != "" {
		t.Errorf("empty password mask = %q, want empty", got)
	}
	if got := maskConfigValue("url", "https://example.com"); got != "https://example.com" {
		t.Errorf("url = %q, want unmasked", got)
	}
}

// TestConfigConfirmationKeyNotMasked is the follow-up regression: the
// 'config set'/'config unset' confirmation envelope names the config *key*
// ("url"), which the central read-path redactor must not mask — while the
// persisted password value stays masked in 'config list'.
func TestConfigConfirmationKeyNotMasked(t *testing.T) {
	saveGlobals(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")

	// The confirmation envelope must preserve the key name in JSON.
	flagJSON, flagPlaintext, flagFormat = true, false, "table"
	out := captureStdout(t, func() {
		if err := runConfigSet(nil, []string{"url", "https://example.com"}); err != nil {
			t.Fatalf("config set url: %v", err)
		}
	})
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("config set --json is not valid JSON: %v\n%s", err, out)
	}
	if got["key"] != "url" {
		t.Errorf("config set --json key = %v, want \"url\" (envelope must not be redacted)", got["key"])
	}
	if got["action"] != "set" {
		t.Errorf("config set --json action = %v, want \"set\"", got["action"])
	}

	// ...and in YAML (for 'config unset', on a key that is not needed later).
	flagJSON, flagPlaintext, flagFormat = false, false, "yaml"
	out = captureStdout(t, func() {
		if err := runConfigSet(nil, []string{"timeout", "45s"}); err != nil {
			t.Fatalf("config set timeout: %v", err)
		}
		if err := runConfigUnset(nil, []string{"timeout"}); err != nil {
			t.Fatalf("config unset timeout: %v", err)
		}
	})
	if !strings.Contains(out, "key: url") && !strings.Contains(out, "key: timeout") {
		t.Errorf("config unset --format yaml did not preserve the key name:\n%s", out)
	}
	if strings.Contains(out, "key: '***'") || strings.Contains(out, `key: "***"`) {
		t.Errorf("config unset --format yaml masked the key name:\n%s", out)
	}

	// 'config list --json' must still mask the password value while showing the
	// (non-secret) url key added above.
	flagJSON, flagPlaintext, flagFormat = false, false, "table"
	_ = captureStdout(t, func() {
		if err := runConfigSet(nil, []string{"password", "hunter2"}); err != nil {
			t.Fatalf("config set password: %v", err)
		}
	})
	flagJSON, flagPlaintext, flagFormat = true, false, "table"
	out = captureStdout(t, func() {
		if err := runConfigList(nil, nil); err != nil {
			t.Fatalf("config list: %v", err)
		}
	})
	var listed map[string]string
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("config list --json is not valid JSON: %v\n%s", err, out)
	}
	if listed["password"] != "***" {
		t.Errorf("config list --json password = %q, want ***", listed["password"])
	}
	if listed["url"] != "https://example.com" {
		t.Errorf("config list --json url = %q, want unmasked", listed["url"])
	}
}

func TestConfigListAndGetHonorOutputFlags(t *testing.T) {
	saveGlobals(t)

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")

	if err := runConfigSet(nil, []string{"url", "https://example.com"}); err != nil {
		t.Fatalf("set url: %v", err)
	}
	if err := runConfigSet(nil, []string{"password", "hunter2"}); err != nil {
		t.Fatalf("set password: %v", err)
	}

	// Default table mode: header + masked password.
	flagJSON, flagPlaintext, flagFormat = false, false, "table"
	out := captureStdout(t, func() {
		if err := runConfigList(nil, nil); err != nil {
			t.Fatalf("config list: %v", err)
		}
	})
	if !strings.Contains(out, "KEY") || !strings.Contains(out, "VALUE") {
		t.Errorf("table output missing headers: %q", out)
	}
	if !strings.Contains(out, "password") || !strings.Contains(out, "***") {
		t.Errorf("table output not masking password: %q", out)
	}
	if strings.Contains(out, "hunter2") {
		t.Errorf("table output leaked the password: %q", out)
	}

	// JSON mode: valid object, password masked.
	flagJSON, flagPlaintext, flagFormat = true, false, "table"
	out = captureStdout(t, func() {
		if err := runConfigList(nil, nil); err != nil {
			t.Fatalf("config list --json: %v", err)
		}
	})
	var obj map[string]string
	if err := json.Unmarshal([]byte(out), &obj); err != nil {
		t.Fatalf("config list --json is not valid JSON: %v\n%s", err, out)
	}
	if obj["url"] != "https://example.com" {
		t.Errorf("json url = %q, want https://example.com", obj["url"])
	}
	if obj["password"] != "***" {
		t.Errorf("json password = %q, want ***", obj["password"])
	}

	// Plaintext mode: tab-separated.
	flagJSON, flagPlaintext, flagFormat = false, true, "table"
	out = captureStdout(t, func() {
		if err := runConfigList(nil, nil); err != nil {
			t.Fatalf("config list --plaintext: %v", err)
		}
	})
	if !strings.Contains(out, "url\thttps://example.com") {
		t.Errorf("plaintext output missing url row: %q", out)
	}

	// config get --json must produce {"url":"https://example.com"}.
	flagJSON, flagPlaintext, flagFormat = true, false, "table"
	out = captureStdout(t, func() {
		if err := runConfigGet(nil, []string{"url"}); err != nil {
			t.Fatalf("config get --json: %v", err)
		}
	})
	var got map[string]string
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("config get --json is not valid JSON: %v\n%s", err, out)
	}
	if got["url"] != "https://example.com" {
		t.Errorf("get json url = %q, want https://example.com", got["url"])
	}

	// config get --plaintext emits "<key>\t<value>".
	flagJSON, flagPlaintext, flagFormat = false, true, "table"
	out = captureStdout(t, func() {
		if err := runConfigGet(nil, []string{"url"}); err != nil {
			t.Fatalf("config get --plaintext: %v", err)
		}
	})
	if out != "url\thttps://example.com\n" {
		t.Errorf("get plaintext = %q, want %q", out, "url\thttps://example.com\n")
	}
}
