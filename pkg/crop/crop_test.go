package crop

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/elements"
	"github.com/roboalchemist/kvm-cli/pkg/models"
)

// makeJPEG builds a w x h JPEG with a distinct solid color per quadrant so
// cropping can be verified by pixel.
func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var c color.RGBA
			switch {
			case x < w/2 && y < h/2:
				c = color.RGBA{255, 0, 0, 255} // top-left red
			case x >= w/2 && y < h/2:
				c = color.RGBA{0, 255, 0, 255} // top-right green
			case x < w/2 && y >= h/2:
				c = color.RGBA{0, 0, 255, 255} // bottom-left blue
			default:
				c = color.RGBA{255, 255, 0, 255} // bottom-right yellow
			}
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 100}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	return buf.Bytes()
}

func decode(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return img
}

func TestCropScale(t *testing.T) {
	src := makeJPEG(t, 100, 100)

	// Crop the top-right quadrant (green).
	out, w, h, err := CropScale(src, elements.Region{X1: 50, Y1: 0, X2: 100, Y2: 50}, 1)
	if err != nil {
		t.Fatalf("crop: %v", err)
	}
	if w != 50 || h != 50 {
		t.Fatalf("crop dims = %dx%d, want 50x50", w, h)
	}
	img := decode(t, out)
	if b := img.Bounds(); b.Dx() != w || b.Dy() != h {
		t.Fatalf("decoded bounds %v", b)
	}
	if r, g, bl, _ := img.At(25, 25).RGBA(); !(r < 40000 && g > 60000 && bl < 40000) {
		t.Errorf("expected green center, got r=%d g=%d b=%d", r, g, bl)
	}

	// Scale the whole frame 2x.
	out, w, h, err = CropScale(src, elements.Region{X1: 0, Y1: 0, X2: 100, Y2: 100}, 2)
	if err != nil {
		t.Fatalf("scale: %v", err)
	}
	if w != 200 || h != 200 {
		t.Fatalf("scaled dims = %dx%d, want 200x200", w, h)
	}
}

func TestCropScaleClampsAndErrors(t *testing.T) {
	src := makeJPEG(t, 40, 40)
	// Region extends beyond bounds; clamps to 40x40.
	out, w, h, err := CropScale(src, elements.Region{X1: -10, Y1: -10, X2: 999, Y2: 999}, 1)
	if err != nil {
		t.Fatalf("clamp: %v", err)
	}
	if w != 40 || h != 40 {
		t.Fatalf("clamped dims = %dx%d, want 40x40", w, h)
	}
	_ = out

	// Empty region after clamping.
	if _, _, _, err := CropScale(src, elements.Region{X1: 100, Y1: 100, X2: 200, Y2: 200}, 1); err == nil {
		t.Error("expected empty-region error")
	}
	// Not a JPEG.
	if _, _, _, err := CropScale([]byte("not a jpeg"), elements.Region{X1: 0, Y1: 0, X2: 10, Y2: 10}, 1); err == nil {
		t.Error("expected decode error")
	}
}

// TestCropScaleEdgeFactors exercises the scale <= 0 shortcut, the tiny-scale
// output-size clamp (ow/oh < 1) and the fractional-scale source clamp (sy/sx
// crossing the region edge).
func TestCropScaleEdgeFactors(t *testing.T) {
	src := makeJPEG(t, 30, 30)

	// scale <= 0 behaves as 1.
	_, w, h, err := CropScale(src, elements.Region{X1: 0, Y1: 0, X2: 10, Y2: 10}, 0)
	if err != nil || w != 10 || h != 10 {
		t.Fatalf("zero scale: %dx%d err %v", w, h, err)
	}
	// Very small scale rounds the output below 1 -> clamped to 1x1.
	_, w, h, err = CropScale(src, elements.Region{X1: 0, Y1: 0, X2: 2, Y2: 2}, 0.01)
	if err != nil || w != 1 || h != 1 {
		t.Fatalf("tiny scale: %dx%d err %v", w, h, err)
	}
	// Fractional scale on an odd-sized region exercises the source-edge clamp.
	_, w, h, err = CropScale(src, elements.Region{X1: 1, Y1: 1, X2: 8, Y2: 5}, 1.7)
	if err != nil || w != 12 || h != 7 {
		t.Fatalf("fractional scale: %dx%d err %v", w, h, err)
	}
}

func TestTransformMapToSource(t *testing.T) {
	tr := Transform{Region: elements.Region{X1: 100, Y1: 200, X2: 300, Y2: 400}, Scale: 2}
	x, y := tr.MapToSource(20, 40)
	if x != 110 || y != 220 {
		t.Fatalf("MapToSource = %v,%v want 110,220", x, y)
	}
	// Scale <=0 treated as 1.
	z := Transform{Region: elements.Region{X1: 5, Y1: 6, X2: 10, Y2: 10}, Scale: 0}
	if x, y := z.MapToSource(3, 4); x != 8 || y != 10 {
		t.Fatalf("zero-scale map = %v,%v", x, y)
	}
}

func TestMapElements(t *testing.T) {
	tr := Transform{Region: elements.Region{X1: 100, Y1: 100, X2: 200, Y2: 200}, Scale: 2}
	in := []models.Element{{
		Type:   "text",
		Center: [2]float64{20, 40},
		BBox:   [4]float64{10, 30, 30, 50},
	}}
	out := MapElements(in, tr)
	if out[0].Center[0] != 110 || out[0].Center[1] != 120 {
		t.Errorf("center = %v", out[0].Center)
	}
	if out[0].BBox[0] != 105 || out[0].BBox[1] != 115 || out[0].BBox[2] != 115 || out[0].BBox[3] != 125 {
		t.Errorf("bbox = %v", out[0].BBox)
	}
	// Original untouched.
	if in[0].Center[0] != 20 {
		t.Error("input mutated")
	}
}

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 3), uint8(y * 3), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func TestCropScalePNG(t *testing.T) {
	src := makePNG(t, 40, 20)
	out, w, h, err := CropScale(src, elements.Region{X1: 5, Y1: 5, X2: 25, Y2: 15}, 2)
	if err != nil {
		t.Fatalf("crop png: %v", err)
	}
	if w != 40 || h != 20 {
		t.Fatalf("png crop dims = %dx%d want 40x20", w, h)
	}
	// Output must be a PNG (same format as input).
	if !bytes.Equal(out[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}) {
		t.Errorf("expected PNG output, got % x", out[:4])
	}
	img := decodePNGImg(t, out)
	_ = img
}

func TestDecodeImageErrors(t *testing.T) {
	// PNG signature but corrupt body.
	bad := append([]byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}, []byte("garbage")...)
	if _, _, _, err := CropScale(bad, elements.Region{X1: 0, Y1: 0, X2: 4, Y2: 4}, 1); err == nil {
		t.Error("expected PNG decode error")
	}
	// Unsupported format.
	if _, _, _, err := CropScale([]byte("BM not an image"), elements.Region{X1: 0, Y1: 0, X2: 4, Y2: 4}, 1); err == nil {
		t.Error("expected unsupported-format error")
	}
}

func decodePNGImg(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("png decode: %v", err)
	}
	return img
}
