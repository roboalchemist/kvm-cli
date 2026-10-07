package cmd

import (
	"errors"
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/auth"
	"github.com/roboalchemist/kvm-cli/pkg/output"
)

// saveGlobals snapshots and restores the package-level flag values that the
// tests mutate, so tests do not leak state into one another.
func saveGlobals(t *testing.T) {
	t.Helper()
	url, user, pass := flagURL, flagUsername, flagPassword
	format, fields, jq := flagFormat, flagFields, flagJQ
	jsonOut, plain, noColor, debug := flagJSON, flagPlaintext, flagNoColor, flagDebug
	t.Cleanup(func() {
		flagURL, flagUsername, flagPassword = url, user, pass
		flagFormat, flagFields, flagJQ = format, fields, jq
		flagJSON, flagPlaintext, flagNoColor, flagDebug = jsonOut, plain, noColor, debug
	})
}

func TestGetOutputOptions(t *testing.T) {
	saveGlobals(t)

	tests := []struct {
		name       string
		json       bool
		plaintext  bool
		format     string
		fields     string
		jq         string
		noColor    bool
		debug      bool
		wantMode   output.Mode
		wantFields []string
	}{
		{name: "default table", format: "table", wantMode: output.ModeTable},
		{name: "format json", format: "json", wantMode: output.ModeJSON},
		{name: "flag json wins", format: "table", json: true, wantMode: output.ModeJSON},
		{name: "flag plaintext wins", format: "json", plaintext: true, wantMode: output.ModePlaintext},
		{name: "yaml", format: "yaml", wantMode: output.ModeYAML},
		{name: "fields", format: "json", fields: "id, name ,", wantMode: output.ModeJSON, wantFields: []string{"id", "name"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			flagJSON, flagPlaintext = tc.json, tc.plaintext
			flagFormat, flagFields, flagJQ = tc.format, tc.fields, tc.jq
			flagNoColor, flagDebug = tc.noColor, tc.debug

			opts := GetOutputOptions()
			if opts.Mode != tc.wantMode {
				t.Fatalf("mode = %v, want %v", opts.Mode, tc.wantMode)
			}
			if tc.jq != "" && opts.JQ != tc.jq {
				t.Fatalf("jq = %q, want %q", opts.JQ, tc.jq)
			}
			if len(opts.Fields) != len(tc.wantFields) {
				t.Fatalf("fields = %v, want %v", opts.Fields, tc.wantFields)
			}
			for i := range tc.wantFields {
				if opts.Fields[i] != tc.wantFields[i] {
					t.Fatalf("fields = %v, want %v", opts.Fields, tc.wantFields)
				}
			}
			if opts.NoColor != tc.noColor || opts.Debug != tc.debug {
				t.Fatalf("NoColor/Debug = %v/%v, want %v/%v", opts.NoColor, opts.Debug, tc.noColor, tc.debug)
			}
		})
	}
}

func TestSupportedConfigKeys(t *testing.T) {
	keys := supportedConfigKeys()
	for _, want := range []string{"url", "username", "password", "timeout", "insecure", "output_format"} {
		found := false
		for _, k := range keys {
			if k == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("supported keys %v missing %q", keys, want)
		}
	}
	if !isOutputFormatKey("format") || !isOutputFormatKey("output_format") {
		t.Error("isOutputFormatKey should accept both format and output_format")
	}
}

func TestExitCode(t *testing.T) {
	if got := exitCode(output.NewCodedError("USAGE", "bad")); got != 2 {
		t.Errorf("usage exit code = %d, want 2", got)
	}
	if got := exitCode(errors.New("boom")); got != 1 {
		t.Errorf("generic exit code = %d, want 1", got)
	}
}

// TestConfigRoundTrip exercises the config command helpers against an isolated
// HOME, verifying that writes through pkg/auth preserve the CLI-only
// output_format preference.
func TestConfigRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")

	if err := runConfigSet(nil, []string{"url", "https://example.com"}); err != nil {
		t.Fatalf("set url: %v", err)
	}
	if err := runConfigSet(nil, []string{"output_format", "json"}); err != nil {
		t.Fatalf("set output_format: %v", err)
	}
	// A subsequent write through pkg/auth must not drop output_format.
	if err := runConfigSet(nil, []string{"username", "admin"}); err != nil {
		t.Fatalf("set username: %v", err)
	}

	cfg, err := auth.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.URL != "https://example.com" || cfg.Username != "admin" {
		t.Fatalf("auth config not persisted: %+v", cfg)
	}
	if got := loadOutputFormat(); got != "json" {
		t.Fatalf("output_format = %q, want json", got)
	}

	if err := runConfigUnset(nil, []string{"output_format"}); err != nil {
		t.Fatalf("unset output_format: %v", err)
	}
	if got := loadOutputFormat(); got != "" {
		t.Fatalf("output_format after unset = %q, want empty", got)
	}

	if err := runConfigUnset(nil, []string{"url"}); err != nil {
		t.Fatalf("unset url: %v", err)
	}
	cfg, err = auth.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.URL != "" {
		t.Fatalf("url after unset = %q, want empty", cfg.URL)
	}
}

func TestConfigValidation(t *testing.T) {
	if err := runConfigSet(nil, []string{"bogus", "x"}); output.ErrorCode(err) != "USAGE" {
		t.Errorf("unknown key error code = %q, want USAGE", output.ErrorCode(err))
	}
	if err := runConfigSet(nil, []string{"timeout", "nope"}); output.ErrorCode(err) != "USAGE" {
		t.Errorf("bad timeout error code = %q, want USAGE", output.ErrorCode(err))
	}
	if err := runConfigSet(nil, []string{"output_format", "nope"}); output.ErrorCode(err) != "USAGE" {
		t.Errorf("bad format error code = %q, want USAGE", output.ErrorCode(err))
	}
}
