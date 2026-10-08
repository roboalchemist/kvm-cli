package grounding

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/ort"
	"github.com/roboalchemist/kvm-cli/pkg/yolo"
)

// writeTestPNG writes a w x h solid gray PNG and returns its path.
func writeTestPNG(t *testing.T, w, h int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "frame.png")
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, image.NewRGBA(image.Rect(0, 0, 1, 1)).At(0, 0))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestLocalProviderGroundPipeline runs the full local pipeline with a fake
// inference runner returning a synthetic [1,5,N] tensor with two well-separated
// boxes, and asserts the mapped element geometry on a 1920x1080 frame.
func TestLocalProviderGroundPipeline(t *testing.T) {
	imgPath := writeTestPNG(t, 1920, 1080)

	// 640-space: active region after letterbox: y in [140, 500] (scale 1/3).
	anchors := 8400
	out := make([]float32, 5*anchors)
	put := func(n int, cx, cy, w, h, conf float32) {
		out[0*anchors+n] = cx
		out[1*anchors+n] = cy
		out[2*anchors+n] = w
		out[3*anchors+n] = h
		out[4*anchors+n] = conf
	}
	// Box A: 640-space (0,140)-(320,320) -> original (0,0)-(960,540).
	put(0, 160, 230, 320, 180, 0.9)
	// Box B: (320,140)-(640,500) -> original (960,0)-(1920,1080).
	put(100, 480, 320, 320, 360, 0.8)

	p := &LocalProvider{
		ModelPath: "unused.onnx",
		runInference: func(ctx context.Context, tensor []float32, shape []int64) ([]float32, []int64, error) {
			if len(tensor) != 3*640*640 {
				t.Errorf("input tensor len = %d, want %d", len(tensor), 3*640*640)
			}
			return out, []int64{1, 5, int64(anchors)}, nil
		},
	}
	res, err := p.Ground(context.Background(), imgPath, Options{BoxThreshold: 0.05, IouThreshold: 0.1})
	if err != nil {
		t.Fatalf("Ground: %v", err)
	}
	if res.Backend != BackendLocal || res.Count != 2 || len(res.Elements) != 2 {
		t.Fatalf("result = %+v", res)
	}
	a := res.Elements[0]
	if a.BBox[0] != 0 || a.BBox[1] != 0 || a.BBox[2] != 960 || a.BBox[3] != 540 {
		t.Errorf("element A bbox = %v, want [0 0 960 540]", a.BBox)
	}
	b := res.Elements[1]
	if b.BBox[0] != 960 || b.BBox[2] != 1920 {
		t.Errorf("element B bbox = %v", b.BBox)
	}
	if !b.Interactivity || b.Content != "" {
		t.Errorf("element B = %+v, want interactive, no content", b)
	}
	if res.ElapsedMS < 0 || math.IsNaN(res.ElapsedMS) {
		t.Errorf("elapsed = %v", res.ElapsedMS)
	}
}

// TestLocalProviderRunInferenceError verifies inference errors propagate.
func TestLocalProviderRunInferenceError(t *testing.T) {
	imgPath := writeTestPNG(t, 64, 64)
	p := &LocalProvider{
		runInference: func(ctx context.Context, tensor []float32, shape []int64) ([]float32, []int64, error) {
			return nil, nil, errors.New("boom")
		},
	}
	if _, err := p.Ground(context.Background(), imgPath, Options{}); err == nil {
		t.Fatal("expected inference error")
	}
}

// TestLocalProviderGroundsRealYoloMath double-checks that the fake runner path
// exercises the same yolo decode used in production (one anchor at conf 0.5).
func TestLocalProviderGroundsRealYoloMath(t *testing.T) {
	imgPath := writeTestPNG(t, 640, 640) // square: scale 1, no padding
	anchors := 8400
	out := make([]float32, 5*anchors)
	out[0*anchors+7] = 320 // cx
	out[1*anchors+7] = 320 // cy
	out[2*anchors+7] = 100 // w
	out[3*anchors+7] = 100 // h
	out[4*anchors+7] = 0.5 // conf == box threshold

	p := &LocalProvider{
		runInference: func(ctx context.Context, tensor []float32, shape []int64) ([]float32, []int64, error) {
			return out, []int64{1, 5, int64(anchors)}, nil
		},
	}
	res, err := p.Ground(context.Background(), imgPath, Options{BoxThreshold: 0.05, IouThreshold: 0.1})
	if err != nil {
		t.Fatalf("Ground: %v", err)
	}
	if res.Count != 1 {
		t.Fatalf("count = %d, want 1", res.Count)
	}
	el := res.Elements[0]
	if el.BBox != ([4]float64{270, 270, 370, 370}) {
		t.Errorf("bbox = %v, want [270 270 370 370]", el.BBox)
	}
	// Letterbox sanity: a 640x640 input has scale 1 and no padding.
	if _, scale, px, py, _ := yolo.Letterbox(image.NewRGBA(image.Rect(0, 0, 640, 640)), 640); scale != 1 || px != 0 || py != 0 {
		t.Error("640x640 letterbox should be identity")
	}
}

