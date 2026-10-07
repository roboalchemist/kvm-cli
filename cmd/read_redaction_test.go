package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/roboalchemist/kvm-cli/pkg/output"
)

// Distinctive markers so a leaked secret is unambiguous in the rendered output.
const (
	testSSLKey     = "SECRET_SSLKEY"
	testSetupKey   = "SECRET_SETUPKEY"
	testPassword   = "SECRET_PASSWORD"
	testAuthkey    = "SECRET_AUTHKEY"
	testPrivateKey = "SECRET_PRIVATEKEY"
	testNBAuthkey  = "SECRET_NB_AUTHKEY"
	testTSAuthKey  = "SECRET_TS_AUTHKEY"
	testInfoToken  = "SECRET_INFO_TOKEN"

	// Non-secret markers that must survive redaction.
	testKeyboard  = "us-qwerty"
	testKeymaps   = "de-neo"
	testProductID = "PID-1234"
	testCertPEM   = "CERTPEM"
	testNBIP      = "10.0.1.7"
)

// newFixtureMock is a read-only device stub whose GET payloads deliberately echo
// secret-named fields, plus benign fields that must not be masked.
func newFixtureMock(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		result := map[string]any{}
		switch r.URL.Path {
		case "/api/auth/login":
			result = map[string]any{"token": "test-token"}
		case "/api/system/get_param":
			result = map[string]any{
				"success":            true,
				"otg_product":        "GL-RM1PE",
				"keyboard":           testKeyboard,
				"keymaps":            testKeymaps,
				"default_product_id": testProductID,
				"ssl_key":            testSSLKey,
				"authkey":            testAuthkey,
				"password":           testPassword,
			}
		case "/api/system/get_config":
			result = map[string]any{
				"success": true,
				"config": map[string]any{
					"keyboard_enabled":   true,
					"keymaps":            testKeymaps,
					"setup_key":          testSetupKey,
					"ssl_key":            testSSLKey,
					"default_product_id": testProductID,
				},
			}
		case "/api/system/ssl_cert":
			result = map[string]any{"success": true, "ssl_cert": testCertPEM, "ssl_key": testSSLKey}
		case "/api/netbird/get_info":
			result = map[string]any{
				"running":     true,
				"connected":   true,
				"ip":          testNBIP,
				"authkey":     testNBAuthkey,
				"private_key": testPrivateKey,
			}
		case "/api/tailscale/config":
			result = map[string]any{"enable": true, "accept_dns": true, "auth_key": testTSAuthKey}
		case "/api/init/is_inited":
			result = map[string]any{
				"is_inited": true, "screen": "clock", "country": "US",
				"setup_key": testSetupKey, "authkey": testAuthkey,
			}
		case "/api/info":
			result = map[string]any{
				"auth":   map[string]any{"enabled": true, "token": testInfoToken},
				"system": map[string]any{"platform": map[string]any{"model": "RM1PE"}},
				"extras": map[string]any{},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestReadPathRedaction is the N1 acceptance: every read command that
// renders a device field map masks secret-named values in both table and JSON
// modes, while non-secret fields remain visible.
func TestReadPathRedaction(t *testing.T) {
	srv := newFixtureMock(t)
	setRedactEnv(t, srv.URL)

	saveGlobals(t)
	saveAnnotationGlobals(t)
	// Neutralise any cross-test mutation of the tailscale/cert command state.
	tsConfigSet, tsConfigExitNode, tsConfigAdvertiseRoutes = nil, "", ""
	syswCertDefault = false
	t.Cleanup(func() {
		tsConfigSet, tsConfigExitNode, tsConfigAdvertiseRoutes = nil, "", ""
		syswCertDefault = false
	})

	cases := []struct {
		name    string
		run     func() error
		secrets []string
		keeps   []string
		// curatedTable is true for commands whose table shows only a curated
		// subset of fields (so a secret not on that list never appears in the
		// table, and no mask is expected there).
		curatedTable bool
	}{
		{
			name:    "system ssl-cert",
			run:     func() error { return runSystemSSLCert(systemSSLCertCmd, nil) },
			secrets: []string{testSSLKey},
			keeps:   []string{testCertPEM},
		},
		{
			name:    "system param",
			run:     func() error { return runSystemParam(systemParamCmd, nil) },
			secrets: []string{testSSLKey, testAuthkey, testPassword},
			keeps:   []string{testKeyboard, testKeymaps, testProductID},
		},
		{
			name:    "system config",
			run:     func() error { return runSystemConfig(systemConfigCmd, nil) },
			secrets: []string{testSetupKey, testSSLKey},
			keeps:   []string{testKeymaps, testProductID, "keyboard_enabled"},
		},
		{
			name:    "netbird info",
			run:     func() error { return runNetbirdInfo(netbirdInfoCmd, nil) },
			secrets: []string{testNBAuthkey, testPrivateKey},
			keeps:   []string{testNBIP},
		},
		{
			name:    "tailscale config",
			run:     func() error { return runTailscaleConfig(tailscaleConfigCmd, nil) },
			secrets: []string{testTSAuthKey},
			keeps:   []string{"accept_dns"},
		},
		{
			name:    "init status",
			run:     func() error { return runInitStatus(initStatusCmd, nil) },
			secrets: []string{testSetupKey, testAuthkey},
			keeps:   []string{"is_inited"},
		},
		{
			name:         "info",
			run:          func() error { return runInfo(infoCmd, nil) },
			secrets:      []string{testInfoToken},
			keeps:        []string{"RM1PE"},
			curatedTable: true,
		},
	}

	modes := []struct {
		name     string
		setup    func()
		validate func(t *testing.T, out string)
	}{
		{
			name:  "table",
			setup: func() { flagJSON, flagPlaintext, flagFormat = false, false, "table" },
		},
		{
			name:  "json",
			setup: func() { flagJSON, flagPlaintext, flagFormat = true, false, "table" },
			validate: func(t *testing.T, out string) {
				var obj map[string]any
				if err := json.Unmarshal([]byte(out), &obj); err != nil {
					t.Fatalf("invalid JSON: %v\n%s", err, out)
				}
			},
		},
	}

	for _, mode := range modes {
		for _, tc := range cases {
			t.Run(mode.name+"/"+tc.name, func(t *testing.T) {
				mode.setup()
				out := captureStdout(t, func() {
					if err := tc.run(); err != nil {
						t.Fatalf("%s: %v", tc.name, err)
					}
				})
				combined := out
				for _, secret := range tc.secrets {
					if strings.Contains(combined, secret) {
						t.Fatalf("%s (%s) leaked %q:\n%s", tc.name, mode.name, secret, out)
					}
				}
				if !(mode.name == "table" && tc.curatedTable) && !strings.Contains(out, "***") {
					t.Fatalf("%s (%s) did not render a mask:\n%s", tc.name, mode.name, out)
				}
				for _, keep := range tc.keeps {
					if !strings.Contains(out, keep) {
						t.Fatalf("%s (%s) dropped non-secret %q:\n%s", tc.name, mode.name, keep, out)
					}
				}
				if mode.validate != nil {
					mode.validate(t, out)
				}
			})
		}
	}
}

// TestShotFetchUsesTimeout is the F13 regression: shotFetchOnce honours the
// caller-supplied timeout instead of a hardcoded 15s.
func TestShotFetchUsesTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte{0xFF, 0xD8, 0xFF})
	}))
	defer srv.Close()

	if _, _, _, err := shotFetchOnce(srv.URL, "tok", 20*time.Millisecond); err == nil {
		t.Fatal("expected a timeout error with a 20ms deadline")
	}
	data, _, _, err := shotFetchOnce(srv.URL, "tok", 2*time.Second)
	if err != nil {
		t.Fatalf("shotFetchOnce with a generous timeout: %v", err)
	}
	if !looksLikeJPEG(data) {
		t.Fatalf("shotFetchOnce returned %v, want JPEG bytes", data)
	}
}

