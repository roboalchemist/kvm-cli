package grounding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/models"
)

func TestVerifyModel(t *testing.T) {
	// Missing file.
	if err := VerifyModel(filepath.Join(t.TempDir(), "nope.onnx")); err == nil {
		t.Error("expected error for missing model file")
	}
	// Right size, wrong hash.
	p := filepath.Join(t.TempDir(), "model.onnx")
	if err := os.WriteFile(p, make([]byte, DefaultModelBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyModel(p); err == nil || !contains(err.Error(), "sha256") {
		t.Errorf("VerifyModel(wrong hash) = %v, want sha256 mismatch", err)
	}
	// Wrong size.
	small := filepath.Join(t.TempDir(), "small.onnx")
	if err := os.WriteFile(small, make([]byte, 10), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyModel(small); err == nil || !contains(err.Error(), "bytes") {
		t.Errorf("VerifyModel(wrong size) = %v, want size mismatch", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestDownloadModelVerifiesAndRejectsTampered(t *testing.T) {
	// Serve the wrong bytes: the download must be rejected by the SHA-256 check
	// and dest must not exist.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, DefaultModelBytes)) // right size, wrong content
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "model.onnx")
	err := DownloadModel(context.Background(), dest, srv.URL)
	if err == nil || !contains(err.Error(), "sha256") {
		t.Fatalf("DownloadModel(tampered) = %v, want sha256 rejection", err)
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("dest should not exist after failed download: %v", statErr)
	}
}

func TestPlatformProviderMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/model/omniparser/v1/ground" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"success":true,"count":1,"latency":12.5,"elements":[{"type":"text","content":"hi","bbox":[1,2,3,4],"center":[2,3]}]}`))
	}))
	defer srv.Close()

	// The platform path uploads the image bytes, so provide a real tiny JPEG.
	imgPath := filepath.Join(t.TempDir(), "frame.jpg")
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, nil)
	if err := os.WriteFile(imgPath, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &PlatformProvider{Client: models.NewClient(models.Options{BaseURL: srv.URL, HTTPClient: srv.Client()})}
	res, err := p.Ground(context.Background(), imgPath, Options{BoxThreshold: 0.05, IouThreshold: 0.1, IncludeAnnotated: true})
	if err != nil {
		t.Fatalf("Ground: %v", err)
	}
	if res.Backend != BackendPlatform || res.Count != 1 || len(res.Elements) != 1 {
		t.Errorf("result = %+v", res)
	}
}

func TestLocalProviderStickyInitError(t *testing.T) {
	// A model path that does not exist: init fails and the error is sticky.
	p := &LocalProvider{ModelPath: filepath.Join(t.TempDir(), "missing.onnx")}
	for i := 0; i < 2; i++ {
		if _, err := p.Ground(context.Background(), "x.jpg", Options{}); err == nil {
			t.Fatal("expected init error")
		}
	}
	p.Close()
}

func TestDefaultModelPathNoHome(t *testing.T) {
	// The error branch: no HOME/XDG means no cache dir.
	t.Setenv("HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	if _, err := DefaultModelPath(); err == nil {
		t.Error("DefaultModelPath without HOME should fail")
	}
}

func TestDefaultModelPath(t *testing.T) {
	got, err := DefaultModelPath()
	if err != nil {
		t.Fatalf("DefaultModelPath: %v", err)
	}
	if base := filepath.Base(got); base != "model.onnx" {
		t.Errorf("path = %q", got)
	}
}

// TestPlatformProviderErrorPropagates covers the error return of the platform
// backend (device failure inside models.Client.Ground).
func TestPlatformProviderErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	imgPath := filepath.Join(t.TempDir(), "frame.jpg")
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil)
	if err := os.WriteFile(imgPath, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	p := &PlatformProvider{Client: models.NewClient(models.Options{BaseURL: srv.URL, HTTPClient: srv.Client()})}
	if _, err := p.Ground(context.Background(), imgPath, Options{}); err == nil {
		t.Fatal("expected error from failing platform")
	}
}

// TestDownloadModelMkdirError covers the create-directory failure branch.
func TestDownloadModelMkdirError(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// dest's parent chain runs through a regular file -> MkdirAll fails.
	err := downloadModel(context.Background(), filepath.Join(blocker, "sub", "m.onnx"), "http://127.0.0.1:1/x", 10, "x")
	if err == nil || !contains(err.Error(), "create model directory") {
		t.Fatalf("err = %v, want mkdir failure", err)
	}
}

// TestDownloadModelNetworkError covers the transport-error branch.
func TestDownloadModelNetworkError(t *testing.T) {
	// Port 1 refuses connections.
	err := downloadModel(context.Background(), filepath.Join(t.TempDir(), "m.onnx"), "http://127.0.0.1:1/x", 10, "x")
	if err == nil {
		t.Fatal("expected transport error")
	}
}

