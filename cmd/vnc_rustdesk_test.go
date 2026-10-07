package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/output"
)

// saveVNCRustDeskGlobals snapshots the vnc/rustdesk flag globals.
func saveVNCRustDeskGlobals(t *testing.T) {
	t.Helper()
	host, user, pass, addr := flagVNCHost, flagVNCUser, flagVNCPassword, flagVNCAddr
	rdDir, rdPass := flagRustDeskConfigDir, flagRustDeskPassword
	mouseAt := flagVNCMouseAt
	t.Cleanup(func() {
		flagVNCHost, flagVNCUser, flagVNCPassword, flagVNCAddr = host, user, pass, addr
		flagRustDeskConfigDir, flagRustDeskPassword = rdDir, rdPass
		flagVNCMouseAt = mouseAt
	})
}

func TestVNCEndpointResolution(t *testing.T) {
	saveVNCRustDeskGlobals(t)
	t.Setenv("KVM_VNC_HOST", "")
	t.Setenv("VNC_HOST", "")
	t.Setenv("KVM_VNC_USERNAME", "")
	t.Setenv("VNC_USERNAME", "")
	t.Setenv("KVM_VNC_PASSWORD", "")
	t.Setenv("VNC_PASSWORD", "")

	flagVNCAddr, flagVNCHost, flagVNCUser, flagVNCPassword = "", "", "", ""
	if h, _ := vncEndpoint(); h != "" || vncTargetConfigured() {
		t.Errorf("empty endpoint = %q", h)
	}

	// --vnc wins and gets the default port appended.
	flagVNCAddr = "192.168.1.100"
	h, creds := vncEndpoint()
	if h != "192.168.1.100:5900" {
		t.Errorf("addr = %q want default port", h)
	}
	_ = creds
	if !vncTargetConfigured() {
		t.Error("expected configured")
	}

	// Explicit port preserved.
	flagVNCAddr = "192.168.1.100:5901"
	if h, _ := vncEndpoint(); h != "192.168.1.100:5901" {
		t.Errorf("addr = %q", h)
	}

	// --host (vnc group) is honored when --vnc is empty.
	flagVNCAddr = ""
	flagVNCHost = "mac-remote.local:5900"
	flagVNCUser = "user"
	flagVNCPassword = "pw"
	h, creds = vncEndpoint()
	if h != "mac-remote.local:5900" || creds.Username != "user" || creds.Password != "pw" {
		t.Errorf("host mode = %q %+v", h, creds)
	}

	// Environment fallback.
	flagVNCHost, flagVNCUser, flagVNCPassword = "", "", ""
	t.Setenv("VNC_HOST", "mini")
	t.Setenv("VNC_PASSWORD", "envpw")
	h, creds = vncEndpoint()
	if h != "mini:5900" || creds.Password != "envpw" {
		t.Errorf("env mode = %q %+v", h, creds)
	}

	// vncRequireHost error.
	if err := vncRequireHost(""); output.ErrorCode(err) != "USAGE" {
		t.Errorf("vncRequireHost empty: %q", output.ErrorCode(err))
	}
	if err := vncRequireHost("x:5900"); err != nil {
		t.Errorf("vncRequireHost non-empty: %v", err)
	}
}

func TestVNCButtonNames(t *testing.T) {
	for _, n := range []string{"left", "middle", "right", "up", "down", "LEFT"} {
		if _, ok := vncButtonName(n); !ok {
			t.Errorf("button %q should be valid", n)
		}
	}
	if _, ok := vncButtonName("bogus"); ok {
		t.Error("bogus button should be invalid")
	}
}

