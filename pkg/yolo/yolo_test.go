package yolo

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/roboalchemist/kvm-cli/pkg/models"
)

// solidImage returns a w x h image where every pixel is the given color.
func solidImage(w, h int, c color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	return img
}

func TestLetterboxTensor(t *testing.T) {
	// 320x160 image into 640: scale = 2.0, newW=640, newH=320, padY = 160.
	img := solidImage(320, 160, color.RGBA{255, 0, 0, 255})
	tensor, scale, padX, padY, err := Letterbox(img, 640)
	if err != nil {
		t.Fatalf("Letterbox: %v", err)
	}
	if len(tensor) != 3*640*640 {
		t.Fatalf("tensor len = %d, want %d", len(tensor), 3*640*640)
	}
	if scale != 2.0 || padX != 0 || padY != 160 {
		t.Fatalf("scale/pads = %v/%v/%v, want 2/0/160", scale, padX, padY)
	}
	// Center of the active region is pure red; the top padding row is gray.
	idx := func(c, y, x int) float32 { return tensor[c*640*640+y*640+x] }
	if idx(0, 320, 320) != 1.0 || idx(1, 320, 320) != 0 || idx(2, 320, 320) != 0 {
		t.Errorf("center pixel = %v/%v/%v, want 1/0/0", idx(0, 320, 320), idx(1, 320, 320), idx(2, 320, 320))
	}
	if idx(0, 10, 320) != float32(114.0/255.0) {
		t.Errorf("pad pixel = %v, want gray 114/255", idx(0, 10, 320))
	}
	// Zero-size image errors.
	if _, _, _, _, err := Letterbox(image.NewRGBA(image.Rect(0, 0, 0, 0)), 640); err == nil {
		t.Error("expected error for empty image")
	}
}

func TestLetterboxSquareNoPad(t *testing.T) {
	img := solidImage(100, 100, color.RGBA{0, 255, 0, 255})
	_, scale, padX, padY, err := Letterbox(img, 640)
	if err != nil {
		t.Fatalf("Letterbox: %v", err)
	}
	if scale != 6.4 || padX != 0 || padY != 0 {
		t.Fatalf("scale/pads = %v/%v/%v, want 6.4/0/0", scale, padX, padY)
	}
}

func TestDecodeFiltersAndConverts(t *testing.T) {
	// Build a [1,5,4] tensor: channel-major (cx, cy, w, h, conf per anchor).
	// anchors: a0 high-conf box at (100,100,50,50); a1 low-conf; a2 high-conf box.
	anchors := 4
	out := make([]float32, 5*anchors)
	set := func(anchor, c int, v float32) { out[c*anchors+anchor] = v }
	// anchor 0: cx=100 cy=100 w=50 h=50 conf=0.9 -> box (75,75)-(125,125)
	set(0, 0, 100)
	set(0, 1, 100)
	set(0, 2, 50)
	set(0, 3, 50)
	set(0, 4, 0.9)
	// anchor 1: below threshold
	set(1, 0, 200)
	set(1, 1, 200)
	set(1, 2, 10)
	set(1, 3, 10)
	set(1, 4, 0.01)
	// anchor 2: cx=300 cy=300 w=20 h=20 conf=0.7 -> (290,290)-(310,310)
	set(2, 0, 300)
	set(2, 1, 300)
	set(2, 2, 20)
	set(2, 3, 20)
	set(2, 4, 0.7)

	dets, err := Decode(out, []int64{1, 5, int64(anchors)}, 0.05)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(dets) != 2 {
		t.Fatalf("got %d detections, want 2", len(dets))
	}
	if dets[0].X1 != 75 || dets[0].Y1 != 75 || dets[0].X2 != 125 || dets[0].Y2 != 125 {
		t.Errorf("det0 = %+v", dets[0])
	}
	if dets[1].X1 != 290 || dets[1].X2 != 310 {
		t.Errorf("det1 = %+v", dets[1])
	}
	// Malformed shapes error.
	if _, err := Decode(out, []int64{1, 5}, 0.05); err == nil {
		t.Error("expected error for rank-2 shape")
	}
	if _, err := Decode(out, []int64{2, 5, 4}, 0.05); err == nil {
		t.Error("expected error for batch != 1")
	}
}

