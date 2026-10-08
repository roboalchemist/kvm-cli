// Package grounding abstracts where kvm-cli's CUA element grounding runs. Two
// backends are supported:
//
//   - platform (default): the models platform's OmniParser endpoint, which
//     returns OCR'd, labelled elements.
//   - local: OmniParser's icon_detect YOLO model run in-process via ONNX
//     Runtime, returning interactive icon boxes only (no OCR captions — the
//     caller reads the screenshot itself). This needs only a ~12 MB model file
//     plus the platform ONNX Runtime library (brew install onnxruntime), no
//     remote service.
package grounding

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/roboalchemist/kvm-cli/pkg/models"
)

const (
	// BackendPlatform grounds via the models platform (default).
	BackendPlatform = "platform"
	// BackendLocal grounds in-process via ONNX Runtime (icon boxes only).
	BackendLocal = "local"
)

// The pinned icon_detect ONNX model (single-class YOLOv8n, 640x640 letterbox
// input, [1,5,N] output). Community ONNX export of OmniParser-v2.0's detector.
const (
	DefaultModelURL    = "https://huggingface.co/onnx-community/OmniParser-icon_detect/resolve/main/onnx/model.onnx"
	DefaultModelBytes  = int64(12136163)
	DefaultModelSHA256 = "199626646b896fc40be49f30185f8c03a7ad066c24cb9ab73c17d0c6f3521f2c"
	// ONNXRuntimeAPIVersion is the C API version the binding layer supports.
	ONNXRuntimeAPIVersion = uint32(23)
)

// Options tunes a Ground call (mirrors models.GroundOptions; IncludeAnnotated
// is honoured by the platform backend only — the local backend cannot produce a
// Set-of-Mark image).
type Options struct {
	BoxThreshold     float64
	IouThreshold     float64
	IncludeAnnotated bool
}

// Result is a backend-agnostic grounding result.
type Result struct {
	Elements  []models.Element
	Count     int
	ElapsedMS float64
	Backend   string
	Width     int
	Height    int
	// Model names the grounding model/tier that produced the result.
	Model string
	// AnnotatedImage is the base64 Set-of-Mark PNG. Only the platform backend
	// produces one; the local backend leaves it empty.
	AnnotatedImage string
}

// Provider grounds an image into Set-of-Mark elements.
type Provider interface {
	Ground(ctx context.Context, imagePath string, opts Options) (*Result, error)
}

// PlatformProvider grounds via the models platform's OmniParser endpoint.
type PlatformProvider struct {
	Client *models.Client
}

// Ground implements Provider.
func (p *PlatformProvider) Ground(ctx context.Context, imagePath string, opts Options) (*Result, error) {
	res, err := p.Client.Ground(ctx, imagePath, models.GroundOptions{
		BoxThreshold:     opts.BoxThreshold,
		IouThreshold:     opts.IouThreshold,
		IncludeAnnotated: opts.IncludeAnnotated,
	})
	if err != nil {
		return nil, err
	}
	return &Result{
		Elements:       res.Elements,
		Count:          res.Count,
		ElapsedMS:      res.ElapsedMS,
		Backend:        BackendPlatform,
		Width:          res.Width,
		Height:         res.Height,
		Model:          res.Model,
		AnnotatedImage: res.AnnotatedImage,
	}, nil
}

// DefaultModelPath returns the on-disk cache path for the local icon_detect
// model (<user cache dir>/kvm-cli/models/icon_detect/model.onnx).
func DefaultModelPath() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("determine cache directory: %w", err)
	}
	return filepath.Join(base, "kvm-cli", "models", "icon_detect", "model.onnx"), nil
}

// VerifyModel checks that the file at path matches the pinned size and SHA-256.
func VerifyModel(path string) error {
	return verifyModel(path, DefaultModelBytes, DefaultModelSHA256)
}

// verifyModel is the testable core of VerifyModel.
func verifyModel(path string, wantBytes int64, wantSHA string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.Size() != wantBytes {
		return fmt.Errorf("model file %s is %d bytes, want %d", path, fi.Size(), wantBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash %s: %w", path, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA {
		return fmt.Errorf("model file %s has sha256 %s, want %s", path, got, wantSHA)
	}
	return nil
}

// DownloadModel fetches the pinned icon_detect ONNX model from url to dest
// (creating parent directories), verifying size and SHA-256 before an atomic
// rename. Pass DefaultModelURL as url.
func DownloadModel(ctx context.Context, dest, url string) error {
	return downloadModel(ctx, dest, url, DefaultModelBytes, DefaultModelSHA256)
}

// downloadModel is the testable core of DownloadModel.
func downloadModel(ctx context.Context, dest, url string, wantBytes int64, wantSHA string) error {
	if dest == "" {
		var err error
		dest, err = DefaultModelPath()
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create model directory: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create download request: %w", err)
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download model: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download model: HTTP %d from %s", resp.StatusCode, url)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".model-*.onnx.part")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), resp.Body)
	if err != nil {
		return fmt.Errorf("download model: %w", err)
	}
	if n != wantBytes {
		return fmt.Errorf("downloaded %d bytes, want %d", n, wantBytes)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != wantSHA {
		return fmt.Errorf("downloaded model sha256 %s, want %s", got, wantSHA)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return err
	}
	return nil
}
