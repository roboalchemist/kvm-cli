package grounding

import (
	"context"
	"fmt"
	"image"
	"os"
	"time"
)

// LocalCaptionedProvider is grounding tier 1: the local YOLO detector finds
// interactive icon boxes, then the local Florence-2 captioner labels each crop.
// Captions are model-generated descriptions, NOT OCR text — text elements still
// require the platform tier.
type LocalCaptionedProvider struct {
	// YOLO grounds the frame into boxes.
	YOLO *LocalProvider
	// Captioner labels each detected crop.
	Captioner Captioner
}

// Ground implements Provider.
func (p *LocalCaptionedProvider) Ground(ctx context.Context, imagePath string, opts Options) (*Result, error) {
	start := time.Now()
	base, err := p.YOLO.Ground(ctx, imagePath, opts)
	if err != nil {
		return nil, err
	}
	if len(base.Elements) == 0 {
		return base, nil
	}

	// Crop every detected box out of the original frame.
	src, err := openDecode(imagePath)
	if err != nil {
		return nil, err
	}
	crops := make([]image.Image, 0, len(base.Elements))
	for _, el := range base.Elements {
		crops = append(crops, cropBounds(src, el.BBox))
	}
	captions, err := p.Captioner.Caption(ctx, crops)
	if err != nil {
		return nil, err
	}
	for i := range base.Elements {
		if i < len(captions) {
			base.Elements[i].Content = captions[i]
		}
	}
	return &Result{
		Elements:  base.Elements,
		Count:     len(base.Elements),
		ElapsedMS: float64(time.Since(start).Microseconds()) / 1000.0,
		Backend:   BackendLocal,
		Width:     base.Width,
		Height:    base.Height,
		Model:     "icon_detect-local+florence",
	}, nil
}

// Tier is one rung of the grounding escalation ladder.
type Tier struct {
	// Name identifies the tier in output ("local", "local+florence", "platform").
	Name string
	// Provider grounds the frame.
	Provider Provider
	// EscalateTo is the next tier when this one yields zero elements; nil ends
	// the ladder.
	EscalateTo *Tier
}

// Ground runs the ladder: the tier grounds the frame, and — only when
// escalation is enabled and the tier returns zero elements — the next tier is
// tried. The result's Model carries the tier that produced the final answer,
// and Attempts lists every tier name that was tried in order. A failing entry
// tier is a real error; failing fallbacks are skipped, and if the final tier
// fails the last good (possibly empty) result is reported instead.
func (t *Tier) Ground(ctx context.Context, imagePath string, opts Options) (*Result, []string, error) {
	var attempts []string
	var lastGood *Result
	for cur := t; cur != nil; cur = cur.EscalateTo {
		attempts = append(attempts, cur.Name)
		res, err := cur.Provider.Ground(ctx, imagePath, opts)
		if err != nil {
			if cur.EscalateTo != nil && len(attempts) > 1 {
				fmt.Fprintf(os.Stderr, "grounding tier %q failed: %v; escalating\n", cur.Name, err)
				continue
			}
			if lastGood != nil {
				fmt.Fprintf(os.Stderr, "grounding tier %q failed: %v; reporting the last good result\n", cur.Name, err)
				return lastGood, attempts, nil
			}
			return nil, attempts, err
		}
		lastGood = res
		if len(res.Elements) == 0 && cur.EscalateTo != nil {
			continue
		}
		return res, attempts, nil
	}
	if lastGood != nil {
		return lastGood, attempts, nil
	}
	return &Result{Backend: t.Name, Model: t.Name, Count: 0}, attempts, nil
}

// cropBounds extracts the sub-image for a pixel bbox, clamped to the image.
func cropBounds(src image.Image, bbox [4]float64) image.Image {
	b := src.Bounds()
	x1 := clampInt(int(bbox[0]), b.Min.X, b.Max.X-1)
	y1 := clampInt(int(bbox[1]), b.Min.Y, b.Max.Y-1)
	x2 := clampInt(int(bbox[2])+1, b.Min.X+1, b.Max.X)
	y2 := clampInt(int(bbox[3])+1, b.Min.Y+1, b.Max.Y)
	if x2 <= x1 || y2 <= y1 {
		// Degenerate: return a 1x1 crop so captioning still has something.
		return src.(interface {
			SubImage(r image.Rectangle) image.Image
		}).SubImage(image.Rect(x1, y1, x1+1, y1+1))
	}
	sub, ok := src.(interface {
		SubImage(r image.Rectangle) image.Image
	})
	if !ok {
		return src
	}
	return sub.SubImage(image.Rect(x1, y1, x2, y2))
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
