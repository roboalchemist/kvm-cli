package grounding

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Default captioner sidecar settings.
const (
	DefaultCaptionerPort   = 8618
	DefaultCaptionerScript = "caption_server.py"
)

// captionerScript is injected by main.go via SetCaptionerScript (go:embed of
// python/caption_server.py) so released binaries carry the sidecar.
var captionerScript string

// SetCaptionerScript installs the embedded sidecar script source.
func SetCaptionerScript(src string) { captionerScript = src }

// CaptionerScriptPath returns the on-disk sidecar path: the KVM_CAPTIONER_SCRIPT
// override, else the cache copy (written from the embedded source when missing
// or when the embedded copy is newer).
func CaptionerScriptPath() (string, error) {
	if p := os.Getenv("KVM_CAPTIONER_SCRIPT"); p != "" {
		return p, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("determine cache directory: %w", err)
	}
	dest := filepath.Join(base, "kvm-cli", DefaultCaptionerScript)
	if captionerScript != "" {
		if cur, err := os.ReadFile(dest); err != nil || string(cur) != captionerScript {
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(dest, []byte(captionerScript), 0o755); err != nil {
				return "", err
			}
		}
	}
	return dest, nil
}

// FindUv locates the uv binary: KVM_UV override, PATH, then ~/.local/bin/uv.
func FindUv() string {
	if p := os.Getenv("KVM_UV"); p != "" {
		return p
	}
	if p, err := exec.LookPath("uv"); err == nil {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, ".local", "bin", "uv")
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
		return p
	}
	return ""
}

// CaptionerBaseURL returns the sidecar base URL for a port.
func CaptionerBaseURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// EnsureCaptioner returns a working Captioner, starting the managed sidecar if
// needed and none is healthy at the target URL. It waits up to timeout for the
// model to become ready (the first start downloads ~1 GB from HuggingFace).
// The returned bool reports whether this call spawned the sidecar.
func EnsureCaptioner(ctx context.Context, baseURL string, port int, device string, timeout time.Duration) (Captioner, bool, error) {
	// Already healthy (or loading) at the target URL? Reuse it.
	if _, err := FetchCaptionerHealth(ctx, baseURL); err == nil {
		if err := waitReady(ctx, baseURL, timeout); err != nil {
			return nil, false, err
		}
		return &HTTPCaptioner{BaseURL: baseURL}, false, nil
	}

	uv := FindUv()
	if uv == "" {
		return nil, false, fmt.Errorf(
			"captioner sidecar needs uv (https://docs.astral.sh/uv/); install it or point KVM_UV at the binary, or start the sidecar manually with 'kvm-cli cua captioner serve'")
	}
	script, err := CaptionerScriptPath()
	if err != nil {
		return nil, false, err
	}

	args := []string{"run", script, "--port", fmt.Sprint(port), "--host", "127.0.0.1", "--warmup"}
	if device != "" {
		args = append(args, "--device", device)
	}
	logFile, err := CaptionerLogPath()
	if err != nil {
		return nil, false, err
	}
	lf, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, false, fmt.Errorf("open captioner log: %w", err)
	}
	_ = lf.Close()
	cmd := exec.Command(uv, args...)
	// The sidecar's stdout/stderr go to a dedicated log file: never the caller's
	// pipes, so a long-lived sidecar cannot wedge the parent process.
	cmd.Stdout, cmd.Stderr = openCaptionerLog()
	if err := cmd.Start(); err != nil {
		return nil, false, fmt.Errorf("start captioner sidecar: %w", err)
	}
	_ = writePid(cmd.Process.Pid)
	spawned := true

	// Poll health (connection refused while uvicorn boots).
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := FetchCaptionerHealth(ctx, baseURL); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return nil, spawned, ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}

	if err := waitReady(ctx, baseURL, timeout); err != nil {
		return nil, spawned, err
	}
	return &HTTPCaptioner{BaseURL: baseURL}, spawned, nil
}

// waitReady polls /health until ready:true, the context is done, or timeout.
func waitReady(ctx context.Context, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		h, err := FetchCaptionerHealth(ctx, baseURL)
		if err == nil && h.Ready {
			return nil
		}
		if time.Now().After(deadline) {
			if err == nil && h != nil && h.Error != nil && *h.Error != "" {
				return fmt.Errorf("captioner failed to load: %s", *h.Error)
			}
			return fmt.Errorf("captioner not ready after %s at %s", timeout, baseURL)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// CaptionerLogPath is where the managed sidecar writes its output.
func CaptionerLogPath() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "kvm-cli", "captioner.log"), nil
}

// openCaptionerLog opens the sidecar log for append, returning the two handles
// the child inherits (closed only when this process exits — fine for a daemon).
func openCaptionerLog() (*os.File, *os.File) {
	p, err := CaptionerLogPath()
	if err != nil {
		p = os.DevNull
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		f, _ = os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	}
	return f, f
}

// writePid records the spawned sidecar PID for 'cua captioner stop'.
func writePid(pid int) error {
	base, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	path := filepath.Join(base, "kvm-cli", "captioner.pid")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(fmt.Sprint(pid)), 0o600)
}

// ReadCaptionerPid returns the recorded sidecar PID (0 when none).
func ReadCaptionerPid() int {
	base, err := os.UserCacheDir()
	if err != nil {
		return 0
	}
	data, err := os.ReadFile(filepath.Join(base, "kvm-cli", "captioner.pid"))
	if err != nil {
		return 0
	}
	var pid int
	_, _ = fmt.Sscanf(string(data), "%d", &pid)
	return pid
}

// PortFree reports whether a TCP port is listening-free (used by serve).
func PortFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// Ensure HTTP import is used (CaptionerHealth builds requests directly today;
// keep the import anchored for future header work).
var _ = http.MethodGet
