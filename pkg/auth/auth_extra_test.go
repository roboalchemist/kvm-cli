package auth

import (
	"bufio"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// errReader always fails, exercising non-EOF read errors.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }

func TestGopassValueEmptyKey(t *testing.T) {
	// An empty key short-circuits without invoking gopass.
	if got := gopassValue(""); got != "" {
		t.Fatalf("gopassValue(\"\") = %q, want empty", got)
	}
}

func TestGopassValueMissingStore(t *testing.T) {
	noGopass(t)
	if got := gopassValue("GLKVM_URL"); got != "" {
		t.Fatalf("gopassValue without gopass = %q, want empty", got)
	}
}

func TestRawStringCoercions(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`"plain"`, "plain"},
		{`true`, "true"},
		{`false`, "false"},
		{`123`, "123"},
		{`1.5`, "1.5"},
		{`{}`, ""}, // object has no string coercion
		{`{`, ""},  // malformed JSON
	}
	for _, tc := range cases {
		if got := rawString(json.RawMessage(tc.in)); got != tc.want {
			t.Errorf("rawString(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPromptLineReadError(t *testing.T) {
	_, err := promptLine(bufio.NewReader(errReader{}), "URL")
	if err == nil {
		t.Fatal("expected read error to propagate")
	}
	if !strings.Contains(err.Error(), "url") {
		t.Fatalf("error %q should mention url", err)
	}
}

func TestLoadConfigWhitespaceOnly(t *testing.T) {
	isolate(t)
	writeRawConfig(t, "   \n\t ")
	got, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if *got != (Config{}) {
		t.Fatalf("whitespace-only config should be empty, got %+v", *got)
	}
}

func TestListConfigAllKeysSet(t *testing.T) {
	isolate(t)
	if err := SaveConfig(&Config{
		URL: "https://x", Username: "u", Password: "p", Timeout: "15s", Insecure: true,
	}); err != nil {
		t.Fatal(err)
	}
	m, err := ListConfig()
	if err != nil {
		t.Fatalf("ListConfig: %v", err)
	}
	for _, k := range []string{"url", "username", "password", "timeout", "insecure"} {
		if _, ok := m[k]; !ok {
			t.Errorf("ListConfig missing %q: %v", k, m)
		}
	}
	if m["insecure"] != "true" {
		t.Errorf("insecure = %q, want true", m["insecure"])
	}
}
