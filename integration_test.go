//go:build integration

package main

// Integration tests for kvm-cli ().
//
// These tests drive the real binary as a subprocess against a live GL.iNet
// Comet PoE Remote KVM. They are split into two groups:
//
// - Read-only tests always run (when the device is reachable) and exercise
// every data-returning command across the full cross-product of global
// output flags (--json, --plaintext, --no-color, --fields, --jq, --debug).
// - Write tests are gated by READONLY=1 (they skip when READONLY=1) and use
// only idempotent, reversible test data (a temporary WOL entry, a
// custom-screen format round-trip, and config lifecycle). They also assert
// that destructive commands refuse to run without --yes.
//
// Credentials come from the environment (KVM_URL/KVM_USERNAME/KVM_PASSWORD or
// the GLKVM_* aliases) or, failing that, gopass (env/GLKVM_*). When no
// credentials are configured or the device is unreachable every device-backed
// test skips cleanly.
//
// Command coverage is verified structurally by TestIntegration_Coverage, which
// walks the *runtime* Cobra tree and cross-references it against the command
// lists declared below.
//
// Run with:
//
//	go test -run Integration -count=1 ./...
//	READONLY=1 go test -run Integration -count=1 ./...
//
// The ast-grep coverage cross-check is run manually (see the report); it
// extracts every cobra.Command Use string / flag from cmd/ and diffs it against
// the command lists declared here.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/roboalchemist/kvm-cli/cmd"
	"github.com/spf13/cobra"
)

// ---------------------------------------------------------------------------
// Test harness
// ---------------------------------------------------------------------------

const (
	testWOLMAC  = "02:12:34:56:78:9a" // locally administered; no real NIC uses it
	testWOLAlt  = "02:12:34:56:78:9b"
	testWOLName = "-integration"
)

var (
	binPath    string
	cliEnv     []string
	liveReady  bool
	liveReason string
)

// cliResult captures the outcome of one subprocess invocation.
type cliResult struct {
	stdout string
	stderr string
	code   int
}

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "kvm-cli-it-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "integration: mkdtemp: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	binPath = filepath.Join(dir, "kvm-cli")
	build := exec.Command("go", "build", "-o", binPath, ".")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "integration: build failed: %v\n", err)
		os.Exit(1)
	}

	// Isolate configuration: all invocations share a throwaway config file so
	// the developer's real ~/.config/kvm-cli/config.json is never touched.
	configPath := filepath.Join(dir, "config.json")
	cliEnv = append(os.Environ(),
		"KVM_CONFIG="+configPath,
		"GLKVM_CONFIG="+configPath,
		// Skip the self-signed-cert warning so stderr is clean JSON on errors.
		"KVM_INSECURE=true",
		"KVM_TIMEOUT=30s",
	)

	url := firstNonEmpty(os.Getenv("KVM_URL"), os.Getenv("GLKVM_URL"))
	user := firstNonEmpty(os.Getenv("KVM_USERNAME"), os.Getenv("GLKVM_USERNAME"))
	pass := firstNonEmpty(os.Getenv("KVM_PASSWORD"), os.Getenv("GLKVM_PASSWORD"))
	if url == "" {
		url = gopass("GLKVM_URL")
	}
	if user == "" {
		user = gopass("GLKVM_USERNAME")
	}
	if pass == "" {
		pass = gopass("GLKVM_PASSWORD")
	}

	switch {
	case url == "":
		liveReason = "KVM URL not configured (set KVM_URL or gopass env/GLKVM_URL)"
	case user == "":
		liveReason = "KVM username not configured"
	case pass == "":
		liveReason = "KVM password not configured"
	default:
		cliEnv = append(cliEnv, "KVM_URL="+url, "KVM_USERNAME="+user, "KVM_PASSWORD="+pass)
		if probeDevice() {
			liveReady = true
		} else {
			liveReason = fmt.Sprintf("device at %s unreachable", url)
		}
	}

	os.Exit(m.Run())
}

// probeDevice reports whether the live device answers a cheap read command.
func probeDevice() bool {
	r := runRaw(25*time.Second, nil, "info", "--json")
	return r.code == 0 && json.Valid([]byte(strings.TrimSpace(r.stdout)))
}

func requireLive(t *testing.T) {
	t.Helper()
	if liveReady {
		return
	}
	t.Skipf("integration: %s", liveReason)
}

// requireWrite gates write-side tests behind READONLY.
func requireWrite(t *testing.T) {
	t.Helper()
	requireLive(t)
	if readonly() {
		t.Skip("READONLY=1: skipping write tests")
	}
}

func readonly() bool { return os.Getenv("READONLY") == "1" }

