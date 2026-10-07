package cmd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// redactMock is a minimal device stub: it authenticates any login and records
// the form body it receives so tests can prove secrets travel in the POST body
// rather than the query string.
type redactMock struct {
	mu    sync.Mutex
	forms map[string]string // path -> recorded "pin"/"token" form value
}

func newRedactMock(t *testing.T) (*httptest.Server, *redactMock) {
	t.Helper()
	m := &redactMock{forms: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/auth/login" {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"token":"test-token"}}`))
			return
		}
		if err := r.ParseForm(); err == nil {
			m.mu.Lock()
			switch r.URL.Path {
			case "/api/modem/input_pin_code":
				m.forms[r.URL.Path] = r.PostFormValue("pin")
			case "/api/zerotier/set_token":
				m.forms[r.URL.Path] = r.PostFormValue("token")
			}
			m.mu.Unlock()
		}
		result := map[string]any{}
		if r.URL.Path == "/api/ap/enable" {
			// The device may echo the passphrase back; the CLI must mask it.
			result = map[string]any{"success": true, "key": "SUPERSECRET", "enable": true}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
	}))
	t.Cleanup(srv.Close)
	return srv, m
}

func (m *redactMock) formValue(path string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.forms[path]
}

// setRedactEnv points the CLI at the mock and isolates all credential/config
// sources (the mock's env values take precedence over gopass).
func setRedactEnv(t *testing.T, url string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KVM_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("GLKVM_CONFIG", "")
	t.Setenv("KVM_URL", url)
	t.Setenv("KVM_USERNAME", "admin")
	t.Setenv("KVM_PASSWORD", "pw")
}

// captureOutput redirects stdout and stderr while fn runs and returns what each
// received. It is the combined-stream counterpart to captureStdout.
func captureOutput(t *testing.T, fn func()) (string, string) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatalf("stderr pipe: %v", err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	defer func() {
		os.Stdout, os.Stderr = oldOut, oldErr
	}()
	fn()
	_ = outW.Close()
	_ = errW.Close()
	outData, _ := io.ReadAll(outR)
	errData, _ := io.ReadAll(errR)
	return string(outData), string(errData)
}

// TestAPEnableMasksSecretParams is acceptance: the Wi-Fi passphrase
// passed via --set key= must be masked in stdout (including JSON) and absent
// from --debug stderr.
func TestAPEnableMasksSecretParams(t *testing.T) {
	srv, _ := newRedactMock(t)
	setRedactEnv(t, srv.URL)

	saveGlobals(t)
	flagFormat, flagJSON, flagPlaintext = "json", false, false
	flagDebug = true

	apSet = []string{"ssid=my-hotspot", "key=SUPERSECRET"}
	apEnableFlag = true
	t.Cleanup(func() {
		apSet = nil
		apEnableFlag = true
	})

	var runErr error
	out, errOut := captureOutput(t, func() {
		runErr = runAPEnable(apEnableCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("runAPEnable: %v", runErr)
	}
	if strings.Contains(out, "SUPERSECRET") {
		t.Fatalf("ap enable leaked the passphrase to stdout:\n%s", out)
	}
	if strings.Contains(errOut, "SUPERSECRET") {
		t.Fatalf("ap enable leaked the passphrase to --debug stderr:\n%s", errOut)
	}
	if !strings.Contains(out, "key=***") {
		t.Fatalf("ap enable did not render a masked key in stdout:\n%s", out)
	}
	if !strings.Contains(errOut, "key=***") {
		t.Fatalf("ap enable did not mask the key in --debug stderr:\n%s", errOut)
	}
	if !strings.Contains(out, "ssid=my-hotspot") {
		t.Fatalf("ap enable dropped the benign ssid param:\n%s", out)
	}
}

// TestModemInputPINDoesNotLeak asserts the SIM PIN is sent in the POST body
// and never appears on stdout or --debug stderr.
func TestModemInputPINDoesNotLeak(t *testing.T) {
	srv, mock := newRedactMock(t)
	setRedactEnv(t, srv.URL)

	saveGlobals(t)
	flagFormat, flagJSON, flagPlaintext = "json", false, false
	flagDebug = true

	var runErr error
	out, errOut := captureOutput(t, func() {
		runErr = runModemInputPIN(modemInputPINCmd, []string{"4321"})
	})
	if runErr != nil {
		t.Fatalf("runModemInputPIN: %v", runErr)
	}
	if strings.Contains(out, "4321") || strings.Contains(errOut, "4321") {
		t.Fatalf("modem input-pin leaked the PIN (stdout=%q stderr=%q)", out, errOut)
	}
	if got := mock.formValue("/api/modem/input_pin_code"); got != "4321" {
		t.Fatalf("PIN was not sent in the POST body: got %q", got)
	}
}

// TestZerotierSetTokenDoesNotLeak asserts the ZeroTier token is sent in the
// POST body, never echoed, and absent from stdout and --debug stderr.
func TestZerotierSetTokenDoesNotLeak(t *testing.T) {
	srv, mock := newRedactMock(t)
	setRedactEnv(t, srv.URL)

	saveGlobals(t)
	flagFormat, flagJSON, flagPlaintext = "json", false, false
	flagDebug = true

	var runErr error
	out, errOut := captureOutput(t, func() {
		runErr = runZerotierSetToken(zerotierSetTokenCmd, []string{"SUPERSECRET"})
	})
	if runErr != nil {
		t.Fatalf("runZerotierSetToken: %v", runErr)
	}
	if strings.Contains(out, "SUPERSECRET") || strings.Contains(errOut, "SUPERSECRET") {
		t.Fatalf("zerotier set-token leaked the token (stdout=%q stderr=%q)", out, errOut)
	}
	if got := mock.formValue("/api/zerotier/set_token"); got != "SUPERSECRET" {
		t.Fatalf("token was not sent in the POST body: got %q", got)
	}
}

// TestModemSimSettingMasksSetSecret asserts --set values that name a secret are
// masked in the rendered action.
func TestModemSimSettingMasksSetSecret(t *testing.T) {
	srv, _ := newRedactMock(t)
	setRedactEnv(t, srv.URL)

	saveGlobals(t)
	flagFormat, flagJSON, flagPlaintext = "json", false, false
	flagDebug = true

	mdSimSet = []string{"apn=internet", "password=SUPERSECRET"}
	t.Cleanup(func() { mdSimSet = nil })

	var runErr error
	out, errOut := captureOutput(t, func() {
		runErr = runModemSimSetting(modemSimSettingCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("runModemSimSetting: %v", runErr)
	}
	if strings.Contains(out, "SUPERSECRET") || strings.Contains(errOut, "SUPERSECRET") {
		t.Fatalf("modem sim-setting leaked the secret (stdout=%q stderr=%q)", out, errOut)
	}
	if !strings.Contains(out, "password=***") {
		t.Fatalf("modem sim-setting did not mask the password:\n%s", out)
	}
}
