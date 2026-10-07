package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// TestK10MaskSecrets verifies secret-looking keys are redacted recursively
// while benign keys are preserved. It also guards the acceptance requirement
// that 2FA output never leaks the shared secret or its otpauth URI.
func TestK10MaskSecrets(t *testing.T) {
	in := map[string]any{
		"enabled": true,
		"secret":  "JBSWY3DPEHPK3PXP",
		"uri":     "otpauth://totp/glkvm?secret=JBSWY3DPEHPK3PXP",
		"nested": map[string]any{
			"password": "hunter2",
			"token":    "abc123",
			"label":    "keep-me",
		},
		"list": []any{
			map[string]any{"otpauth": "x"},
			"plain",
		},
	}

	masked := k10MaskMap(in)

	if masked["enabled"] != true {
		t.Errorf("enabled = %v, want true", masked["enabled"])
	}
	if masked["secret"] != "***" {
		t.Errorf("secret = %v, want ***", masked["secret"])
	}
	if masked["uri"] != "***" {
		t.Errorf("uri = %v, want ***", masked["uri"])
	}
	nested, _ := masked["nested"].(map[string]any)
	if nested["password"] != "***" || nested["token"] != "***" {
		t.Errorf("nested secrets not masked: %v", nested)
	}
	if nested["label"] != "keep-me" {
		t.Errorf("nested label = %v, want keep-me", nested["label"])
	}
	list, _ := masked["list"].([]any)
	if len(list) != 2 {
		t.Fatalf("list = %v, want 2 elements", list)
	}
	if item, _ := list[0].(map[string]any); item["otpauth"] != "***" {
		t.Errorf("list[0].otpauth = %v, want ***", item["otpauth"])
	}
	if list[1] != "plain" {
		t.Errorf("list[1] = %v, want plain", list[1])
	}
}

func TestK10IsSecretKey(t *testing.T) {
	secret := []string{"secret", "Secret", "password", "passwd", "token", "uri", "key", "otpauth", "private_key", "totp_seed", "qr_code"}
	for _, k := range secret {
		if !k10IsSecretKey(k) {
			t.Errorf("k10IsSecretKey(%q) = false, want true", k)
		}
	}
	benign := []string{"enabled", "is_running", "battery", "size", "total_size", "username", "model", "version"}
	for _, k := range benign {
		if k10IsSecretKey(k) {
			t.Errorf("k10IsSecretKey(%q) = true, want false", k)
		}
	}
}

func TestK10ValidatePressTime(t *testing.T) {
	valid := []int{500, 1000, 3000, 60000}
	for _, ms := range valid {
		if err := k10ValidatePressTime(ms); err != nil {
			t.Errorf("k10ValidatePressTime(%d) = %v, want nil", ms, err)
		}
	}
	invalid := []int{0, 999, 60001, -1}
	for _, ms := range invalid {
		if err := k10ValidatePressTime(ms); output.ErrorCode(err) != "USAGE" {
			t.Errorf("k10ValidatePressTime(%d) code = %q, want USAGE", ms, output.ErrorCode(err))
		}
	}
}

func TestK10StrengthEnum(t *testing.T) {
	cases := map[string]int{"low": 1, "LOW": 1, "high": 2, "High": 2, "1": 1, "2": 2}
	for name, want := range cases {
		got, err := k10StrengthEnum(name)
		if err != nil {
			t.Errorf("k10StrengthEnum(%q) error = %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("k10StrengthEnum(%q) = %d, want %d", name, got, want)
		}
	}
	if _, err := k10StrengthEnum("medium"); output.ErrorCode(err) != "USAGE" {
		t.Errorf("k10StrengthEnum(medium) code = %q, want USAGE", output.ErrorCode(err))
	}
}

// TestK10GuardsWithoutYes asserts every destructive command refuses without
// --yes with a CONFIRMATION_REQUIRED code. Because each run function checks the
// guard before resolving credentials, these calls never touch the network.
func TestK10GuardsWithoutYes(t *testing.T) {
	cases := []struct {
		name string
		run  func(*testing.T) error
	}{
		{"upgrade reboot", func(t *testing.T) error { return runUpgradeReboot(nil, nil) }},
		{"upgrade reset", func(t *testing.T) error { return runUpgradeReset(nil, nil) }},
		{"upgrade start", func(t *testing.T) error { return runUpgradeStart(nil, nil) }},
		{"upgrade upload", func(t *testing.T) error { return runUpgradeUpload(nil, []string{"/tmp/k10-nonexistent.img"}) }},
		{"2fa create", func(t *testing.T) error { return runTwofaCreate(nil, nil) }},
		{"2fa delete", func(t *testing.T) error { return runTwofaDelete(nil, nil) }},
		{"2fa init", func(t *testing.T) error { return runTwofaInit(nil, nil) }},
		{"fingerbot upgrade", func(t *testing.T) error { return runFingerbotUpgrade(nil, nil) }},
		{"init run", func(t *testing.T) error { return runInitRun(nil, nil) }},
		{"init change-password", func(t *testing.T) error { return runInitChangePassword(nil, nil) }},
	}

	// Ensure every guard flag starts false so the runs exercise the dry-run path.
	saved := []*bool{
		&k10UpgradeRebootYes, &k10UpgradeResetYes, &k10UpgradeStartYes, &k10UpgradeUploadYes,
		&k10TwofaCreateYes, &k10TwofaDeleteYes, &k10TwofaInitYes,
		&k10FingerbotUpgradeYes, &k10InitRunYes, &k10InitChangeYes,
	}
	for _, p := range saved {
		orig := *p
		*p = false
		t.Cleanup(func() { *p = orig })
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(t)
			if err == nil {
				t.Fatal("expected error without --yes, got nil")
			}
			if code := output.ErrorCode(err); code != "CONFIRMATION_REQUIRED" {
				t.Fatalf("error code = %q, want CONFIRMATION_REQUIRED (err: %v)", code, err)
			}
		})
	}
}

