package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/api"
	"github.com/roboalchemist/kvm-cli/pkg/output"
	"github.com/spf13/cobra"
)

func TestCliValueString(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want string
	}{
		{"nil", nil, ""},
		{"string", "hi", "hi"},
		{"true", true, "true"},
		{"false", false, "false"},
		{"integral float", float64(11), "11"},
		{"decimal float", float64(20.6), "20.6"},
		{"slice", []any{"a", "b"}, `["a","b"]`},
		{"nested map", map[string]any{"k": float64(1)}, `{"k":1}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := cliValueString(tc.in); got != tc.want {
				t.Fatalf("cliValueString(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestCliFlattenRows(t *testing.T) {
	rows := cliFlattenRows("", map[string]any{
		"b": float64(2),
		"a": map[string]any{"y": true, "x": "s"},
		"c": []any{"p", "q"},
	})
	want := [][]string{
		{"a.x", "s"},
		{"a.y", "true"},
		{"b", "2"},
		{"c", `["p","q"]`},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %v, want %v", rows, want)
	}
	for i := range want {
		if rows[i][0] != want[i][0] || rows[i][1] != want[i][1] {
			t.Fatalf("row %d = %v, want %v", i, rows[i], want[i])
		}
	}
}

func TestCliFlattenRowsPrefix(t *testing.T) {
	rows := cliFlattenRows("cfg", map[string]any{"k": "v"})
	if len(rows) != 1 || rows[0][0] != "cfg.k" {
		t.Fatalf("rows = %v, want [[cfg.k v]]", rows)
	}
}

func TestCliGetPath(t *testing.T) {
	m := map[string]any{"a": map[string]any{"b": map[string]any{"c": "deep"}}}
	if v, ok := cliGetPath(m, "a", "b", "c"); !ok || v != "deep" {
		t.Fatalf("cliGetPath = %v, %v; want deep, true", v, ok)
	}
	if _, ok := cliGetPath(m, "a", "missing"); ok {
		t.Fatal("cliGetPath should report false for a missing path")
	}
}

func TestCliWithout(t *testing.T) {
	src := map[string]any{"a": 1, "success": true}
	out := cliWithout(src, "success")
	if _, ok := out["success"]; ok {
		t.Fatal("cliWithout did not remove the key")
	}
	if _, ok := src["success"]; !ok {
		t.Fatal("cliWithout mutated its input")
	}
}

func TestCliDeviceError(t *testing.T) {
	apiErr := &api.APIError{Code: "BadRequestError", Message: "timezone parameter is required", Status: 400}
	err := cliDeviceError(apiErr)
	if got := output.ErrorCode(err); got != "ERROR" {
		t.Fatalf("error code = %q, want ERROR", got)
	}
	if !strings.Contains(err.Error(), "timezone parameter is required") {
		t.Fatalf("error %q does not surface error_msg", err)
	}

	authErr := cliDeviceError(&api.APIError{Code: "Unauthorized", Message: "bad token", Status: 401})
	if got := output.ErrorCode(authErr); got != "AUTH_INVALID" {
		t.Fatalf("401 error code = %q, want AUTH_INVALID", got)
	}

	// Non-API errors pass through unchanged.
	plain := errors.New("boom")
	if got := cliDeviceError(plain); got != plain {
		t.Fatalf("plain error was wrapped: %v", got)
	}
	if cliDeviceError(nil) != nil {
		t.Fatal("cliDeviceError(nil) should be nil")
	}
}

func TestCliErrorCode(t *testing.T) {
	cases := map[int]string{
		400: "ERROR",
		401: "AUTH_INVALID",
		403: "FORBIDDEN",
		404: "NOT_FOUND",
		500: "DEVICE_ERROR",
		502: "DEVICE_ERROR",
	}
	for status, want := range cases {
		if got := cliErrorCode(status); got != want {
			t.Errorf("cliErrorCode(%d) = %q, want %q", status, got, want)
		}
	}
}

func TestCliStringSlice(t *testing.T) {
	if got := cliStringSlice([]any{"a", "b"}); len(got) != 2 || got[0] != "a" {
		t.Fatalf("cliStringSlice([]any) = %v", got)
	}
	if got := cliStringSlice([]string{"x"}); len(got) != 1 || got[0] != "x" {
		t.Fatalf("cliStringSlice([]string) = %v", got)
	}
	if got := cliStringSlice(42); got != nil {
		t.Fatalf("cliStringSlice(42) = %v, want nil", got)
	}
}

func TestNetworkDataFallsBackToTypedFields(t *testing.T) {
	dhcp := true
	n := api.NetworkConfig{Config: api.NetworkConfigDetails{
		Interface:  "eth0",
		IPAddress:  "10.0.0.2",
		IsDHCP:     &dhcp,
		DNSServers: []string{"1.1.1.1"},
	}}
	data := networkData(n)
	if data["interface"] != "eth0" || data["is_dhcp"] != true {
		t.Fatalf("networkData = %v", data)
	}
	if _, ok := data["dns_servers"].([]string); !ok {
		t.Fatalf("dns_servers = %T, want []string", data["dns_servers"])
	}
}

func TestNewCommandsAreWellFormed(t *testing.T) {
	roots := []*cobra.Command{infoCmd, authCmd, systemCmd}
	for _, c := range roots {
		if c.Use == "" || c.Short == "" || c.Example == "" || c.Args == nil {
			t.Errorf("command %q missing required metadata (Use=%q Short=%q Example=%q Args=%v)",
				c.Name(), c.Use, c.Short, c.Example, c.Args)
		}
		for _, sub := range c.Commands() {
			if sub.Use == "" || sub.Short == "" || sub.Example == "" || sub.Args == nil {
				t.Errorf("subcommand %q of %q missing required metadata", sub.Name(), c.Name())
			}
		}
	}
}

func TestSystemTimezoneHasListFlag(t *testing.T) {
	if f := systemTimezoneCmd.Flags().Lookup("list"); f == nil {
		t.Fatal("system timezone is missing the --list flag")
	}
}

func TestInfoRowsSummary(t *testing.T) {
	raw := api.RawMap{
		"auth": map[string]any{"enabled": true},
		"extras": map[string]any{
			"vnc":   map[string]any{"enabled": false, "started": false},
			"janus": map[string]any{"enabled": true, "started": true},
		},
	}
	rows := infoRows(raw)
	joined := map[string]string{}
	for _, r := range rows {
		joined[r[0]] = r[1]
	}
	if joined["auth.enabled"] != "true" {
		t.Errorf("auth.enabled row = %q, want true", joined["auth.enabled"])
	}
	if joined["extras.janus"] != "enabled=true started=true" {
		t.Errorf("extras.janus row = %q", joined["extras.janus"])
	}
	if joined["extras.vnc"] != "enabled=false started=false" {
		t.Errorf("extras.vnc row = %q", joined["extras.vnc"])
	}
}