// TestScreenshotStdoutRejectsStructuredFormat is the F8 regression: '-o -'
// cannot be combined with a structured output mode.
func TestScreenshotStdoutRejectsStructuredFormat(t *testing.T) {
	saveGlobals(t)
	flagJSON, flagPlaintext, flagFormat = true, false, "table"

	err := shotCapture(screenshotCmd, shotOptions{Output: "-", Frames: 1})
	if output.ErrorCode(err) != "USAGE" {
		t.Fatalf("shotCapture -o - --json: code = %q, want USAGE (err: %v)", output.ErrorCode(err), err)
	}
}

// TestStreamerSnapshotKeepAlive is the F9 regression: streamer snapshot
// exposes the same --keep-alive flag as screenshot.
func TestStreamerSnapshotKeepAlive(t *testing.T) {
	if streamerSnapshotCmd.Flags().Lookup("keep-alive") == nil {
		t.Fatal("streamer snapshot is missing the --keep-alive flag")
	}
}

// TestVersionHelpHasNoYamlFlag is the F7 regression: version help must not
// advertise a non-existent --yaml flag.
func TestVersionHelpHasNoYamlFlag(t *testing.T) {
	if strings.Contains(versionCmd.Long, "--yaml") {
		t.Fatalf("version help still references --yaml:\n%s", versionCmd.Long)
	}
}

// TestHIDTypePrintHelpDistinct is the F10 regression: the help for
// 'hid type' and 'hid print' cross-references each other so their difference is
// clear.
func TestHIDTypePrintHelpDistinct(t *testing.T) {
	if !strings.Contains(hidTypeCmd.Long, "hid print") {
		t.Errorf("hid type help does not reference hid print:\n%s", hidTypeCmd.Long)
	}
	if !strings.Contains(hidPrintCmd.Long, "hid type") {
		t.Errorf("hid print help does not reference hid type:\n%s", hidPrintCmd.Long)
	}
}