// TestLocalProviderRealORT runs the real ONNX Runtime pipeline (no fake runner)
// against the pinned model when both the library and the model file are
// available; it skips otherwise (CI runners without onnxruntime).
func TestLocalProviderRealORT(t *testing.T) {
	if _, err := os.Stat("/opt/homebrew/lib/libonnxruntime.dylib"); err != nil {
		if _, err2 := os.Stat("/usr/local/lib/libonnxruntime.dylib"); err2 != nil {
			t.Skip("onnxruntime library not installed")
		}
	}
	model := os.Getenv("KVM_TEST_ICON_DETECT_MODEL")
	if model == "" {
		model, _ = DefaultModelPath()
	}
	if _, err := os.Stat(model); err != nil {
		t.Skipf("icon_detect model not present at %s (run 'kvm-cli cua model download --yes')", model)
	}

	p := &LocalProvider{ModelPath: model, LibPath: ort.DiscoverLib("")}
	defer p.Close()
	if err := p.init(); err != nil {
		t.Fatalf("init: %v", err)
	}
	// Second init must be a no-op (sticky success).
	if err := p.init(); err != nil {
		t.Fatalf("second init: %v", err)
	}

	imgPath := writeTestPNG(t, 1280, 720)
	res, err := p.Ground(context.Background(), imgPath, Options{BoxThreshold: 0.05, IouThreshold: 0.1})
	if err != nil {
		t.Fatalf("Ground: %v", err)
	}
	if res.Backend != BackendLocal || res.Count < 0 {
		t.Fatalf("result = %+v", res)
	}
	for _, el := range res.Elements {
		if el.Type != "icon" || !el.Interactivity {
			t.Errorf("element = %+v, want interactive icon", el)
		}
	}
}

// TestDiscoverLibFindsBrewInstall verifies discovery on machines with the brew
// package (skips otherwise).
func TestDiscoverLibFindsBrewInstall(t *testing.T) {
	p := ort.DiscoverLib("")
	if p == "" {
		t.Skip("onnxruntime not found in standard locations")
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("discovered lib %s missing: %v", p, err)
	}
}

// TestLocalProviderGroundErrors covers the open/decode error branches.
func TestLocalProviderGroundErrors(t *testing.T) {
	p := &LocalProvider{ModelPath: "unused.onnx"} // init never reached
	// Unreadable source.
	if _, err := p.Ground(context.Background(), filepath.Join(t.TempDir(), "nope.png"), Options{}); err == nil {
		t.Error("expected open error")
	}
	// Not an image.
	bad := filepath.Join(t.TempDir(), "bad.png")
	if err := os.WriteFile(bad, []byte("definitely not a png"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Ground(context.Background(), bad, Options{}); err == nil || !contains(err.Error(), "decode") {
		t.Errorf("err = %v, want decode error", err)
	}
}

// TestLocalProviderCorruptModel covers the session-load error branch: the file
// exists (so the CLI stat gate passes) but is not a valid ONNX model.
func TestLocalProviderCorruptModel(t *testing.T) {
	if _, err := os.Stat("/opt/homebrew/lib/libonnxruntime.dylib"); err != nil {
		t.Skip("onnxruntime not installed")
	}
	corrupt := filepath.Join(t.TempDir(), "corrupt.onnx")
	if err := os.WriteFile(corrupt, []byte("not an onnx model at all"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := &LocalProvider{ModelPath: corrupt, LibPath: ort.DiscoverLib("")}
	defer p.Close()
	if _, err := p.Ground(context.Background(), "x.jpg", Options{}); err == nil {
		t.Fatal("expected session-load error for corrupt model")
	}
}