// runRaw executes the built binary with the base environment plus optional
// overrides (which replace matching keys in the base environment).
func runRaw(timeout time.Duration, overrides map[string]string, args ...string) cliResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	c := exec.CommandContext(ctx, binPath, args...)
	c.Env = withOverrides(cliEnv, overrides)

	var so, se bytes.Buffer
	c.Stdout, c.Stderr = &so, &se
	err := c.Run()

	code := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			code = -1 // failed to start or killed (timeout)
		}
	}
	return cliResult{stdout: so.String(), stderr: se.String(), code: code}
}

// runCLI runs a command with the default per-command timeout (45s).
func runCLI(t *testing.T, args ...string) cliResult {
	t.Helper()
	return runCLIEnv(t, nil, args...)
}

func runCLIEnv(t *testing.T, overrides map[string]string, args ...string) cliResult {
	t.Helper()
	r := runRaw(45*time.Second, overrides, args...)
	if r.code == -1 {
		t.Fatalf("kvm-cli %v: process did not complete (timeout)", args)
	}
	return r
}

func withOverrides(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		// Return a copy so callers can never mutate the shared slice.
		return append([]string(nil), base...)
	}
	out := make([]string, 0, len(base)+len(overrides))
	for _, kv := range base {
		key, _, _ := strings.Cut(kv, "=")
		if _, replaced := overrides[key]; replaced {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overrides {
		out = append(out, k+"="+v)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func gopass(key string) string {
	out, err := exec.Command("gopass", "show", "-o", "env/"+key).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ---------------------------------------------------------------------------
// Read-only cross-product tests
// ---------------------------------------------------------------------------

// readCommands are every data-returning command expected to render JSON,
// exercised against the full cross-product of global output flags.
var readCommands = []string{
	"ap status",
	"asr status",
	"atx status",
	"auth check",
	"auth status",
	"fingerbot battery",
	"hid keys",
	"hid status",
	"info",
	"init status",
	"modem at AT",
	"modem sim-setting",
	"msd partitions",
	"msd status",
	"netbird info",
	"repeater saved",
	"repeater scan",
	"repeater status",
	"screen background",
	"screen status",
	"streamer status",
	"switch status",
	"system capability",
	"system config",
	"system firewall",
	"system hostname",
	"system network",
	"system otg",
	"system param",
	"system time",
	"system timezone",
	"tailscale config",
	"tailscale login-status",
	"tailscale status",
	"turn get",
	"twofa is-enabled",
	"twofa show",
	"upgrade check",
	"upgrade edid",
	"upgrade status",
	"upgrade version",
	"version",
	"wol list",
	"wol scan",
	"zerotier status",
}

// allowedDeviceCodes are the structured error codes a read command may return
// and still be considered "handled gracefully" (usually a firmware limitation,
// an expected empty state, or a transient device-side fault). Anything else —
// AUTH_INVALID, NETWORK_ERROR, USAGE — indicates a broken test environment and
// fails the test.
var allowedDeviceCodes = map[string]bool{
	"NOT_SUPPORTED": true,
	"NOT_ENABLED":   true,
	"NOT_FOUND":     true,
	"ERROR":         true,
	"DEVICE_ERROR":  true,
	"BAD_REQUEST":   true,
	"FORBIDDEN":     true,
}

// localDebugExceptions are commands whose output is unconditional and therefore
// never emit HTTP-level debug logging even with --debug.
//
// - version / hid keys / config list never issue an HTTP request.
// - auth status resolves credentials and calls client.Check() directly
// instead of going through cmd.NewClient, so it does not install the debug
// logger (noted as a minor CLI gap; it still exercises the --debug flag
// without erroring).
var localDebugExceptions = map[string]bool{
	"version":     true,
	"hid keys":    true,
	"config list": true,
	"auth status": true,
}

// allowEmptyPlaintext lists commands whose table can legitimately have zero
// rows even when the JSON envelope is non-empty.
var allowEmptyPlaintext = map[string]bool{
	"wol list":          true,
	"wol scan":          true,
	"screen background": true, // renders no rows when no background is set
}

func TestIntegration_ReadCommands_CrossProduct(t *testing.T) {
	requireLive(t)
	for _, path := range readCommands {
		path := path
		t.Run(strings.ReplaceAll(path, " ", "_"), func(t *testing.T) {
			exerciseReadCommand(t, path)
		})
	}
}

// exerciseReadCommand runs one command with every global output flag and
// asserts the result is well-formed.
func exerciseReadCommand(t *testing.T, path string) {
	t.Helper()
	base := strings.Fields(path)

	// --- baseline --json -----------------------------------------------------
	jr := runCLI(t, appendArgs(base, "--json")...)
	limited := false
	var data any
	if jr.code != 0 {
		code := assertJSONErrorEnvelope(t, path, jr)
		if !allowedDeviceCodes[code] {
			t.Fatalf("%s --json: unexpected error code %q (%s)", path, code, firstLine(jr.stderr))
		}
		limited = true
	} else if err := json.Unmarshal([]byte(jr.stdout), &data); err != nil {
		t.Fatalf("%s --json: output is not valid JSON: %v\n%s", path, err, jr.stdout)
	}

	// --- --no-color (table mode) --------------------------------------------
	nr := runCLI(t, appendArgs(base, "--no-color")...)
	assertNoANSI(t, path+" --no-color", nr.stdout)
	if !limited && nr.code != 0 {
		t.Fatalf("%s --no-color: exit %d: %s", path, nr.code, firstLine(nr.stderr))
	}

	// --- --plaintext ---------------------------------------------------------
	pr := runCLI(t, appendArgs(base, "--plaintext")...)
	assertNoANSI(t, path+" --plaintext", pr.stdout)
	if limited {
		if pr.code == 0 {
			t.Fatalf("%s --plaintext: expected same device limitation as --json, got success", path)
		}
	} else {
		if pr.code != 0 {
			t.Fatalf("%s --plaintext: exit %d: %s", path, pr.code, firstLine(pr.stderr))
		}
		assertPlaintext(t, path, data, pr.stdout)
	}

	// --- --fields ------------------------------------------------------------
	fields := pickFields(data)
	if len(fields) == 0 {
		fields = []string{"___missing__"}
	}
	fr := runCLI(t, appendArgs(base, "--json", "--fields", strings.Join(fields, ","))...)
	if limited {
		if fr.code == 0 {
			t.Fatalf("%s --fields: expected device limitation, got success", path)
		}
	} else {
		if fr.code != 0 {
			t.Fatalf("%s --fields: exit %d: %s", path, fr.code, firstLine(fr.stderr))
		}
		assertProjected(t, path, fr.stdout, fields)
	}

	// --- --jq ----------------------------------------------------------------
	expr := pickJQ(data)
	qr := runCLI(t, appendArgs(base, "--json", "--jq", expr)...)
	if limited {
		if qr.code == 0 {
			t.Fatalf("%s --jq: expected device limitation, got success", path)
		}
	} else {
		if qr.code != 0 {
			t.Fatalf("%s --jq %q: exit %d: %s", path, expr, qr.code, firstLine(qr.stderr))
		}
		out := strings.TrimSpace(qr.stdout)
		if out == "" {
			t.Fatalf("%s --jq %q: produced no output", path, expr)
		}
		// gojq emits exactly one JSON value per result line; our expressions
		// always yield a single value.
		if !json.Valid([]byte(out)) {
			t.Fatalf("%s --jq %q: output is not valid JSON: %q", path, expr, out)
		}
	}

	// --- --debug -------------------------------------------------------------
	dr := runCLI(t, appendArgs(base, "--debug")...)
	assertNoANSI(t, path+" --debug", dr.stdout)
	if !localDebugExceptions[path] && !strings.Contains(dr.stderr, "[debug]") {
		t.Fatalf("%s --debug: no debug logging found on stderr", path)
	}
}

// appendArgs returns base with extra appended, without aliasing base's array.
func appendArgs(base []string, extra ...string) []string {
	out := make([]string, 0, len(base)+len(extra))
	out = append(out, base...)
	out = append(out, extra...)
	return out
}

// assertJSONErrorEnvelope verifies that stderr holds the structured error
// envelope and returns its code.
func assertJSONErrorEnvelope(t *testing.T, path string, r cliResult) string {
	t.Helper()
	trimmed := strings.TrimSpace(r.stderr)
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		t.Fatalf("%s: expected JSON error envelope on stderr, got: %q", path, firstLine(r.stderr))
	}
	if env.Error.Code == "" {
		t.Fatalf("%s: error envelope missing code: %s", path, trimmed)
	}
	return env.Error.Code
}

// pickFields returns up to two top-level JSON keys suitable for --fields.
func pickFields(data any) []string {
	switch v := data.(type) {
	case map[string]any:
		keys := sortedKeys(v)
		if len(keys) >= 2 {
			return keys[:2]
		}
		return keys
	case []any:
		if len(v) > 0 {
			if m, ok := v[0].(map[string]any); ok {
				keys := sortedKeys(m)
				if len(keys) >= 2 {
					return keys[:2]
				}
				return keys
			}
		}
	}
	return nil
}

// pickJQ returns a jq expression guaranteed to produce exactly one result for
// data.
func pickJQ(data any) string {
	switch v := data.(type) {
	case map[string]any:
		keys := sortedKeys(v)
		if len(keys) == 0 {
			return "length"
		}
		return fmt.Sprintf(".[%q]", keys[0])
	case []any:
		if len(v) == 0 {
			return "length"
		}
		return ".[0]"
	}
	return "."
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// dataNonEmpty reports whether the parsed JSON carries any rows.
func dataNonEmpty(data any) bool {
	switch v := data.(type) {
	case nil:
		return false
	case map[string]any:
		return len(v) > 0
	case []any:
		return len(v) > 0
	default:
		return true
	}
}

// wantsTab reports whether plaintext output should contain a tab for the given
// JSON shape (maps of 2+ keys, or arrays of maps with 2+ keys).
func wantsTab(data any) bool {
	switch v := data.(type) {
	case map[string]any:
		return len(v) >= 2
	case []any:
		if len(v) > 0 {
			if m, ok := v[0].(map[string]any); ok {
				return len(m) >= 2
			}
		}
	}
	return false
}

func assertPlaintext(t *testing.T, path string, data any, out string) {
	t.Helper()
	if !dataNonEmpty(data) || allowEmptyPlaintext[path] {
		return
	}
	if strings.TrimSpace(out) == "" {
		t.Fatalf("%s --plaintext: empty output for non-empty data", path)
	}
	if wantsTab(data) && !strings.Contains(out, "\t") {
		t.Fatalf("%s --plaintext: expected tab-separated output, got %q", path, firstLine(out))
	}
}

// assertProjected verifies that --fields produced parseable JSON containing none
// of the keys outside the requested set (fields not present are dropped).
func assertProjected(t *testing.T, path, out string, fields []string) {
	t.Helper()
	var proj any
	if err := json.Unmarshal([]byte(out), &proj); err != nil {
		t.Fatalf("%s --fields: output is not valid JSON: %v\n%s", path, err, out)
	}
	allowed := map[string]bool{}
	for _, f := range fields {
		allowed[f] = true
	}
	var check func(v any)
	check = func(v any) {
		switch val := v.(type) {
		case map[string]any:
			for k := range val {
				if !allowed[k] {
					t.Errorf("%s --fields: unexpected key %q in projection", path, k)
				}
			}
		case []any:
			for _, item := range val {
				check(item)
			}
		}
	}
	check(proj)
}

func assertNoANSI(t *testing.T, label, s string) {
	t.Helper()
	if strings.Contains(s, "\x1b[") {
		t.Fatalf("%s: output contains ANSI escape sequences", label)
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ---------------------------------------------------------------------------
// Dedicated read tests for binary / local commands
// ---------------------------------------------------------------------------

// TestIntegration_Screenshot captures a JPEG to a temp file and validates it.
func TestIntegration_Screenshot(t *testing.T) {
	requireLive(t)
	out := filepath.Join(t.TempDir(), "shot.jpg")
	r := runCLI(t, "screenshot", "--output", out, "--json")
	if r.code != 0 {
		if code, ok := structuredErrorCode(r.stderr); ok && allowedDeviceCodes[code] {
			t.Skipf("screenshot unavailable on this device (%s): %s", code, firstLine(r.stderr))
		}
		t.Fatalf("screenshot: exit %d: %s", r.code, firstLine(r.stderr))
	}
	// The JSON metadata must be well-formed.
	var meta []struct {
		Path  string `json:"path"`
		Bytes int    `json:"bytes"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &meta); err != nil {
		t.Fatalf("screenshot --json: invalid JSON: %v\n%s", err, r.stdout)
	}
	if len(meta) == 0 || meta[0].Bytes == 0 {
		t.Fatalf("screenshot --json: empty metadata: %s", r.stdout)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("screenshot: read output: %v", err)
	}
	if len(data) < 3 || data[0] != 0xFF || data[1] != 0xD8 || data[2] != 0xFF {
		t.Fatalf("screenshot: %s is not a JPEG (first bytes: % x)", out, data[:min(4, len(data))])
	}
	t.Logf("screenshot: wrote %d bytes JPEG", len(data))
}

// TestIntegration_ConfigLocal exercises the local (non-device) config
// lifecycle, which ignores the output flags by design.
func TestIntegration_ConfigLocal(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	env := map[string]string{"KVM_CONFIG": cfg, "GLKVM_CONFIG": cfg}

	r := runCLIEnv(t, env, "config", "list")
	if r.code != 0 {
		t.Fatalf("config list: exit %d: %s", r.code, firstLine(r.stderr))
	}

	if r := runCLIEnv(t, env, "config", "set", "username", "-tmp"); r.code != 0 {
		t.Fatalf("config set username: exit %d: %s", r.code, firstLine(r.stderr))
	}
	if r := runCLIEnv(t, env, "config", "get", "username"); r.code != 0 || !strings.Contains(r.stdout, "-tmp") {
		t.Fatalf("config get username: exit %d out=%q", r.code, firstLine(r.stdout))
	}
	if r := runCLIEnv(t, env, "config", "set", "output_format", "json"); r.code != 0 {
		t.Fatalf("config set output_format: exit %d: %s", r.code, firstLine(r.stderr))
	}
	if r := runCLIEnv(t, env, "config", "get", "output_format"); r.code != 0 || !strings.Contains(r.stdout, "json") {
		t.Fatalf("config get output_format: exit %d out=%q", r.code, firstLine(r.stdout))
	}
	// A malformed value must be rejected with a usage error (exit 2).
	if r := runCLIEnv(t, env, "config", "set", "timeout", "nonsense"); r.code != 2 {
		t.Fatalf("config set timeout nonsense: exit %d, want 2", r.code)
	}
	if r := runCLIEnv(t, env, "config", "unset", "username"); r.code != 0 {
		t.Fatalf("config unset username: exit %d: %s", r.code, firstLine(r.stderr))
	}
	if r := runCLIEnv(t, env, "config", "get", "username"); !strings.Contains(r.stdout, "(not set)") {
		t.Fatalf("config get username after unset: out=%q", firstLine(r.stdout))
	}
	if r := runCLIEnv(t, env, "config", "unset", "output_format"); r.code != 0 {
		t.Fatalf("config unset output_format: exit %d: %s", r.code, firstLine(r.stderr))
	}
}

// TestIntegration_SystemTimezoneList exercises the --list flag on system
// timezone, which returns a different (larger) document.
func TestIntegration_SystemTimezoneList(t *testing.T) {
	requireLive(t)
	r := runCLI(t, "system", "timezone", "--list", "--json")
	if r.code != 0 {
		t.Fatalf("system timezone --list: exit %d: %s", r.code, firstLine(r.stderr))
	}
	var data struct {
		Count     int      `json:"count"`
		Timezones []string `json:"timezones"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &data); err != nil {
		t.Fatalf("system timezone --list: invalid JSON: %v", err)
	}
	if data.Count == 0 || len(data.Timezones) == 0 {
		t.Fatalf("system timezone --list: empty list (count=%d)", data.Count)
	}
}

// TestIntegration_WOLCRUDL exercises the WOL add -> list -> wake -> update ->
// list -> remove lifecycle with reversible test data, leaving the device in its
// original state. wol has no get/update endpoints, so list stands in for get
// and re-add (with a new name) stands in for update.
func TestIntegration_WOLCRUDL(t *testing.T) {
	requireWrite(t)

	cleanup := func() {
		_ = runCLI(t, "wol", "remove", testWOLMAC, "--yes", "--json")
	}
	cleanup()
	t.Cleanup(cleanup)

	// 1. Create
	r := runCLI(t, "wol", "add", testWOLMAC, testWOLName, "--json")
	if r.code != 0 {
		t.Fatalf("wol add: exit %d: %s", r.code, firstLine(r.stderr))
	}

	// 2. Get (via list)
	if !wolListContains(t, testWOLMAC, testWOLName) {
		t.Fatalf("wol list after add: missing %s/%s", testWOLMAC, testWOLName)
	}

	// 3a. Benign action scoped to the test entry
	if r := runCLI(t, "wol", "wake", testWOLMAC, "--json"); r.code != 0 {
		if code, ok := structuredErrorCode(r.stderr); !ok || !allowedDeviceCodes[code] {
			t.Fatalf("wol wake: exit %d: %s", r.code, firstLine(r.stderr))
		}
	}

	// 3b. Update (re-add under the same MAC with a new name)
	if r := runCLI(t, "wol", "add", testWOLMAC, testWOLAlt, "--json"); r.code != 0 {
		t.Fatalf("wol add (update): exit %d: %s", r.code, firstLine(r.stderr))
	}
	// 4. Get again (via list) -> updated name present, old name gone
	if !wolListContains(t, testWOLMAC, testWOLAlt) {
		t.Fatalf("wol list after update: missing %s/%s", testWOLMAC, testWOLAlt)
	}

	// 5. List (already verified).

	// 6a. Remove without --yes must be refused and must not delete anything.
	noYes := runCLI(t, "wol", "remove", testWOLMAC, "--json")
	if noYes.code == 0 {
		t.Fatalf("wol remove without --yes: expected non-zero exit")
	}
	if code, _ := structuredErrorCode(noYes.stderr); code != "CONFIRMATION_REQUIRED" {
		t.Fatalf("wol remove without --yes: code = %q, want CONFIRMATION_REQUIRED", code)
	}
	if !wolListContains(t, testWOLMAC, testWOLAlt) {
		t.Fatalf("wol remove without --yes must not delete the entry")
	}

	// 6b. Delete (with confirmation)
	if r := runCLI(t, "wol", "remove", testWOLMAC, "--yes", "--json"); r.code != 0 {
		t.Fatalf("wol remove --yes: exit %d: %s", r.code, firstLine(r.stderr))
	}

	// 7. List -> gone
	if wolListContains(t, testWOLMAC, testWOLAlt) {
		t.Fatalf("wol list after remove: %s still present", testWOLMAC)
	}
}

func wolListContains(t *testing.T, mac, name string) bool {
	t.Helper()
	r := runCLI(t, "wol", "list", "--json")
	if r.code != 0 {
		t.Fatalf("wol list: exit %d: %s", r.code, firstLine(r.stderr))
	}
	var data any
	if err := json.Unmarshal([]byte(r.stdout), &data); err != nil {
		t.Fatalf("wol list: invalid JSON: %v", err)
	}
	strs := flattenStrings(data)
	macSeen, nameSeen := false, false
	for _, s := range strs {
		if strings.EqualFold(s, mac) {
			macSeen = true
		}
		if s == name {
			nameSeen = true
		}
	}
	return macSeen && nameSeen
}

func flattenStrings(v any) []string {
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case string:
			out = append(out, t)
		case map[string]any:
			for _, val := range t {
				walk(val)
			}
		case []any:
			for _, item := range t {
				walk(item)
			}
		}
	}
	walk(v)
	return out
}

// TestIntegration_ScreenRoundTrip performs a reversible custom-screen date
// format round-trip. Some firmware builds do not implement the endpoint; those
// are skipped rather than failed.
func TestIntegration_ScreenRoundTrip(t *testing.T) {
	requireWrite(t)

	known := []string{"locale", "mm_dd_yyyy", "dd_mm_yyyy", "yyyy_mm_dd"}

	status := runCLI(t, "screen", "status", "--json")
	if status.code != 0 {
		code, _ := structuredErrorCode(status.stderr)
		if code == "NOT_SUPPORTED" || code == "ERROR" {
			t.Skipf("custom screen not supported on this device (%s)", code)
		}
		t.Fatalf("screen status: exit %d: %s", status.code, firstLine(status.stderr))
	}
	var data any
	if err := json.Unmarshal([]byte(status.stdout), &data); err != nil {
		t.Fatalf("screen status: invalid JSON: %v", err)
	}
	current := findKnownValue(data, known)
	if current == "" {
		t.Skip("screen status: could not determine current date format")
	}
	target := known[0]
	for _, k := range known {
		if k != current {
			target = k
			break
		}
	}

	restore := func() {
		_ = runCLI(t, "screen", "set-date-format", current, "--json")
	}
	t.Cleanup(restore)

	if r := runCLI(t, "screen", "set-date-format", target, "--json"); r.code != 0 {
		t.Fatalf("screen set-date-format %s: exit %d: %s", target, r.code, firstLine(r.stderr))
	}
	if got := currentDateFormat(t, known); got != target {
		t.Fatalf("screen date format after set = %q, want %q", got, target)
	}

	restore()
	if got := currentDateFormat(t, known); got != current {
		t.Fatalf("screen date format after restore = %q, want %q", got, current)
	}
}

func currentDateFormat(t *testing.T, known []string) string {
	t.Helper()
	r := runCLI(t, "screen", "status", "--json")
	if r.code != 0 {
		t.Fatalf("screen status: exit %d: %s", r.code, firstLine(r.stderr))
	}
	var data any
	if err := json.Unmarshal([]byte(r.stdout), &data); err != nil {
		t.Fatalf("screen status: invalid JSON: %v", err)
	}
	return findKnownValue(data, known)
}

// findKnownValue returns the first leaf string in data that is one of known.
func findKnownValue(data any, known []string) string {
	set := map[string]bool{}
	for _, k := range known {
		set[k] = true
	}
	for _, s := range flattenStrings(data) {
		if set[s] {
			return s
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Destructive-command confirmation gate
// ---------------------------------------------------------------------------

// requireYesCommands are destructive commands that must refuse to run without
// --yes. All are safe to invoke without --yes: they validate arguments and then
// return CONFIRMATION_REQUIRED before touching the device.
//
// Explicitly EXCLUDED (never invoked, per ticket): upgrade reboot,
// upgrade reset, init run, and msd format.
var requireYesCommands = [][]string{
	{"wol", "remove", testWOLMAC},
	{"msd", "remove", "-nonexistent.img"},
	{"msd", "write-remote", "https://example.invalid/.img"},
	{"repeater", "disconnect"},
	{"repeater", "remove-saved", "-nonexistent"},
	{"netbird", "logout"},
	{"netbird", "stop"},
	{"zerotier", "stop"},
	{"tailscale", "stop"},
	{"tailscale", "logout"},
	{"twofa", "create"},
	{"twofa", "init"},
	{"twofa", "delete"},
	{"fingerbot", "upgrade"},
	{"upgrade", "start"},
	{"system", "set-config"},
	{"system", "set-network"},
	{"system", "set-hostname", "-noop"},
	{"system", "set-firewall"},
	{"system", "set-param", "--set", "x=y"},
	{"system", "ssl-cert", "--default"},
	{"system", "set-time", "1700000000"},
	{"system", "set-timezone", "UTC"},
	{"atx", "power"},
	{"atx", "reset"},
	{"atx", "click", "power"},
}

// requireYesWithFile are destructive commands that also take a local file
// argument. They are tested with a real temp file so argument parsing succeeds.
func requireYesWithFile() [][]string {
	// The file is created by the caller; the paths here are placeholders that
	// get the real path preppended at call time.
	return [][]string{
		{"msd", "write"},
		{"upgrade", "upload"},
		{"screen", "set-background"},
	}
}

func TestIntegration_DestructiveRequireYes(t *testing.T) {
	requireWrite(t)

	tmp := filepath.Join(t.TempDir(), ".bin")
	if err := os.WriteFile(tmp, []byte(" integration test payload"), 0o600); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	cases := make([][]string, 0, len(requireYesCommands)+3)
	for _, c := range requireYesCommands {
		cases = append(cases, c)
	}
	// File-taking commands must come AFTER the flag, mirroring the examples.
	cases = append(cases,
		[]string{"msd", "write", tmp},
		[]string{"upgrade", "upload", tmp},
		[]string{"screen", "set-background", tmp},
	)

	for _, args := range cases {
		args := args
		name := strings.ReplaceAll(strings.Join(args, "_"), "/", "_")
		t.Run(name, func(t *testing.T) {
			r := runCLI(t, appendArgs(args, "--json")...)
			if r.code == 0 {
				t.Fatalf("%v without --yes: expected non-zero exit, got 0", args)
			}
			code := assertJSONErrorEnvelope(t, strings.Join(args, " "), r)
			if code != "CONFIRMATION_REQUIRED" {
				t.Fatalf("%v without --yes: code = %q, want CONFIRMATION_REQUIRED", args, code)
			}
		})
	}
}

// structuredErrorCode extracts the code from a JSON error envelope on stderr,
// if present.
func structuredErrorCode(stderr string) (string, bool) {
	trimmed := strings.TrimSpace(stderr)
	// Tolerate non-JSON warning lines before the envelope.
	if i := strings.Index(trimmed, "{"); i > 0 {
		trimmed = trimmed[i:]
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(trimmed), &env); err != nil {
		return "", false
	}
	if env.Error.Code == "" {
		return "", false
	}
	return env.Error.Code, true
}

// ---------------------------------------------------------------------------
// Structural command-coverage verification
// ---------------------------------------------------------------------------

// writeLifecycleCommands are write commands covered by an idempotent lifecycle
// test (WOL, screen, config) rather than by the destructive gate.
var writeLifecycleCommands = []string{
	"wol add",
	"wol wake",
	"wol remove",
	"screen set-date-format",
	"screen set-background",
	"screen delete-background",
	"config list",
	"config get",
	"config set",
	"config unset",
	"msd remove",
	"msd write",
	"msd write-remote",
	"upgrade start",
	"upgrade upload",
	"screenshot",
}

// omittedCommands documents intentionally unexercised commands with a reason.
var omittedCommands = map[string]string{
	"ap enable":              "changes Wi-Fi AP state",
	"ap open-last":           "changes Wi-Fi AP state",
	"ap close-all":           "changes Wi-Fi AP state",
	"asr start":              "changes ASR daemon state",
	"asr stop":               "changes ASR daemon state",
	"auth login":             "session lifecycle; login occurs implicitly in every device command",
	"auth logout":            "terminates the device session",
	"completion":             "shell completion; covered by smoke tests",
	"docs":                   "static embedded README; covered by smoke tests",
	"fingerbot click":        "would physically press a button",
	"help":                   "cobra built-in help command",
	"hid combo":              "HID input injection would affect the target",
	"hid key":                "HID input injection would affect the target",
	"hid type":               "HID input injection would affect the target",
	"hid print":              "HID input injection would affect the target",
	"hid set-mouse-output":   "changes the HID mouse output mode",
	"hid mouse click":        "HID input injection would affect the target",
	"hid mouse down":         "HID input injection would affect the target",
	"hid mouse move":         "HID input injection would affect the target",
	"hid mouse up":           "HID input injection would affect the target",
	"hid mouse wheel":        "HID input injection would affect the target",
	"init run":               "first-run initialization excluded by ticket",
	"init change-password":   "changes the admin password",
	"man":                    "static roff output; covered by smoke tests",
	"modem input-pin":        "submits the SIM PIN",
	"msd connect":            "changes virtual media mount state",
	"msd disconnect":         "changes virtual media mount state",
	"msd format":             "destructive format excluded by ticket",
	"msd set-connected":      "changes virtual media mount state",
	"msd set-params":         "changes virtual drive parameters",
	"netbird login":          "starts a network login flow",
	"netbird start":          "changes network daemon state",
	"repeater connect":       "joins a Wi-Fi network",
	"repeater enable":        "changes repeater state",
	"skill add":              "installs files locally; covered by smoke tests",
	"skill path":             "prints a local path; covered by smoke tests",
	"skill print":            "static output; covered by smoke tests",
	"streamer set-params":    "changes video encoder parameters",
	"streamer snapshot":      "binary snapshot; shares the screenshot code path",
	"screen set-time-format": "screen writes not exercised (date-format round-trip only)",
	"screen set-mode":        "screen writes not exercised (date-format round-trip only)",
	"switch set-active":      "changes USB switch routing",
	"system ssl-cert":        "reading path exercised separately; writing is gated",
	"tailscale login":        "may initiate a login flow",
	"tailscale login-url":    "may initiate a login flow",
	"tailscale start":        "changes daemon state",
	"upgrade cancel":         "requires an in-progress download",
	"upgrade download":       "starts a firmware download",
	"upgrade reboot":         "reboot excluded by ticket",
	"upgrade reset":          "factory reset excluded by ticket",
	"upgrade log":            "binary zip download",
	"zerotier set-token":     "changes the network id",
	"zerotier start":         "changes daemon state",
}

// TestIntegration_Coverage walks the runtime Cobra tree and fails if any leaf
// command is neither exercised by these tests nor explicitly documented as
// omitted. It runs without a device.
func TestIntegration_Coverage(t *testing.T) {
	covered := map[string]string{}
	for _, p := range readCommands {
		covered[p] = "read cross-product"
	}
	for _, p := range writeLifecycleCommands {
		covered[p] = "write lifecycle / dedicated test"
	}
	for _, args := range requireYesCommands {
		covered[strings.Join(args, " ")] = "destructive --yes gate"
	}
	for _, args := range requireYesWithFile() {
		covered[strings.Join(args, " ")+" <file>"] = "destructive --yes gate"
	}
	// cua commands are exercised live by cua_integration_test.go ().
	for _, p := range cuaCoveredCommands {
		covered[p] = "cua integration"
	}
	for p, reason := range omittedCommands {
		covered[p] = "omitted: " + reason
	}

	leaves := collectLeafCommands(cmd.GetRootCmd())
	var missing []string
	for _, p := range leaves {
		if _, ok := lookupCoverage(covered, p); !ok {
			missing = append(missing, p)
		}
	}
	sort.Strings(missing)

	t.Logf("command coverage: %d leaf commands discovered, %d accounted for", len(leaves), len(leaves)-len(missing))
	for _, p := range leaves {
		if reason, ok := lookupCoverage(covered, p); ok {
			t.Logf("  covered  %-32s %s", p, reason)
		}
	}
	if len(missing) > 0 {
		t.Errorf("uncovered commands (%d):\n  %s", len(missing), strings.Join(missing, "\n  "))
	}
}

// lookupCoverage finds the coverage entry for a leaf command path. Test lists
// carry argument placeholders (e.g. "modem at AT"), so a covered key that
// starts with the leaf path followed by a space also matches.
func lookupCoverage(covered map[string]string, path string) (string, bool) {
	if reason, ok := covered[path]; ok {
		return reason, true
	}
	prefix := path + " "
	for k, reason := range covered {
		if strings.HasPrefix(k, prefix) {
			return reason, true
		}
	}
	return "", false
}

// collectLeafCommands returns the space-joined path of every non-hidden leaf
// command in the tree.
func collectLeafCommands(root *cobra.Command) []string {
	var out []string
	var walk func(c *cobra.Command, path []string)
	walk = func(c *cobra.Command, path []string) {
		children := c.Commands()
		visible := make([]*cobra.Command, 0, len(children))
		for _, ch := range children {
			if ch.Hidden {
				continue
			}
			visible = append(visible, ch)
		}
		if len(visible) == 0 {
			out = append(out, strings.Join(path, " "))
			return
		}
		for _, ch := range visible {
			walk(ch, append(append([]string(nil), path...), ch.Name()))
		}
	}
	for _, top := range root.Commands() {
		if top.Hidden {
			continue
		}
		walk(top, []string{top.Name()})
	}
	return out
}
