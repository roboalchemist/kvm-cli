package grounding

import (
	"context"
	"fmt"
	"net"

	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestFindUv(t *testing.T) {
	// Env override wins even when the path does not exist.
	t.Setenv("KVM_UV", "/nonexistent/uv")
	if got := FindUv(); got != "/nonexistent/uv" {
		t.Errorf("FindUv = %q, want the KVM_UV override", got)
	}
	// Without override: PATH or ~/.local/bin/uv; empty is acceptable.
	t.Setenv("KVM_UV", "")
	got := FindUv()
	if got == "" {
		t.Skip("uv not installed on this machine")
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("FindUv = %q which does not exist", got)
	}
}

func TestCaptionerScriptPathOverride(t *testing.T) {
	t.Setenv("KVM_CAPTIONER_SCRIPT", "/custom/caption_server.py")
	p, err := CaptionerScriptPath()
	if err != nil {
		t.Fatalf("CaptionerScriptPath: %v", err)
	}
	if p != "/custom/caption_server.py" {
		t.Errorf("path = %q", p)
	}
}

func TestCaptionerScriptPathWritesEmbedded(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir) // UserCacheDir -> $HOME/Library/Caches on darwin
	t.Setenv("KVM_CAPTIONER_SCRIPT", "")

	SetCaptionerScript("#!/usr/bin/env python3\n# test script v1\n")
	p, err := CaptionerScriptPath()
	if err != nil {
		t.Fatalf("CaptionerScriptPath: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	if string(data) != "#!/usr/bin/env python3\n# test script v1\n" {
		t.Errorf("script content = %q", data)
	}
	// A newer embedded version overwrites the cache copy.
	SetCaptionerScript("# test script v2\n")
	if _, err := CaptionerScriptPath(); err != nil {
		t.Fatalf("second call: %v", err)
	}
	data, _ = os.ReadFile(p)
	if string(data) != "# test script v2\n" {
		t.Errorf("script not updated: %q", data)
	}
}

func TestEnsureCaptionerReusesRunning(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write([]byte(`{"ready":true,"device":"cpu","model":"microsoft/Florence-2-base"}`))
	}))
	defer srv.Close()

	c, spawned, err := EnsureCaptioner(context.Background(), srv.URL, 1, "", 2*time.Second)
	if err != nil {
		t.Fatalf("EnsureCaptioner: %v", err)
	}
	if spawned {
		t.Error("should reuse a healthy sidecar, not spawn")
	}
	if c == nil {
		t.Error("captioner should be non-nil")
	}
	if hits < 1 {
		t.Error("health endpoint not consulted")
	}
}

func TestEnsureCaptionerSpawnFailure(t *testing.T) {
	// No server at this URL and a broken uv -> spawn fails fast.
	t.Setenv("KVM_UV", "/nonexistent/uv")
	_, spawned, err := EnsureCaptioner(context.Background(),
		"http://127.0.0.1:1", 1, "", 300*time.Millisecond)
	if err == nil {
		t.Fatal("expected spawn failure with broken uv")
	}
	if !spawned {
		t.Log("spawned=false; uv missing detected before spawn")
	}
}

func TestWaitReadyTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ready":false,"error":null}`))
	}))
	defer srv.Close()
	err := waitReady(context.Background(), srv.URL, 300*time.Millisecond)
	if err == nil || !contains(err.Error(), "not ready") {
		t.Fatalf("err = %v, want readiness timeout", err)
	}
	// Sidecar load failure surfaces the sidecar's own error.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ready":false,"error":"CUDA out of memory"}`))
	}))
	defer srv2.Close()
	err = waitReady(context.Background(), srv2.URL, 300*time.Millisecond)
	if err == nil || !contains(err.Error(), "CUDA out of memory") {
		t.Fatalf("err = %v, want the sidecar load error", err)
	}
}

func TestPidRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if pid := ReadCaptionerPid(); pid != 0 {
		t.Errorf("expected no pid initially, got %d", pid)
	}
	if err := writePid(4242); err != nil {
		t.Fatalf("writePid: %v", err)
	}
	if pid := ReadCaptionerPid(); pid != 4242 {
		t.Errorf("pid = %d, want 4242", pid)
	}
}

