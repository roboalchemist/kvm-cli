package cmd

import (
	"net/url"
	"strings"
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

// FindCmd resolves a space-separated command path under rootCmd.
func FindCmd(t *testing.T, path string) *cobra.Command {
	t.Helper()
	cur := rootCmd
	for _, name := range strings.Fields(path) {
		var next *cobra.Command
		for _, c := range cur.Commands() {
			if c.Name() == name {
				next = c
				break
			}
		}
		if next == nil {
			t.Fatalf("command %q not found under %q", name, cur.CommandPath())
		}
		cur = next
	}
	return cur
}

// TestCommandTree verifies every command this ticket owns is registered with
// a usage line, a short description, an Args validator and at least one example.
func TestCommandTree(t *testing.T) {
	paths := []string{
		"tailscale", "tailscale status", "tailscale start", "tailscale stop",
		"tailscale login", "tailscale login-url", "tailscale login-status",
		"tailscale logout", "tailscale config",
		"netbird", "netbird info", "netbird login", "netbird logout",
		"netbird start", "netbird stop",
		"zerotier", "zerotier status", "zerotier start", "zerotier stop",
		"zerotier set-token",
		"repeater", "repeater scan", "repeater connect", "repeater disconnect",
		"repeater enable", "repeater status", "repeater saved",
		"repeater remove-saved",
		"ap", "ap status", "ap enable", "ap open-last", "ap close-all",
		"modem", "modem at", "modem input-pin", "modem sim-setting",
		"system set-config", "system set-network", "system set-hostname",
		"system set-firewall", "system set-param", "system ssl-cert",
		"system set-time", "system set-timezone",
	}
	for _, p := range paths {
		c := FindCmd(t, p)
		if c.Use == "" {
			t.Errorf("%s: empty Use", p)
		}
		if strings.TrimSpace(c.Short) == "" {
			t.Errorf("%s: empty Short", p)
		}
		if c.Example == "" {
			t.Errorf("%s: empty Example", p)
		}
		if c.Args == nil {
			t.Errorf("%s: nil Args", p)
		}
		if c.RunE == nil && len(c.Commands()) == 0 {
			t.Errorf("%s: neither RunE nor subcommands", p)
		}
	}
}

// guarded maps a command path to the function that performs its destructive
// action and the arguments needed to reach the --yes check.
func TestYesGuards(t *testing.T) {
	saveYes := func(t *testing.T) {
		t.Helper()
		ts, nb, zt, rp, sysw := tsYes, nbYes, ztYes, rpYes, syswYes
		tsYes, nbYes, ztYes, rpYes, syswYes = false, false, false, false, false
		t.Cleanup(func() { tsYes, nbYes, ztYes, rpYes, syswYes = ts, nb, zt, rp, sysw })
	}
	saveYes(t)

	cases := []struct {
		name string
		run  func(*cobra.Command, []string) error
		args []string
		cmd  *cobra.Command
	}{
		{"tailscale stop", runTailscaleStop, nil, nil},
		{"tailscale logout", runTailscaleLogout, nil, nil},
		{"netbird logout", runNetbirdLogout, nil, nil},
		{"netbird stop", runNetbirdStop, nil, nil},
		{"zerotier stop", runZerotierStop, nil, nil},
		{"repeater disconnect", runRepeaterDisconnect, nil, nil},
		{"repeater remove-saved", runRepeaterRemoveSaved, []string{"X"}, nil},
		{"system set-config", runSystemSetConfig, nil, nil},
		{"system set-network", runSystemSetNetwork, nil, nil},
		{"system set-hostname", runSystemSetHostname, []string{"h"}, nil},
		{"system set-firewall", runSystemSetFirewall, nil, nil},
		{"system set-param", runSystemSetParam, nil, nil},
		{"system set-time", runSystemSetTime, nil, nil},
		{"system set-timezone", runSystemSetTimezone, []string{"UTC"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run(tc.cmd, tc.args)
			if err == nil {
				t.Fatalf("expected CONFIRMATION_REQUIRED, got nil")
			}
			if code := output.ErrorCode(err); code != "CONFIRMATION_REQUIRED" {
				t.Fatalf("code = %q, want CONFIRMATION_REQUIRED (err: %v)", code, err)
			}
		})
	}

	// ssl-cert only guards the write path (GET is a safe read); force it.
	t.Run("system ssl-cert", func(t *testing.T) {
		def := syswCertDefault
		syswCertDefault = true
		t.Cleanup(func() { syswCertDefault = def })
		err := runSystemSSLCert(systemSSLCertCmd, nil)
		if code := output.ErrorCode(err); code != "CONFIRMATION_REQUIRED" {
			t.Fatalf("code = %q, want CONFIRMATION_REQUIRED (err: %v)", code, err)
		}
	})
}

// TestGuardedCommandsExposeFlag checks that every guarded command advertises
// the --yes flag in its help (acceptance requires it on set-hostname).
func TestGuardedCommandsExposeFlag(t *testing.T) {
	for _, p := range []string{
		"tailscale stop", "tailscale logout", "netbird logout", "netbird stop",
		"zerotier stop", "repeater disconnect", "repeater remove-saved",
		"system set-config", "system set-network", "system set-hostname",
		"system set-firewall", "system set-param", "system ssl-cert",
		"system set-time", "system set-timezone",
	} {
		c := FindCmd(t, p)
		if c.Flags().Lookup("yes") == nil {
			t.Errorf("%s: missing --yes flag", p)
		}
	}
}

func TestParseSets(t *testing.T) {
	m, err := syswParseSets([]string{"a=1", "b=two=three", "c=d"})
	if err != nil {
		t.Fatalf("syswParseSets error: %v", err)
	}
	want := map[string]string{"a": "1", "b": "two=three", "c": "d"}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("m[%q] = %q, want %q", k, m[k], v)
		}
	}

	if _, err := syswParseSets([]string{"novalue"}); output.ErrorCode(err) != "USAGE" {
		t.Errorf("missing '=' code = %q, want USAGE", output.ErrorCode(err))
	}
	if _, err := syswParseSets([]string{"=x"}); output.ErrorCode(err) != "USAGE" {
		t.Errorf("empty key code = %q, want USAGE", output.ErrorCode(err))
	}
}

func TestMergeSetsOverrides(t *testing.T) {
	v := url.Values{"mode": {"dhcp"}}
	if err := syswMergeSets(v, []string{"mode=static", "ip_address=10.0.0.1"}); err != nil {
		t.Fatalf("merge error: %v", err)
	}
	if got := v.Get("mode"); got != "static" {
		t.Errorf("mode = %q, want static (override)", got)
	}
	if got := v.Get("ip_address"); got != "10.0.0.1" {
		t.Errorf("ip_address = %q, want 10.0.0.1", got)
	}
}

func TestJSONBody(t *testing.T) {
	body, err := syswJSONBody("", []string{"theme_mode=dark"})
	if err != nil {
		t.Fatalf("syswJSONBody error: %v", err)
	}
	if body["theme_mode"] != "dark" {
		t.Fatalf("body = %v, want theme_mode=dark", body)
	}

	if _, err := syswJSONBody("/nonexistent/file.json", nil); output.ErrorCode(err) != "USAGE" {
		t.Errorf("missing file code = %q, want USAGE", output.ErrorCode(err))
	}
}
