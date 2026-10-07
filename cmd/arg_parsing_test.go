package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// captureStderr redirects os.Stderr while fn runs and returns what was written.
func captureStderr(t *testing.T, fn func()) string {
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

// tlsLoginServer serves /api/auth/login over TLS with a self-signed certificate
// so the client exercises the insecure-TLS fallback (and its stderr warning).
func tlsLoginServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/auth/login" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"token":"tok"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestAuthStatusQuietSuppressesTLSWarning is the regression: 'auth
// status' must route through the quiet-aware client path so --quiet/--silent
// suppresses the TLS-fallback warning on stderr.
func TestAuthStatusQuietSuppressesTLSWarning(t *testing.T) {
	saveGlobals(t)
	// saveGlobals does not cover the quiet/verbose globals; snapshot them here.
	quiet, silent, verbose := flagQuiet, flagSilent, flagVerbose
	t.Cleanup(func() { flagQuiet, flagSilent, flagVerbose = quiet, silent, verbose })

	t.Setenv("HOME", t.TempDir())
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")
	t.Setenv("PATH", t.TempDir()) // no gopass lookups

	srv := tlsLoginServer(t)
	t.Setenv("KVM_URL", srv.URL)
	t.Setenv("KVM_USERNAME", "admin")
	t.Setenv("KVM_PASSWORD", "pw")

	// Loud: the TLS fallback warning must reach stderr.
	flagQuiet, flagSilent, flagVerbose = false, false, false
	loud := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			if err := runAuthStatus(authStatusCmd, nil); err != nil {
				t.Fatalf("runAuthStatus (loud): %v", err)
			}
		})
	})
	if !strings.Contains(loud, "TLS certificate verification failed") {
		t.Fatalf("expected TLS warning without --quiet, got stderr: %q", loud)
	}

	// Quiet: the same call must go through the quiet-aware path and suppress it.
	flagQuiet = true
	quietOut := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			if err := runAuthStatus(authStatusCmd, nil); err != nil {
				t.Fatalf("runAuthStatus (quiet): %v", err)
			}
		})
	})
	if strings.Contains(quietOut, "TLS certificate verification failed") {
		t.Fatalf("--quiet did not suppress the TLS warning: %q", quietOut)
	}

	// --verbose wins over --quiet, keeping the warning observable.
	flagQuiet, flagVerbose = true, true
	verboseOut := captureStderr(t, func() {
		_ = captureStdout(t, func() {
			if err := runAuthStatus(authStatusCmd, nil); err != nil {
				t.Fatalf("runAuthStatus (verbose): %v", err)
			}
		})
	})
	if !strings.Contains(verboseOut, "TLS certificate verification failed") {
		t.Fatalf("--verbose should keep the warning visible, got stderr: %q", verboseOut)
	}
}

func TestIsNegativeNumber(t *testing.T) {
	neg := []string{"-5", "-0.5", "-100"}
	for _, s := range neg {
		if !isNegativeNumber(s) {
			t.Errorf("isNegativeNumber(%q) = false, want true", s)
		}
	}
	nonNeg := []string{"5", "0", "--json", "-j", "-", "-x", "-5px", "", "3.5"}
	for _, s := range nonNeg {
		if isNegativeNumber(s) {
			t.Errorf("isNegativeNumber(%q) = true, want false", s)
		}
	}
}

// TestTrailingFlagArgs verifies that global flags may follow the
// coordinates of a non-interspersed command while negative deltas remain
// positional.
func TestTrailingFlagArgs(t *testing.T) {
	newCmd := func() (*cobra.Command, *bool, *int) {
		var jsonOut bool
		var pct int
		c := &cobra.Command{Use: "move"}
		c.Flags().BoolVar(&jsonOut, "json", false, "")
		c.Flags().IntVar(&pct, "pct", 0, "")
		return c, &jsonOut, &pct
	}

	// Trailing boolean flag is parsed; coordinates kept.
	c, jsonOut, _ := newCmd()
	pos, err := trailingFlagArgs(c, []string{"1000", "500", "--json"})
	if err != nil {
		t.Fatalf("trailingFlagArgs: %v", err)
	}
	if len(pos) != 2 || pos[0] != "1000" || pos[1] != "500" {
		t.Fatalf("positional = %v, want [1000 500]", pos)
	}
	if !*jsonOut {
		t.Fatal("--json after coordinates was not parsed")
	}

	// Trailing flag with a value is parsed.
	c, _, pct := newCmd()
	pos, err = trailingFlagArgs(c, []string{"5", "-5", "--pct", "50"})
	if err != nil {
		t.Fatalf("trailingFlagArgs: %v", err)
	}
	if len(pos) != 2 || pos[0] != "5" || pos[1] != "-5" {
		t.Fatalf("positional = %v, want [5 -5]", pos)
	}
	if *pct != 50 {
		t.Fatalf("--pct = %d, want 50", *pct)
	}

	// No trailing flags: everything is positional, including negatives.
	c, jsonOut, _ = newCmd()
	pos, err = trailingFlagArgs(c, []string{"-3", "-7"})
	if err != nil {
		t.Fatalf("trailingFlagArgs: %v", err)
	}
	if len(pos) != 2 || pos[0] != "-3" || pos[1] != "-7" {
		t.Fatalf("positional = %v, want [-3 -7]", pos)
	}
	if *jsonOut {
		t.Fatal("--json should remain unset")
	}

	// An unknown trailing flag is a usage error.
	c, _, _ = newCmd()
	if _, err := trailingFlagArgs(c, []string{"1", "2", "--nope"}); err == nil {
		t.Fatal("expected usage error for unknown trailing flag")
	} else if got := output.ErrorCode(err); got != "USAGE" {
		t.Fatalf("unknown trailing flag code = %q, want USAGE", got)
	}
}