// TestVerifyModelSuccess covers the happy path with injectable expectations.
func TestVerifyModelSuccess(t *testing.T) {
	p := filepath.Join(t.TempDir(), "model.onnx")
	content := []byte("fake model bytes for hashing")
	if err := os.WriteFile(p, content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	if err := verifyModel(p, int64(len(content)), hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("verifyModel(valid) = %v", err)
	}
	// Wrong size now fails.
	if err := verifyModel(p, int64(len(content))+1, hex.EncodeToString(sum[:])); err == nil {
		t.Error("expected size mismatch")
	}
}

// TestDownloadModelSuccess covers the atomic-rename happy path.
func TestDownloadModelSuccess(t *testing.T) {
	content := []byte("model payload")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "model.onnx")
	sum := sha256.Sum256(content)
	if err := downloadModel(context.Background(), dest, srv.URL, int64(len(content)), hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("downloadModel: %v", err)
	}
	if err := verifyModel(dest, int64(len(content)), hex.EncodeToString(sum[:])); err != nil {
		t.Fatalf("verify after download: %v", err)
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(filepath.Dir(dest))
	if len(entries) != 1 {
		t.Errorf("dir entries = %d, want 1 (no .part leftovers)", len(entries))
	}
}

// TestDownloadModelHTTPError covers the non-200 path.
func TestDownloadModelHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	err := downloadModel(context.Background(), filepath.Join(t.TempDir(), "m.onnx"), srv.URL, 10, "x")
	if err == nil || !contains(err.Error(), "HTTP 403") {
		t.Fatalf("err = %v, want HTTP 403", err)
	}
}

// TestDownloadModelNoDest verifies the default-path branch resolves and creates
// the cache directory (using an isolated cache dir).
func TestDownloadModelNoDest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir) // os.UserCacheDir uses $HOME/Library/Caches on darwin
	// Serve a 1-byte payload with matching expectations so only path resolution
	// is exercised before the hash check fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()
	err := downloadModel(context.Background(), "", srv.URL, 1, "wrong")
	// The failure must be the sha mismatch (meaning the path resolved and the
	// file downloaded), not a path error.
	if err == nil || !contains(err.Error(), "sha256") {
		t.Fatalf("err = %v, want sha256 mismatch after successful download", err)
	}
}

// TestVerifyModelOpenError covers the open-failure branch (stat passes, open
// fails on permissions).
func TestVerifyModelOpenError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can open anything")
	}
	p := filepath.Join(t.TempDir(), "model.onnx")
	if err := os.WriteFile(p, make([]byte, DefaultModelBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o600) })
	if err := VerifyModel(p); err == nil {
		t.Error("expected open error for unreadable model")
	}
}

// TestDownloadModelRenameError covers the final-rename failure branch: dest is
// an existing directory, so the atomic rename cannot succeed.
func TestDownloadModelRenameError(t *testing.T) {
	content := []byte("payload")
	sum := sha256.Sum256(content)
	destDir := filepath.Join(t.TempDir(), "model.onnx") // a directory!
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()
	err := downloadModel(context.Background(), destDir, srv.URL, int64(len(content)), hex.EncodeToString(sum[:]))
	if err == nil {
		t.Fatal("expected rename error when dest is a directory")
	}
}

// TestDownloadModelTempError covers CreateTemp failing when the model directory
// is read-only.
func TestDownloadModelTempError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere")
	}
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	defer srv.Close()
	err := downloadModel(context.Background(), filepath.Join(dir, "model.onnx"), srv.URL, 7, "x")
	if err == nil {
		t.Fatal("expected temp-create error in read-only dir")
	}
}

// TestDownloadModelInvalidURL covers the request-construction error branch.
func TestDownloadModelInvalidURL(t *testing.T) {
	err := downloadModel(context.Background(), filepath.Join(t.TempDir(), "m.onnx"), "http://exa mple.com/x", 10, "x")
	if err == nil {
		t.Fatal("expected request-construction error for invalid URL")
	}
}

// TestDownloadModelShortBody covers the size-mismatch branch (server sends
// fewer bytes than pinned without a Content-Length).
func TestDownloadModelShortBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("abc")) // 3 bytes, no Content-Length
	}))
	defer srv.Close()
	err := downloadModel(context.Background(), filepath.Join(t.TempDir(), "m.onnx"), srv.URL, 10, "x")
	if err == nil || !contains(err.Error(), "downloaded 3 bytes, want 10") {
		t.Fatalf("err = %v, want size mismatch", err)
	}
}

// TestDownloadModelTruncatedBody covers the io.Copy error branch mid-download
// (Content-Length promises more than the handler writes).
func TestDownloadModelTruncatedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("only ten!!"))
	}))
	defer srv.Close()
	err := downloadModel(context.Background(), filepath.Join(t.TempDir(), "m.onnx"), srv.URL, 100, "x")
	if err == nil {
		t.Fatal("expected truncated-body error")
	}
}