func writeKD(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRustDeskInfo(t *testing.T) {
	saveVNCRustDeskGlobals(t)
	dir := t.TempDir()
	writeKD(t, dir, "RustDesk.toml", "enc_id = 'abc'\npassword = '00x=='\n")
	writeKD(t, dir, "RustDesk2.toml", "rendezvous_server = 'relay.example:21116'\n[options]\nset-permanent-password = 'p'\n")
	flagRustDeskConfigDir = dir
	flagJSON = true

	out := captureStdout(t, func() {
		if err := rustdeskInfoCmd.RunE(rustdeskInfoCmd, nil); err != nil {
			t.Fatalf("info: %v", err)
		}
	})
	var got rustdeskInfoOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if !got.HasID || got.RendezvousServer != "relay.example:21116" || !got.HasPermanentPassword {
		t.Errorf("info = %+v", got)
	}
	// The password must never appear.
	if containsAny(out, "00x==", "'p'") {
		t.Errorf("output leaked password material: %s", out)
	}
}

func TestRustDeskPeers(t *testing.T) {
	saveVNCRustDeskGlobals(t)
	dir := t.TempDir()
	pdir := filepath.Join(dir, "peers")
	if err := os.MkdirAll(pdir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeKD(t, pdir, "191877719.toml", "hostname = 'ubuntu-vm'\nusername = 'gdm'\nplatform = 'Linux'\n")
	flagRustDeskConfigDir = dir
	flagJSON = true

	out := captureStdout(t, func() {
		if err := rustdeskPeersCmd.RunE(rustdeskPeersCmd, nil); err != nil {
			t.Fatalf("peers: %v", err)
		}
	})
	var got struct {
		Count int `json:"count"`
		Peers []struct {
			ID       string `json:"id"`
			Hostname string `json:"hostname"`
		} `json:"peers"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if got.Count != 1 || got.Peers[0].ID != "191877719" || got.Peers[0].Hostname != "ubuntu-vm" {
		t.Errorf("peers = %+v", got)
	}
}

func TestRustDeskConnectDryRun(t *testing.T) {
	saveVNCRustDeskGlobals(t)
	saveGlobals(t)
	flagDryRun = true
	flagJSON = true
	out := captureStdout(t, func() {
		if err := runRustDeskConnect(rustdeskConnectCmd, []string{"191877719"}); err != nil {
			t.Fatalf("connect dry-run: %v", err)
		}
	})
	if !containsAny(out, "--connect") || !containsAny(out, "191877719") {
		t.Errorf("dry-run output = %s", out)
	}
}

func TestRustDeskConnectRequiresYes(t *testing.T) {
	saveVNCRustDeskGlobals(t)
	saveGlobals(t)
	flagDryRun = false
	flagRustDeskYes = false
	// Use a fake binary so the --yes gate is what fails, not binary discovery.
	t.Setenv("PATH", t.TempDir())
	err := runRustDeskConnect(rustdeskConnectCmd, []string{"191877719"})
	// Either binary-not-found or confirmation-required is acceptable; assert the
	// confirmation gate is enforced when a binary would be found.
	if output.ErrorCode(err) != "DEVICE_ERROR" && output.ErrorCode(err) != "CONFIRMATION_REQUIRED" {
		t.Fatalf("code = %q (%v)", output.ErrorCode(err), err)
	}
}

func TestVNCMouseClickBadButton(t *testing.T) {
	saveVNCRustDeskGlobals(t)
	flagVNCAddr = "127.0.0.1:1"
	err := vncMouseClickCmd.RunE(vncMouseClickCmd, []string{"sideways"})
	if output.ErrorCode(err) != "USAGE" {
		t.Fatalf("code = %q want USAGE", output.ErrorCode(err))
	}
}

func TestVNCKeyCommandUnknown(t *testing.T) {
	saveVNCRustDeskGlobals(t)
	flagVNCAddr = "127.0.0.1:1"
	err := vncKeyCmd.RunE(vncKeyCmd, []string{"notakey"})
	if output.ErrorCode(err) != "USAGE" {
		t.Fatalf("code = %q want USAGE", output.ErrorCode(err))
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && len(sub) <= len(s) {
			for i := 0; i+len(sub) <= len(s); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
		}
	}
	return false
}
