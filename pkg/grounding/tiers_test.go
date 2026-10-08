package grounding

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/models"
)

type fakeProvider struct {
	name    string
	result  *Result
	err     error
	calls   int
	gotPath string
}

func (f *fakeProvider) Ground(ctx context.Context, imagePath string, opts Options) (*Result, error) {
	f.calls++
	f.gotPath = imagePath
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func emptyRes(name string) *Result {
	return &Result{Backend: name, Model: name, Count: 0}
}

func boxesRes(name string, n int) *Result {
	els := make([]models.Element, n)
	for i := range els {
		els[i] = models.Element{Type: "icon", BBox: [4]float64{0, 0, 10, 10}, Center: [2]float64{5, 5}}
	}
	return &Result{Backend: name, Model: name, Count: n, Elements: els}
}

func TestTierEscalatesOnEmpty(t *testing.T) {
	t0 := &fakeProvider{name: "t0", result: emptyRes("t0")}
	t1 := &fakeProvider{name: "t1", result: boxesRes("t1", 3)}
	ladder := &Tier{Name: "t0", Provider: t0, EscalateTo: &Tier{Name: "t1", Provider: t1}}

	res, attempts, err := ladder.Ground(context.Background(), "frame.png", Options{})
	if err != nil {
		t.Fatalf("Ground: %v", err)
	}
	if t0.calls != 1 || t1.calls != 1 {
		t.Errorf("calls: t0=%d t1=%d, want 1/1", t0.calls, t1.calls)
	}
	if res.Model != "t1" || res.Count != 3 {
		t.Errorf("result = %+v, want t1 with 3 elements", res)
	}
	if len(attempts) != 2 || attempts[0] != "t0" || attempts[1] != "t1" {
		t.Errorf("attempts = %v", attempts)
	}
}

func TestTierNoEscalationWhenFound(t *testing.T) {
	t0 := &fakeProvider{name: "t0", result: boxesRes("t0", 2)}
	t1 := &fakeProvider{name: "t1", result: boxesRes("t1", 9)}
	ladder := &Tier{Name: "t0", Provider: t0, EscalateTo: &Tier{Name: "t1", Provider: t1}}
	res, attempts, err := ladder.Ground(context.Background(), "frame.png", Options{})
	if err != nil {
		t.Fatalf("Ground: %v", err)
	}
	if t1.calls != 0 {
		t.Errorf("t1 should not run when t0 found elements (calls=%d)", t1.calls)
	}
	if res.Model != "t0" || res.Count != 2 || len(attempts) != 1 {
		t.Errorf("res=%+v attempts=%v", res, attempts)
	}
}

func TestTierEntryErrorPropagates(t *testing.T) {
	t0 := &fakeProvider{name: "t0", err: errors.New("boom")}
	t1 := &fakeProvider{name: "t1", result: boxesRes("t1", 1)}
	ladder := &Tier{Name: "t0", Provider: t0, EscalateTo: &Tier{Name: "t1", Provider: t1}}
	_, attempts, err := ladder.Ground(context.Background(), "frame.png", Options{})
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want boom propagated (entry tier without fallback)", err)
	}
	_ = attempts
}

func TestTierFailingFallbackSkipped(t *testing.T) {
	t0 := &fakeProvider{name: "t0", result: emptyRes("t0")}
	t1 := &fakeProvider{name: "t1", err: errors.New("sidecar down")}
	t2 := &fakeProvider{name: "t2", result: boxesRes("t2", 5)}
	ladder := &Tier{Name: "t0", Provider: t0,
		EscalateTo: &Tier{Name: "t1", Provider: t1,
			EscalateTo: &Tier{Name: "t2", Provider: t2}}}
	res, attempts, err := ladder.Ground(context.Background(), "frame.png", Options{})
	if err != nil {
		t.Fatalf("Ground: %v", err)
	}
	if t2.calls != 1 {
		t.Errorf("t2 should run after t1 failure (calls=%d)", t2.calls)
	}
	if res.Model != "t2" || res.Count != 5 || len(attempts) != 3 {
		t.Errorf("res=%+v attempts=%v", res, attempts)
	}
}

func TestTierAllEmpty(t *testing.T) {
	t0 := &fakeProvider{name: "t0", result: emptyRes("t0")}
	t1 := &fakeProvider{name: "t1", result: emptyRes("t1")}
	ladder := &Tier{Name: "t0", Provider: t0, EscalateTo: &Tier{Name: "t1", Provider: t1}}
	res, attempts, err := ladder.Ground(context.Background(), "frame.png", Options{})
	if err != nil {
		t.Fatalf("Ground: %v", err)
	}
	// All tiers empty: the result is reported under the last tier that ran.
	if res.Count != 0 || res.Model != "t1" || len(attempts) != 2 {
		t.Errorf("res=%+v attempts=%v; want empty result from the last tier", res, attempts)
	}
}

// fakeCaptioner records crops and returns canned captions.
type fakeCaptioner struct {
	crops  int
	titles []string
	err    error
}

func (f *fakeCaptioner) Caption(ctx context.Context, crops []image.Image) ([]string, error) {
	f.crops += len(crops)
	if f.err != nil {
		return nil, f.err
	}
	return f.titles, nil
}

