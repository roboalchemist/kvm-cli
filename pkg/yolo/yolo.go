// Package yolo implements the YOLOv8 pre/post-processing used by the local
// grounding backend: letterboxed tensor construction, output decoding, NMS, and
// the mapping back to original-image coordinates as UI elements.
//
// The model this targets is OmniParser's `icon_detect` (a single-class YOLOv8n
// exported to ONNX): input is a 1x3x640x640 NCHW float32 tensor (RGB, scaled to
// 0..1), output is [1,5,N] — four box channels (center-x, center-y, w, h in
// 640-space pixels) plus one post-sigmoid confidence channel — with no NMS
// embedded.
package yolo

import (
	"fmt"
	"image"
	"math"
	"sort"

	"github.com/roboalchemist/kvm-cli/pkg/models"
)

// Detection is one raw box in letterboxed (640-space) pixel coordinates.
type Detection struct {
	X1, Y1, X2, Y2 float64
	Conf           float64
}

// Letterbox converts img into a 1x3xsize x size NCHW float32 tensor (RGB,
// scaled to 0..1), preserving aspect ratio and centering the image on a gray
// (114/255) canvas. It returns the tensor and the scale factor and x/y padding
// needed to map 640-space coordinates back to original pixels.
func Letterbox(img image.Image, size int) ([]float32, float64, float64, float64, error) {
	if size <= 0 {
		return nil, 0, 0, 0, fmt.Errorf("yolo: invalid tensor size %d", size)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 1 || h < 1 {
		return nil, 0, 0, 0, fmt.Errorf("yolo: invalid image %dx%d", w, h)
	}
	scale := math.Min(float64(size)/float64(w), float64(size)/float64(h))
	newW := int(math.Round(float64(w) * scale))
	newH := int(math.Round(float64(h) * scale))
	padX := float64(size-newW) / 2
	padY := float64(size-newH) / 2

	tensor := make([]float32, 3*size*size)
	// Pre-fill with the letterbox gray (114/255) so padding is exact even for
	// non-integer pads.
	const gray = float32(114.0 / 255.0)
	for c := 0; c < 3; c++ {
		plane := tensor[c*size*size : (c+1)*size*size]
		for i := range plane {
			plane[i] = gray
		}
	}

	for y := 0; y < newH; y++ {
		srcY := b.Min.Y + int(math.Floor((float64(y)+0.5)/scale))
		if srcY >= b.Max.Y {
			srcY = b.Max.Y - 1
		}
		dstY := y + int(math.Round(padY))
		rowBase := dstY * size
		for x := 0; x < newW; x++ {
			srcX := b.Min.X + int(math.Floor((float64(x)+0.5)/scale))
			if srcX >= b.Max.X {
				srcX = b.Max.X - 1
			}
			r, g, bl, _ := img.At(srcX, srcY).RGBA()
			dst := rowBase + x + int(math.Round(padX))
			tensor[0*size*size+dst] = float32(r>>8) / 255
			tensor[1*size*size+dst] = float32(g>>8) / 255
			tensor[2*size*size+dst] = float32(bl>>8) / 255
		}
	}
	return tensor, scale, padX, padY, nil
}

// Decode turns a YOLOv8 output tensor ([batch, 5, anchors]: cx, cy, w, h,
// confidence in 640-space pixels) into detections above conf.
func Decode(output []float32, shape []int64, conf float64) ([]Detection, error) {
	if len(shape) != 3 {
		return nil, fmt.Errorf("yolo: unexpected output rank %d (want 3)", len(shape))
	}
	batch, channels, anchors := int(shape[0]), int(shape[1]), int(shape[2])
	if batch != 1 || channels < 5 {
		return nil, fmt.Errorf("yolo: unexpected output shape [%d,%d,%d] (want [1,>=5,N])",
			batch, channels, anchors)
	}
	// [1,C,A] contiguous: element (c, n) lives at c*anchors + n.
	at := func(c int, n int64) float64 {
		return float64(output[int64(c)*int64(anchors)+n])
	}
	var dets []Detection
	for n := int64(0); n < int64(anchors); n++ {
		c := at(4, n) // conf lives in channel 4
		if c < conf {
			continue
		}
		cx := at(0, n)
		cy := at(1, n)
		w := at(2, n)
		h := at(3, n)
		dets = append(dets, Detection{
			X1: cx - w/2, Y1: cy - h/2, X2: cx + w/2, Y2: cy + h/2, Conf: c,
		})
	}
	return dets, nil
}

// iou returns the intersection-over-union of two axis-aligned boxes.
func iou(a, b Detection) float64 {
	ix1 := math.Max(a.X1, b.X1)
	iy1 := math.Max(a.Y1, b.Y1)
	ix2 := math.Min(a.X2, b.X2)
	iy2 := math.Min(a.Y2, b.Y2)
	inter := math.Max(0, ix2-ix1) * math.Max(0, iy2-iy1)
	areaA := (a.X2 - a.X1) * (a.Y2 - a.Y1)
	areaB := (b.X2 - b.X1) * (b.Y2 - b.Y1)
	union := areaA + areaB - inter
	if union <= 0 {
		return 0
	}
	return inter / union
}

// NMS greedily suppresses overlapping detections (keep the highest confidence).
func NMS(dets []Detection, iouThreshold float64) []Detection {
	sorted := make([]Detection, len(dets))
	copy(sorted, dets)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Conf > sorted[j].Conf })
	var keep []Detection
	for _, d := range sorted {
		suppressed := false
		for _, k := range keep {
			if iou(d, k) > iouThreshold {
				suppressed = true
				break
			}
		}
		if !suppressed {
			keep = append(keep, d)
		}
	}
	return keep
}

// ToElements rescales letterboxed detections back to original-image pixel
// coordinates and maps them to Set-of-Mark elements. Local detections are icon
// boxes without OCR content, so Type is "icon", Content is empty, and
// Interactivity is true (icon_detect finds interactable regions).
func ToElements(dets []Detection, scale, padX, padY float64, origW, origH int) []models.Element {
	out := make([]models.Element, 0, len(dets))
	for _, d := range dets {
		x1 := clampF((d.X1-padX)/scale, 0, float64(origW))
		y1 := clampF((d.Y1-padY)/scale, 0, float64(origH))
		x2 := clampF((d.X2-padX)/scale, 0, float64(origW))
		y2 := clampF((d.Y2-padY)/scale, 0, float64(origH))
		if x2-x1 < 1 || y2-y1 < 1 {
			continue
		}
		el := models.Element{
			Type:          "icon",
			Interactivity: true,
			BBox:          [4]float64{x1, y1, x2, y2},
			Center:        [2]float64{(x1 + x2) / 2, (y1 + y2) / 2},
		}
		el.BBoxNorm = [4]float64{x1 / float64(origW), y1 / float64(origH), x2 / float64(origW), y2 / float64(origH)}
		out = append(out, el)
	}
	return out
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
