// Package crop provides stdlib-only JPEG region cropping and nearest-neighbor
// upscaling, plus the coordinate mapping needed to translate a point in a crop
// back to source-frame pixels. It lets an agent magnify a small UI region (to
// read it) without leaving the CLI.
package crop

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"

	"github.com/roboalchemist/kvm-cli/pkg/elements"
	"github.com/roboalchemist/kvm-cli/pkg/models"
)

// Transform describes how a cropped/scaled image relates to its source frame:
// the source rectangle that was kept and the (>=1) upscale factor applied.
type Transform struct {
	Region elements.Region
	Scale  float64
}

// MapToSource maps a coordinate in the cropped/scaled image back to source-frame
// pixels.
func (t Transform) MapToSource(x, y float64) (float64, float64) {
	s := t.Scale
	if s <= 0 {
		s = 1
	}
	return t.Region.X1 + x/s, t.Region.Y1 + y/s
}

// CropScale decodes JPEG or PNG data, crops it to r (clamped to the image
// bounds), upscales by scale (>=1, nearest-neighbor; <=0 means 1), and returns
// the re-encoded image (in the same format as the input) plus its output
// dimensions. The region must be non-degenerate after clamping.
func CropScale(data []byte, r elements.Region, scale float64) ([]byte, int, int, error) {
	if scale <= 0 {
		scale = 1
	}
	src, format, err := decodeImage(data)
	if err != nil {
		return nil, 0, 0, err
	}
	b := src.Bounds()
	x1 := clampInt(int(math.Round(r.X1)), 0, b.Dx())
	y1 := clampInt(int(math.Round(r.Y1)), 0, b.Dy())
	x2 := clampInt(int(math.Round(r.X2)), 0, b.Dx())
	y2 := clampInt(int(math.Round(r.Y2)), 0, b.Dy())
	cw, ch := x2-x1, y2-y1
	if cw <= 0 || ch <= 0 {
		return nil, 0, 0, fmt.Errorf("crop: region is empty within %dx%d", b.Dx(), b.Dy())
	}
	ow := int(math.Round(float64(cw) * scale))
	oh := int(math.Round(float64(ch) * scale))
	if ow < 1 {
		ow = 1
	}
	if oh < 1 {
		oh = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for oy := 0; oy < oh; oy++ {
		sy := b.Min.Y + y1 + int(float64(oy)/scale)
		if sy >= b.Min.Y+y2 {
			sy = b.Min.Y + y2 - 1
		}
		for ox := 0; ox < ow; ox++ {
			sx := b.Min.X + x1 + int(float64(ox)/scale)
			if sx >= b.Min.X+x2 {
				sx = b.Min.X + x2 - 1
			}
			dst.Set(ox, oy, src.At(sx, sy))
		}
	}

	var buf bytes.Buffer
	switch format {
	case "png":
		if err := png.Encode(&buf, dst); err != nil {
			return nil, 0, 0, fmt.Errorf("crop: encode png: %w", err)
		}
	default:
		if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 90}); err != nil {
			return nil, 0, 0, fmt.Errorf("crop: encode jpeg: %w", err)
		}
	}
	return buf.Bytes(), ow, oh, nil
}

// decodeImage decodes JPEG or PNG by content sniffing and reports the format.
func decodeImage(data []byte) (image.Image, string, error) {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8:
		img, err := jpeg.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, "", fmt.Errorf("crop: decode jpeg: %w", err)
		}
		return img, "jpeg", nil
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}):
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			return nil, "", fmt.Errorf("crop: decode png: %w", err)
		}
		return img, "png", nil
	default:
		return nil, "", fmt.Errorf("crop: unsupported image format (not JPEG or PNG)")
	}
}

// clampInt clamps v to [lo, hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// MapElements returns a copy of elems with every center and bbox mapped from a
// cropped source image back to full-frame source coordinates using t.
func MapElements(elems []models.Element, t Transform) []models.Element {
	out := make([]models.Element, len(elems))
	for i, e := range elems {
		out[i] = e
		out[i].Center[0], out[i].Center[1] = t.MapToSource(e.Center[0], e.Center[1])
		out[i].BBox[0], out[i].BBox[1] = t.MapToSource(e.BBox[0], e.BBox[1])
		out[i].BBox[2], out[i].BBox[3] = t.MapToSource(e.BBox[2], e.BBox[3])
	}
	return out
}