func TestNMSSuppressesOverlaps(t *testing.T) {
	dets := []Detection{
		{X1: 0, Y1: 0, X2: 100, Y2: 100, Conf: 0.6},
		{X1: 5, Y1: 5, X2: 105, Y2: 105, Conf: 0.9}, // heavily overlaps #0
		{X1: 300, Y1: 300, X2: 400, Y2: 400, Conf: 0.5},
	}
	kept := NMS(dets, 0.1)
	if len(kept) != 2 {
		t.Fatalf("kept %d, want 2", len(kept))
	}
	// The higher-confidence overlap must be the survivor.
	if kept[0].Conf != 0.9 {
		t.Errorf("kept[0].Conf = %v, want 0.9", kept[0].Conf)
	}
	// iou=1 with itself.
	if iou(dets[0], dets[0]) != 1 {
		t.Error("self-IoU should be 1")
	}
	// Disjoint boxes -> no suppression at any threshold.
	if kept2 := NMS([]Detection{dets[0], dets[2]}, 0.01); len(kept2) != 2 {
		t.Errorf("disjoint boxes suppressed: kept %d", len(kept2))
	}
}

func TestIOUEdgeCases(t *testing.T) {
	a := Detection{X1: 0, Y1: 0, X2: 10, Y2: 10}
	touching := Detection{X1: 10, Y1: 0, X2: 20, Y2: 10}
	if iou(a, touching) != 0 {
		t.Error("edge-touching boxes should have IoU 0")
	}
	contained := Detection{X1: 2, Y1: 2, X2: 4, Y2: 4}
	if math.Abs(iou(a, contained)-0.04/1.0) > 1e-9 {
		t.Errorf("containment IoU = %v, want 0.04", iou(a, contained))
	}
}

func TestToElementsRescale(t *testing.T) {
	// Original 1920x1080 into 640: scale = 1/3, padY = (640-360)/2 = 140.
	scale, padX, padY := 640.0/1920.0, 0.0, (640-640.0*1080.0/1920.0)/2
	dets := []Detection{
		// 640-space box covering the top-left quadrant of the active region.
		{X1: 0, Y1: padY, X2: 320, Y2: padY + 180, Conf: 0.8},
		// Degenerate after rescale (1px) should be dropped.
		{X1: 10, Y1: padY, X2: 10.2, Y2: padY + 1, Conf: 0.9},
	}
	elems := ToElements(dets, scale, padX, padY, 1920, 1080)
	if len(elems) != 1 {
		t.Fatalf("got %d elements, want 1 (degenerate dropped)", len(elems))
	}
	el := elems[0]
	if el.Type != "icon" || !el.Interactivity || el.Content != "" {
		t.Errorf("element = %+v, want interactive icon with empty content", el)
	}
	if el.BBox[0] != 0 || el.BBox[1] != 0 || el.BBox[2] != 960 || el.BBox[3] != 540 {
		t.Errorf("bbox = %v, want [0 0 960 540]", el.BBox)
	}
	if el.Center[0] != 480 || el.Center[1] != 270 {
		t.Errorf("center = %v, want [480 270]", el.Center)
	}
	// Normalized bbox matches.
	want := models.Element{BBoxNorm: [4]float64{0, 0, 0.5, 0.5}}
	if el.BBoxNorm != want.BBoxNorm {
		t.Errorf("bbox_norm = %v, want %v", el.BBoxNorm, want.BBoxNorm)
	}
}

func TestToElementsClamps(t *testing.T) {
	// Detections that escape the letterboxed region must clamp to image bounds.
	scale, padX, padY := 1.0, 100.0, 100.0
	dets := []Detection{{X1: -500, Y1: -500, X2: 900, Y2: 900, Conf: 0.5}}
	elems := ToElements(dets, scale, padX, padY, 640, 640)
	if len(elems) != 1 {
		t.Fatalf("got %d elements", len(elems))
	}
	if elems[0].BBox != ([4]float64{0, 0, 640, 640}) {
		t.Errorf("bbox = %v, want clamped full frame", elems[0].BBox)
	}
}
