package cmd

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/api"
	"github.com/roboalchemist/kvm-cli/pkg/output"
)

// newAbortMock serves a successful login, then abruptly aborts the connection
// for abortPath so the caller observes a raw net/http transport error (the
// exact shape that used to leak the query-string secret). Every other path
// returns an empty success envelope.
func newAbortMock(t *testing.T, abortPath string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{"token":"tok"}}`))
			return
		case abortPath:
			// ErrAbortHandler closes the connection without an HTTP response,
			// producing a *url.Error on the client.
			panic(http.ErrAbortHandler)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func snapshotATXGlobals(t *testing.T) {
	t.Helper()
	dry, insecure, yes := flagDryRun, flagInsecure, atxYes
	t.Cleanup(func() { flagDryRun, flagInsecure, atxYes = dry, insecure, yes })
}

// TestRawPostRedactsTransportError is the D1 unit test: a raw vmRawPost
// transport failure against a closed port must never surface the secret query
// parameters in its error string, with or without --debug.
func TestRawPostRedactsTransportError(t *testing.T) {
	snapshotATXGlobals(t)
	flagInsecure = false
	t.Setenv("KVM_TLS_STRICT", "")
	t.Setenv("GLKVM_TLS_STRICT", "")

	client := api.NewClient("http://127.0.0.1:1", "admin", "pw", api.Options{})
	query := url.Values{
		"user":         {"admin"},
		"old_password": {"OLDSECRET"},
		"new_password": {"NEWSECRET"},
	}.Encode()

	var out map[string]any
	err := vmRawPost(client, "/api/init/change_password?"+query, "", nil, 0, &out)
	if err == nil {
		t.Fatal("expected a transport error against a closed port")
	}
	msg := err.Error()
	for _, secret := range []string{"OLDSECRET", "NEWSECRET"} {
		if strings.Contains(msg, secret) {
			t.Fatalf("raw transport error leaked %q: %s", secret, msg)
		}
	}
	for _, want := range []string{"old_password=***", "new_password=***"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("raw transport error did not mask %q: %s", want, msg)
		}
	}
	// errors.As must still reach the underlying *url.Error through the wrapper.
	var uerr *url.Error
	if !errors.As(err, &uerr) {
		t.Fatalf("redaction broke errors.As to *url.Error: %v", err)
	}
}

// TestInitChangePasswordNoLeak is the D1 acceptance for
// 'init change-password': when the raw request fails in transport, neither the
// old nor the new password may appear in the returned error.
func TestInitChangePasswordNoLeak(t *testing.T) {
	srv := newAbortMock(t, "/api/init/change_password")
	setRedactEnv(t, srv.URL)
	saveGlobals(t)
	flagFormat, flagJSON, flagPlaintext = "json", false, false

	k10InitChangeYes = true
	k10InitUser = "admin"
	k10InitOldPassword = "OLDSECRET"
	k10InitNewPassword = "NEWSECRET"
	t.Cleanup(func() {
		k10InitChangeYes = false
		k10InitOldPassword = ""
		k10InitNewPassword = ""
	})

	var runErr error
	out, errOut := captureOutput(t, func() {
		runErr = runInitChangePassword(initChangePasswordCmd, nil)
	})
	if runErr == nil {
		t.Fatal("expected a transport error from change-password")
	}
	combined := runErr.Error() + out + errOut
	for _, secret := range []string{"OLDSECRET", "NEWSECRET"} {
		if strings.Contains(combined, secret) {
			t.Fatalf("init change-password leaked %q (err=%v stdout=%q stderr=%q)", secret, runErr, out, errOut)
		}
	}
	if !strings.Contains(runErr.Error(), "new_password=***") {
		t.Fatalf("init change-password did not mask the password in the error: %v", runErr)
	}
}

// TestTwofaInitNoLeak is the D1 acceptance for '2fa init': the TOTP secret
// must not leak when the raw request fails in transport.
func TestTwofaInitNoLeak(t *testing.T) {
	srv := newAbortMock(t, "/api/2fa/init")
	setRedactEnv(t, srv.URL)
	saveGlobals(t)
	flagFormat, flagJSON, flagPlaintext = "json", false, false

	k10TwofaInitYes = true
	k10TwofaInitSecret = "JBSWY3DPEHPK3PXP"
	k10TwofaInitCode = "123456"
	t.Cleanup(func() {
		k10TwofaInitYes = false
		k10TwofaInitSecret = ""
		k10TwofaInitCode = ""
	})

	var runErr error
	out, errOut := captureOutput(t, func() {
		runErr = runTwofaInit(twofaInitCmd, nil)
	})
	if runErr == nil {
		t.Fatal("expected a transport error from 2fa init")
	}
	combined := runErr.Error() + out + errOut
	if strings.Contains(combined, "JBSWY3DPEHPK3PXP") {
		t.Fatalf("2fa init leaked the TOTP secret (err=%v stdout=%q stderr=%q)", runErr, out, errOut)
	}
	if !strings.Contains(runErr.Error(), "secret=***") {
		t.Fatalf("2fa init did not mask the secret in the error: %v", runErr)
	}
}

// TestActionLabelsMaskSecretParams is the D2 acceptance for the action /
// summary labels rendered by tailscale config, system set-param and msd
// set-params.
func TestActionLabelsMaskSecretParams(t *testing.T) {
	srv, _ := newRedactMock(t)
	setRedactEnv(t, srv.URL)
	saveGlobals(t)
	flagFormat, flagJSON, flagPlaintext = "json", false, false
	dry := flagDryRun
	flagDryRun = false
	t.Cleanup(func() { flagDryRun = dry })

	const secret = "SUPERSECRET"

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "tailscale config",
			run: func() error {
				tsConfigSet = []string{"password=" + secret}
				defer func() { tsConfigSet = nil }()
				return runTailscaleConfig(tailscaleConfigCmd, nil)
			},
		},
		{
			name: "system set-param",
			run: func() error {
				syswYes = true
				syswSetParam = []string{"key=" + secret}
				defer func() { syswYes = false; syswSetParam = nil }()
				return runSystemSetParam(systemSetParamCmd, nil)
			},
		},
		{
			name: "msd set-params",
			run: func() error {
				return runMSDSetParams(msdSetParamsCmd, []string{"key=" + secret})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var runErr error
			out, errOut := captureOutput(t, func() { runErr = tc.run() })
			if runErr != nil {
				t.Fatalf("%s: %v", tc.name, runErr)
			}
			combined := out + errOut
			if strings.Contains(combined, secret) {
				t.Fatalf("%s leaked the secret (stdout=%q stderr=%q)", tc.name, out, errOut)
			}
			if !strings.Contains(combined, "=***") {
				t.Fatalf("%s did not render a masked parameter (stdout=%q)", tc.name, out)
			}
		})
	}
}

// TestJSONFlagRequested locks in the argv scan used to honour --json when it
// could not be parsed because it followed an unknown token.
func TestJSONFlagRequested(t *testing.T) {
	cases := []struct {
		argv []string
		want bool
	}{
		{nil, false},
		{[]string{"--json", "bogus"}, true},
		{[]string{"-j", "bogus"}, true},
		{[]string{"--bogus", "--json"}, true},
		{[]string{"--bogus", "-j"}, true},
		{[]string{"--json=false", "bogus"}, false},
		{[]string{"--bogus"}, false},
		{[]string{"--help", "--json"}, false},
		{[]string{"bogus", "--", "--json"}, false},
	}
	for _, tc := range cases {
		if got := jsonFlagRequested(tc.argv); got != tc.want {
			t.Errorf("jsonFlagRequested(%v) = %v, want %v", tc.argv, got, tc.want)
		}
	}
}

// TestRenderExecuteErrorHonorsJSON asserts the error renderer falls back to
// scanning argv so '--bogus --json' and '--json bogus' both emit the structured
// JSON envelope, while an invocation without --json stays plain text.
func TestRenderExecuteErrorHonorsJSON(t *testing.T) {
	saveGlobals(t)
	flagFormat, flagJSON, flagPlaintext = "table", false, false

	err := output.WrapCodedError("USAGE", errors.New("unknown flag: --bogus"), "unknown flag: --bogus")

	jsonOut := captureStderr(t, func() { renderExecuteError(err, []string{"--bogus", "--json"}) })
	if !strings.Contains(jsonOut, `"code": "USAGE"`) {
		t.Fatalf("--json after bad token did not render a JSON envelope:\n%s", jsonOut)
	}
	if !strings.Contains(jsonOut, `"error"`) {
		t.Fatalf("JSON error envelope missing the error object:\n%s", jsonOut)
	}

	plainOut := captureStderr(t, func() { renderExecuteError(err, []string{"--bogus"}) })
	if strings.Contains(plainOut, `"error"`) {
		t.Fatalf("plain error should not be JSON:\n%s", plainOut)
	}
	if !strings.Contains(plainOut, "Error: unknown flag: --bogus") {
		t.Fatalf("plain error missing expected text:\n%s", plainOut)
	}
}

// TestGroupUnknownSubcommandError is the D5 regression: an unknown
// subcommand of a grouping command (for example 'ap bogus') must be a
// structured USAGE error, while the bare group still prints help and succeeds.
func TestGroupUnknownSubcommandError(t *testing.T) {
	applyGroupCommandGuards()

	for _, path := range []string{"ap", "config", "tailscale", "msd"} {
		cmd := findCommandByPath(t, path)
		if cmd.RunE == nil {
			t.Fatalf("%s: group guard RunE was not installed", path)
		}

		err := cmd.RunE(cmd, []string{"bogus"})
		if output.ErrorCode(err) != "USAGE" {
			t.Fatalf("%s bogus: code = %q, want USAGE (err: %v)", path, output.ErrorCode(err), err)
		}
		if err == nil || !strings.Contains(err.Error(), "unknown command") {
			t.Fatalf("%s bogus: unexpected error %v", path, err)
		}

		out := captureStdout(t, func() {
			if e := cmd.RunE(cmd, nil); e != nil {
				t.Fatalf("%s (no args): %v", path, e)
			}
		})
		if !strings.Contains(out, "Usage:") {
			t.Fatalf("%s (no args) did not print help:\n%s", path, out)
		}
	}
}

// TestATXRequiresConfirmation is the D4 regression: ATX action commands
// refuse without --yes/-f/--force, preview under --dry-run, and proceed once
// confirmed.
func TestATXRequiresConfirmation(t *testing.T) {
	snapshotATXGlobals(t)
	saveGlobals(t)
	flagDryRun, flagInsecure = false, false

	// Refused before any device contact.
	if err := runATXClick(atxPowerCmd, "power", false); output.ErrorCode(err) != "CONFIRMATION_REQUIRED" {
		t.Fatalf("atx power without confirmation: code = %q, want CONFIRMATION_REQUIRED (err: %v)", output.ErrorCode(err), err)
	}

	// --dry-run produces a dry-run preview without contacting the device.
	flagDryRun = true
	if err := runATXClick(atxPowerCmd, "power", false); output.ErrorCode(err) != "CONFIRMATION_REQUIRED" || !strings.Contains(err.Error(), "dry run") {
		t.Fatalf("atx power --dry-run: got %v, want a dry-run preview", err)
	}
	flagDryRun = false

	// Invalid button is rejected before confirmation.
	if err := runATXClick(atxClickCmd, "nope", true); output.ErrorCode(err) != "USAGE" {
		t.Fatalf("atx click invalid button: code = %q, want USAGE", output.ErrorCode(err))
	}

	// Confirmed click proceeds past the gate and reaches the (unreachable)
	// device, so the failure is a transport/auth error, not a refusal.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")
	t.Setenv("KVM_URL", "http://127.0.0.1:1")
	t.Setenv("KVM_USERNAME", "admin")
	t.Setenv("KVM_PASSWORD", "pw")

	err := runATXClick(atxPowerCmd, "power", true)
	if err == nil {
		t.Fatal("confirmed atx power against a closed port should fail")
	}
	if output.ErrorCode(err) == "CONFIRMATION_REQUIRED" {
		t.Fatalf("confirmed atx power was refused: %v", err)
	}
}

// TestDryRunMasksSecretPositionals is the D6 regression: positional secrets
// (zerotier set-token, modem input-pin) and secret key=value positionals are
// masked in the --dry-run preview.
func TestDryRunMasksSecretPositionals(t *testing.T) {
	saveGlobals(t)
	dry := flagDryRun
	flagFormat, flagJSON, flagPlaintext, flagDryRun = "table", false, false, false
	t.Cleanup(func() { flagDryRun = dry })

	check := func(name string, run func() error, secret string) {
		t.Helper()
		out := captureStdout(t, func() {
			if err := run(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		})
		if strings.Contains(out, secret) {
			t.Fatalf("%s preview leaked %q:\n%s", name, secret, out)
		}
		if !strings.Contains(out, "***") {
			t.Fatalf("%s preview did not mask the secret:\n%s", name, out)
		}
	}

	check("zerotier set-token", func() error {
		return renderDryRunPreview(zerotierSetTokenCmd, []string{"SUPERSECRET"})
	}, "SUPERSECRET")
	check("modem input-pin", func() error {
		return renderDryRunPreview(modemInputPINCmd, []string{"4321"})
	}, "4321")
	check("msd set-params", func() error {
		return renderDryRunPreview(msdSetParamsCmd, []string{"key=SUPERSECRET"})
	}, "SUPERSECRET")
}