// TestK10TwofaShowMasksSecret exercises the full 'twofa show' path against a
// mock device that returns secret material and asserts the secret never
// reaches stdout (acceptance: "2fa show never prints secret material").
func TestK10TwofaShowMasksSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/auth/login":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"token":"tok"}}`))
		case "/api/2fa/show":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"enabled":true,"secret":"JBSWY3DPEHPK3PXP","uri":"otpauth://totp/glkvm?secret=JBSWY3DPEHPK3PXP"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("HOME", t.TempDir())
	t.Setenv("KVM_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("GLKVM_CONFIG", "")
	t.Setenv("KVM_URL", srv.URL)
	t.Setenv("KVM_USERNAME", "admin")
	t.Setenv("KVM_PASSWORD", "pw")

	saveGlobals(t)
	flagFormat, flagJSON, flagPlaintext = "json", false, false

	out := captureStdout(t, func() {
		if err := runTwofaShow(twofaShowCmd, nil); err != nil {
			t.Fatalf("runTwofaShow: %v", err)
		}
	})

	if strings.Contains(out, "JBSWY3DPEHPK3PXP") {
		t.Fatalf("2fa show leaked the secret:\n%s", out)
	}
	if strings.Contains(out, "otpauth://") {
		t.Fatalf("2fa show leaked the otpauth URI:\n%s", out)
	}
	if !strings.Contains(out, `"secret": "***"`) {
		t.Fatalf("2fa show did not render a masked secret:\n%s", out)
	}
	if !strings.Contains(out, `"enabled": true`) {
		t.Fatalf("2fa show dropped benign fields:\n%s", out)
	}
}

// captureStdout redirects os.Stdout while fn runs and returns what was written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	_ = w.Close()
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return string(data)
}

// TestK10CommandTree locks in the command/flag surface added by this ticket.
func TestK10CommandTree(t *testing.T) {
	find := func(name string) *cobra.Command {
		for _, c := range GetRootCmd().Commands() {
			if c.Name() == name {
				return c
			}
		}
		return nil
	}

	for _, name := range []string{"upgrade", "twofa", "asr", "fingerbot", "init", "turn"} {
		if find(name) == nil {
			t.Fatalf("root command %q is not registered", name)
		}
	}

	twofa := find("twofa")
	hasAlias := false
	for _, a := range twofa.Aliases {
		if a == "2fa" {
			hasAlias = true
		}
	}
	if !hasAlias {
		t.Errorf("twofa aliases = %v, want to include 2fa", twofa.Aliases)
	}

	sub := map[string][]string{
		"upgrade":   {"version", "check", "status", "log", "edid", "start", "upload", "download", "cancel", "reboot", "reset"},
		"twofa":     {"is-enabled", "show", "create", "init", "delete"},
		"asr":       {"status", "start", "stop"},
		"fingerbot": {"battery", "click", "upgrade"},
		"init":      {"status", "run", "change-password"},
		"turn":      {"get"},
	}
	for parent, children := range sub {
		p := find(parent)
		for _, child := range children {
			found := false
			for _, c := range p.Commands() {
				if c.Name() == child {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: missing subcommand %q", parent, child)
			}
		}
	}

	for _, guard := range [][2]string{
		{"upgrade", "reboot"}, {"upgrade", "reset"}, {"upgrade", "start"}, {"upgrade", "upload"},
		{"twofa", "create"}, {"twofa", "delete"}, {"twofa", "init"},
		{"fingerbot", "upgrade"}, {"init", "run"}, {"init", "change-password"},
	} {
		p := find(guard[0])
		var child *cobra.Command
		for _, c := range p.Commands() {
			if c.Name() == guard[1] {
				child = c
			}
		}
		if child == nil {
			t.Fatalf("%s %s: command not found", guard[0], guard[1])
		}
		if child.Flags().Lookup("yes") == nil {
			t.Errorf("%s %s: missing --yes flag", guard[0], guard[1])
		}
	}
}