func TestPortFree(t *testing.T) {
	// Grab an ephemeral port, release it, then expect it to be free.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	if !PortFree(port) {
		t.Errorf("port %d should be free after close", port)
	}
	// A listening port reports busy.
	ln2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("re-listen: %v", err)
	}
	defer ln2.Close()
	if PortFree(port) {
		t.Errorf("port %d should be busy while listening", port)
	}
}

// TestFindUvBranches exercises the PATH-miss and home-fallback branches.
func TestFindUvBranches(t *testing.T) {
	// PATH with no uv and no ~/.local/bin/uv -> empty.
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := FindUv(); got != "" {
		t.Errorf("FindUv without uv = %q, want empty", got)
	}
	// ~/.local/bin/uv present -> found via the home fallback.
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(bin, "uv")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := FindUv(); got != fake {
		t.Errorf("FindUv = %q, want %q", got, fake)
	}
}

func TestCaptionerBaseURL(t *testing.T) {
	if got := CaptionerBaseURL(8618); got != "http://127.0.0.1:8618" {
		t.Errorf("CaptionerBaseURL = %q", got)
	}
}

func TestCaptionerScriptPathWriteError(t *testing.T) {
	// Make the cache path a regular file so MkdirAll fails. The cache root is
	// platform-specific: ~/Library/Caches on darwin, ~/.cache elsewhere.
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("KVM_CAPTIONER_SCRIPT", "")
	var cacheRoot string
	switch runtime.GOOS {
	case "darwin":
		cacheRoot = filepath.Join(dir, "Library", "Caches")
	default:
		cacheRoot = filepath.Join(dir, ".cache")
	}
	if err := os.MkdirAll(cacheRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheRoot, "kvm-cli"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	SetCaptionerScript("#!/usr/bin/env python3\n")
	if _, err := CaptionerScriptPath(); err == nil {
		t.Error("expected MkdirAll failure when the cache path is a file")
	}
}

// TestEnsureCaptionerSpawnSuccess covers the full spawn path with a fake uv
// binary that starts a stdlib-only HTTP server answering /health.
func TestEnsureCaptionerSpawnSuccess(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("KVM_CAPTIONER_SCRIPT", "")
	SetCaptionerScript("# test\n")

	// Fake uv: ignores args, serves /health with a ready payload for ~30s.
	bin := filepath.Join(dir, "fakebin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeUV := filepath.Join(bin, "uv")
	script := `#!/bin/sh
python3 - <<'PYEOF' &
import http.server, json, os, threading, time
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps({"ready": True, "device": "cpu", "model": "fake"}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a): pass
srv = http.server.HTTPServer(("127.0.0.1", int(os.environ["KVM_TEST_PORT"])), H)
t = threading.Timer(30.0, srv.shutdown)
t.daemon = True
t.start()
srv.serve_forever()
PYEOF
`
	if err := os.WriteFile(fakeUV, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KVM_UV", fakeUV)

	// Pick a free port for the fake sidecar.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	t.Setenv("KVM_TEST_PORT", fmt.Sprint(port))

	c, spawned, err := EnsureCaptioner(context.Background(), CaptionerBaseURL(port), port, "", 5*time.Second)
	if err != nil {
		t.Fatalf("EnsureCaptioner: %v", err)
	}
	if !spawned {
		t.Error("expected spawned=true")
	}
	if c == nil {
		t.Fatal("captioner nil")
	}
	// The PID of the spawned fake uv must be recorded.
	if pid := ReadCaptionerPid(); pid == 0 {
		t.Error("no PID recorded for spawned sidecar")
	} else {
		// Clean up the fake process.
		if proc, err := os.FindProcess(pid); err == nil {
			_ = proc.Kill()
		}
	}
}

// TestFindUvNoHome covers the UserHomeDir error branch.
func TestFindUvNoHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("PATH", "/nonexistent")
	if got := FindUv(); got != "" {
		t.Errorf("FindUv without HOME = %q, want empty", got)
	}
}
