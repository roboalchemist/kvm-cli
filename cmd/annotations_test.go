package cmd

import (
	"strings"
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// findCommandByPath resolves a space-separated command path (e.g. "hid mouse
// click") within the root command tree.
func findCommandByPath(t *testing.T, path string) *cobra.Command {
	t.Helper()
	c := GetRootCmd()
	for _, p := range strings.Fields(path) {
		var next *cobra.Command
		for _, sub := range c.Commands() {
			if sub.Name() == p {
				next = sub
				break
			}
		}
		if next == nil {
			t.Fatalf("command path %q: %q not found under %q", path, p, c.CommandPath())
		}
		c = next
	}
	return c
}

// saveAnnotationGlobals snapshots and restores the global flag values mutated by the
// tests.
func saveAnnotationGlobals(t *testing.T) {
	t.Helper()
	version, dryRun, verbose := flagVersion, flagDryRun, flagVerbose
	format, fields, jq := flagFormat, flagFields, flagJQ
	jsonOut, plain := flagJSON, flagPlaintext
	t.Cleanup(func() {
		flagVersion, flagDryRun, flagVerbose = version, dryRun, verbose
		flagFormat, flagFields, flagJQ = format, fields, jq
		flagJSON, flagPlaintext = jsonOut, plain
	})
}

// TestWriteAnnotations locks in the set of commands enforced by --dry-run.
// Every mutator must carry the write annotation; read-only commands must not,
// so read commands keep contacting the device.
func TestWriteAnnotations(t *testing.T) {
	writePaths := []string{
		"atx power", "atx reset", "atx click",
		"msd connect", "msd disconnect", "msd set-connected", "msd format",
		"msd remove", "msd write", "msd write-remote", "msd set-params",
		"wol add", "wol remove", "wol wake",
		"screen set-background", "screen delete-background", "screen set-date-format",
		"screen set-time-format", "screen set-mode",
		"system set-config", "system set-network", "system set-hostname",
		"system set-firewall", "system set-param", "system ssl-cert",
		"system set-time", "system set-timezone",
		"tailscale start", "tailscale stop", "tailscale login", "tailscale login-url",
		"tailscale logout", "tailscale config",
		"netbird login", "netbird logout", "netbird start", "netbird stop",
		"zerotier start", "zerotier stop", "zerotier set-token",
		"repeater connect", "repeater disconnect", "repeater enable", "repeater remove-saved",
		"ap enable", "ap open-last", "ap close-all",
		"modem at", "modem input-pin", "modem sim-setting",
		"upgrade start", "upgrade upload", "upgrade download", "upgrade cancel",
		"upgrade reboot", "upgrade reset",
		"twofa create", "twofa init", "twofa delete",
		"fingerbot click", "fingerbot upgrade",
		"init run", "init change-password",
		"config set", "config unset",
		"switch set-active",
		"streamer set-params",
		"asr start", "asr stop",
		"hid key", "hid combo", "hid type", "hid print", "hid set-mouse-output",
		"hid mouse move", "hid mouse click", "hid mouse down", "hid mouse up", "hid mouse wheel",
		"auth login", "auth logout",
		"skill add",
	}
	for _, p := range writePaths {
		if c := findCommandByPath(t, p); !isWriteCommand(c) {
			t.Errorf("write command %q is not annotated with write=true", p)
		}
	}

	readPaths := []string{
		"atx status", "msd status", "msd partitions", "wol list", "wol scan",
		"screen status", "screen background",
		"system capability", "system param", "system config", "system hostname",
		"system network", "system time", "system timezone", "system otg", "system firewall",
		"tailscale status", "tailscale login-status",
		"netbird info", "zerotier status",
		"repeater scan", "repeater status", "repeater saved",
		"ap status",
		"upgrade version", "upgrade check", "upgrade status", "upgrade log", "upgrade edid",
		"twofa is-enabled", "twofa show", "fingerbot battery", "init status",
		"config list", "config get",
		"switch status", "streamer status", "streamer snapshot", "asr status",
		"hid status", "hid keys", "auth check", "auth status",
		"screenshot", "info", "version",
	}
	for _, p := range readPaths {
		if c := findCommandByPath(t, p); isWriteCommand(c) {
			t.Errorf("read command %q must not be annotated as a write", p)
		}
	}
}

func TestVersionRequested(t *testing.T) {
	cases := []struct {
		argv []string
		want bool
	}{
		{nil, false},
		{[]string{"--help"}, false},
		{[]string{"-V"}, true},
		{[]string{"--version"}, true},
		{[]string{"atx", "power", "--version", "--url", "http://127.0.0.1:1"}, true},
		{[]string{"atx", "power", "--version=false"}, false},
		{[]string{"atx", "click", "--", "--version"}, false},
		{[]string{"--help", "--version"}, false},
		{[]string{"--version", "--help"}, false},
	}
	for _, tc := range cases {
		if got := versionRequested(tc.argv); got != tc.want {
			t.Errorf("versionRequested(%v) = %v, want %v", tc.argv, got, tc.want)
		}
	}
}

func TestFormatValidation(t *testing.T) {
	saveAnnotationGlobals(t)
	flagJSON, flagPlaintext = false, false

	valid := []string{"", "table", "json", "plaintext", "plain", "yaml", "yml", "TABLE", "JSON"}
	for _, f := range valid {
		flagFormat = f
		if err := validateFormatFlag(); err != nil {
			t.Errorf("validateFormatFlag(%q) = %v, want nil", f, err)
		}
	}

	flagFormat = "bogus"
	err := validateFormatFlag()
	if output.ErrorCode(err) != "USAGE" {
		t.Fatalf("validateFormatFlag(bogus) code = %q, want USAGE (err: %v)", output.ErrorCode(err), err)
	}

	// The --json/--plaintext shorthands win, so a stale --format is ignored.
	flagFormat = "bogus"
	flagJSON = true
	if err := validateFormatFlag(); err != nil {
		t.Errorf("validateFormatFlag with --json = %v, want nil", err)
	}
}

func TestDryRunShortCircuit(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")
	saveAnnotationGlobals(t)
	flagVersion, flagFormat, flagJSON, flagPlaintext, flagVerbose = false, "table", false, false, false
	flagDryRun = true

	cmd := findCommandByPath(t, "atx power")
	out := captureStdout(t, func() {
		if err := GetRootCmd().PersistentPreRunE(cmd, nil); err != errStopSuccess {
			t.Fatalf("PersistentPreRunE = %v, want errStopSuccess", err)
		}
	})
	if !strings.Contains(out, "dry run") || !strings.Contains(out, "atx power") {
		t.Fatalf("dry-run preview missing expected content:\n%s", out)
	}
}

func TestReadCommandNotShortCircuited(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KVM_CONFIG", "")
	t.Setenv("GLKVM_CONFIG", "")
	saveAnnotationGlobals(t)
	flagVersion, flagFormat, flagJSON, flagPlaintext, flagVerbose = false, "table", false, false, false
	flagDryRun = true

	cmd := findCommandByPath(t, "atx status")
	out := captureStdout(t, func() {
		if err := GetRootCmd().PersistentPreRunE(cmd, nil); err != nil {
			t.Fatalf("PersistentPreRunE for read command = %v, want nil", err)
		}
	})
	if strings.Contains(out, "dry run") {
		t.Fatalf("read command was short-circuited by --dry-run:\n%s", out)
	}
}

func TestVersionShortCircuit(t *testing.T) {
	saveAnnotationGlobals(t)
	flagVersion, flagDryRun = true, false

	cmd := findCommandByPath(t, "atx power")
	out := captureStdout(t, func() {
		if err := GetRootCmd().PersistentPreRunE(cmd, nil); err != errStopSuccess {
			t.Fatalf("PersistentPreRunE = %v, want errStopSuccess", err)
		}
	})
	if !strings.HasPrefix(out, "kvm-cli ") {
		t.Fatalf("version output = %q, want to start with %q", out, "kvm-cli ")
	}
	if !strings.Contains(out, "License MIT") {
		t.Fatalf("version output missing license block:\n%s", out)
	}
}

// TestAtxForceShorthand verifies the ATX action commands expose both the
// standard --yes gate and its GNU short form -f/--force.
func TestAtxForceShorthand(t *testing.T) {
	for _, path := range []string{"atx power", "atx reset", "atx click"} {
		cmd := findCommandByPath(t, path)

		f := cmd.Flags().Lookup("force")
		if f == nil {
			t.Fatalf("%s: --force flag is not registered", path)
		}
		if f.Shorthand != "f" {
			t.Fatalf("%s --force shorthand = %q, want %q", path, f.Shorthand, "f")
		}
		if f.Usage == "" {
			t.Fatalf("%s --force has no usage text", path)
		}
		if cmd.Flags().Lookup("yes") == nil {
			t.Fatalf("%s: --yes flag is not registered", path)
		}
	}
}