// encodePNGBytes encodes an image to PNG bytes.
func encodePNGBytes(img image.Image) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func writeRealPNG(t *testing.T, w, h int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "frame.png")
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := os.WriteFile(p, encodePNGBytes(img), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLocalCaptionedProviderLabelsCrops(t *testing.T) {
	frame := writeRealPNG(t, 100, 100)
	yolo := &LocalProvider{
		runInference: func(ctx context.Context, tensor []float32, shape []int64) ([]float32, []int64, error) {
			// Two boxes in 640-space (square frame: scale 1, no padding).
			anchors := 8400
			out := make([]float32, 5*anchors)
			put := func(n int, cx, cy, w, h, conf float32) {
				out[0*anchors+n], out[1*anchors+n], out[2*anchors+n], out[3*anchors+n], out[4*anchors+n] = cx, cy, w, h, conf
			}
			put(0, 50, 50, 20, 20, 0.9)  // (40,40)-(60,60)
			put(50, 90, 90, 10, 10, 0.8) // (85,85)-(95,95)
			return out, []int64{1, 5, int64(anchors)}, nil
		},
	}
	cap := &fakeCaptioner{titles: []string{"a blue gear icon", "a power button"}}
	p := &LocalCaptionedProvider{YOLO: yolo, Captioner: cap}
	res, err := p.Ground(context.Background(), frame, Options{})
	if err != nil {
		t.Fatalf("Ground: %v", err)
	}
	if cap.crops != 2 {
		t.Errorf("captioned %d crops, want 2", cap.crops)
	}
	if res.Model != "icon_detect-local+florence" {
		t.Errorf("model = %q", res.Model)
	}
	if res.Elements[0].Content != "a blue gear icon" || res.Elements[1].Content != "a power button" {
		t.Errorf("contents = %q / %q", res.Elements[0].Content, res.Elements[1].Content)
	}
	// Type stays icon; tier-1 captions are not OCR text elements.
	for _, el := range res.Elements {
		if el.Type != "icon" || !el.Interactivity {
			t.Errorf("element = %+v", el)
		}
	}
}

func TestLocalCaptionedProviderEmptyStaysEmpty(t *testing.T) {
	frame := writeRealPNG(t, 64, 64)
	yolo := &LocalProvider{
		runInference: func(ctx context.Context, tensor []float32, shape []int64) ([]float32, []int64, error) {
			return make([]float32, 5*8400), []int64{1, 5, 8400}, nil // all below threshold
		},
	}
	cap := &fakeCaptioner{titles: []string{"should not be called"}}
	p := &LocalCaptionedProvider{YOLO: yolo, Captioner: cap}
	res, err := p.Ground(context.Background(), frame, Options{})
	if err != nil {
		t.Fatalf("Ground: %v", err)
	}
	if res.Count != 0 || cap.crops != 0 {
		t.Errorf("count=%d crops=%d; captioner must not run on empty detection", res.Count, cap.crops)
	}
}

func TestLocalCaptionedProviderCaptionErrorPropagates(t *testing.T) {
	frame := writeRealPNG(t, 64, 64)
	yolo := &LocalProvider{
		runInference: func(ctx context.Context, tensor []float32, shape []int64) ([]float32, []int64, error) {
			anchors := 8400
			out := make([]float32, 5*anchors)
			out[4*anchors+0] = 0.9
			out[0*anchors+0], out[1*anchors+0], out[2*anchors+0], out[3*anchors+0] = 320, 320, 50, 50
			return out, []int64{1, 5, int64(anchors)}, nil
		},
	}
	p := &LocalCaptionedProvider{YOLO: yolo, Captioner: &fakeCaptioner{err: errors.New("sidecar down")}}
	if _, err := p.Ground(context.Background(), frame, Options{}); err == nil {
		t.Fatal("expected captioner error to propagate")
	}
}

func TestCropBoundsClamps(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 50, 50))
	// Out-of-range bbox clamps into the image.
	c := cropBounds(img, [4]float64{-10, -10, 999, 40})
	if c.Bounds().Dx() != 50 || c.Bounds().Dy() != 41 {
		t.Errorf("crop bounds = %v", c.Bounds())
	}
	// Degenerate bbox still yields a 1x1 crop.
	c2 := cropBounds(img, [4]float64{10, 10, 10, 10})
	if c2.Bounds().Dx() < 1 || c2.Bounds().Dy() < 1 {
		t.Errorf("degenerate crop bounds = %v", c2.Bounds())
	}
}

// TestTierFinalFallbackErrorReturnsLastGood locks in the graceful-degradation
// semantics: when the final tier fails, the last good (possibly empty) result
// is reported instead of an error.
func TestTierFinalFallbackErrorReturnsLastGood(t *testing.T) {
	t0 := &fakeProvider{name: "t0", result: emptyRes("t0")}
	t1 := &fakeProvider{name: "t1", err: errors.New("platform 500")}
	ladder := &Tier{Name: "t0", Provider: t0, EscalateTo: &Tier{Name: "t1", Provider: t1}}
	res, attempts, err := ladder.Ground(context.Background(), "frame.png", Options{})
	if err != nil {
		t.Fatalf("err = %v, want the last good result", err)
	}
	if res.Model != "t0" || res.Count != 0 || len(attempts) != 2 {
		t.Errorf("res=%+v attempts=%v", res, attempts)
	}
	if t1.calls != 1 {
		t.Errorf("t1 calls = %d, want 1", t1.calls)
	}
}
