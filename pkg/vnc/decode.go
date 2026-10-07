package vnc

import (
	"encoding/binary"
	"fmt"
	"image"
	"io"

	"github.com/roboalchemist/kvm-cli/pkg/redact"
)

// framebuffer is an RGBA canvas the decoders paint into.
type framebuffer struct {
	img *image.RGBA
}

func newFramebuffer(w, h int) *framebuffer {
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return &framebuffer{img: image.NewRGBA(image.Rect(0, 0, w, h))}
}

func (fb *framebuffer) resize(w, h int) {
	if w < 1 || h < 1 || (w == fb.img.Bounds().Dx() && h == fb.img.Bounds().Dy()) {
		return
	}
	fb.img = image.NewRGBA(image.Rect(0, 0, w, h))
}

// setPixel writes one decoded pixel (bytes in requested format: R,G,B at 0..2).
func (fb *framebuffer) setPixel(x, y int, px uint32) {
	b := fb.img.Bounds()
	if x < 0 || y < 0 || x >= b.Dx() || y >= b.Dy() {
		return
	}
	o := fb.img.PixOffset(x, y)
	fb.img.Pix[o] = byte(px)
	fb.img.Pix[o+1] = byte(px >> 8)
	fb.img.Pix[o+2] = byte(px >> 16)
	fb.img.Pix[o+3] = 0xff
}

// fillRect fills an w x h region at (x,y) with a raw pixel value.
func (fb *framebuffer) fillRect(x, y, w, h int, px uint32) {
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			fb.setPixel(x+i, y+j, px)
		}
	}
}

// readPixel reads a single pixel as an unsigned little-endian value of the
// given byte width.
func readPixel(r io.Reader, bypp int) (uint32, error) {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:bypp]); err != nil {
		return 0, err
	}
	var v uint32
	for i := 0; i < bypp; i++ {
		v |= uint32(buf[i]) << (8 * i)
	}
	return v, nil
}

// decodeRaw reads a RAW rectangle (w*h*bypp little-endian pixels) into fb.
func decodeRaw(r io.Reader, fb *framebuffer, x, y, w, h, bypp int) error {
	row := make([]byte, w*bypp)
	for j := 0; j < h; j++ {
		if _, err := io.ReadFull(r, row); err != nil {
			return redact.Error(fmt.Errorf("vnc: read RAW row: %w", err))
		}
		for i := 0; i < w; i++ {
			var v uint32
			for k := 0; k < bypp; k++ {
				v |= uint32(row[i*bypp+k]) << (8 * k)
			}
			fb.setPixel(x+i, y+j, v)
		}
	}
	return nil
}

// decodeCopyRect copies an existing framebuffer region.
func decodeCopyRect(r io.Reader, fb *framebuffer, x, y, w, h int) error {
	var sx, sy uint16
	if err := binary.Read(r, binary.BigEndian, &sx); err != nil {
		return redact.Error(fmt.Errorf("vnc: read CopyRect src x: %w", err))
	}
	if err := binary.Read(r, binary.BigEndian, &sy); err != nil {
		return redact.Error(fmt.Errorf("vnc: read CopyRect src y: %w", err))
	}
	src := fb.img.Bounds()
	for j := 0; j < h; j++ {
		syy := int(sy) + j
		dyy := y + j
		for i := 0; i < w; i++ {
			sxx := int(sx) + i
			if sxx < 0 || syy < 0 || sxx >= src.Dx() || syy >= src.Dy() {
				continue
			}
			so := fb.img.PixOffset(sxx, syy)
			var v uint32
			v = uint32(fb.img.Pix[so]) | uint32(fb.img.Pix[so+1])<<8 | uint32(fb.img.Pix[so+2])<<16
			fb.setPixel(x+i, dyy, v)
		}
	}
	return nil
}

// Hextile subencoding bits (RFC 6143 §7.7.4).
const (
	hextileRaw        = 1
	hextileBgSpec     = 2
	hextileFgSpec     = 4
	hextileAnySubrect = 8
	hextileSubcolored = 16
)

// decodeHextile reads a Hextile rectangle into fb.
func decodeHextile(r io.Reader, fb *framebuffer, x, y, w, h, bypp int) error {
	var bg, fg uint32
	for ty := 0; ty < h; ty += 16 {
		th := 16
		if ty+th > h {
			th = h - ty
		}
		for tx := 0; tx < w; tx += 16 {
			tw := 16
			if tx+tw > w {
				tw = w - tx
			}
			px := x + tx
			py := y + ty
			var sub uint8
			if err := binary.Read(r, binary.BigEndian, &sub); err != nil {
				return redact.Error(fmt.Errorf("vnc: read hextile subencoding: %w", err))
			}
			if sub&hextileRaw != 0 {
				if err := decodeRaw(r, fb, px, py, tw, th, bypp); err != nil {
					return err
				}
				continue
			}
			if sub&hextileBgSpec != 0 {
				v, err := readPixel(r, bypp)
				if err != nil {
					return redact.Error(fmt.Errorf("vnc: read hextile bg: %w", err))
				}
				bg = v
			}
			fb.fillRect(px, py, tw, th, bg)
			if sub&hextileFgSpec != 0 {
				v, err := readPixel(r, bypp)
				if err != nil {
					return redact.Error(fmt.Errorf("vnc: read hextile fg: %w", err))
				}
				fg = v
			}
			if sub&hextileAnySubrect == 0 {
				continue
			}
			var nsub uint8
			if err := binary.Read(r, binary.BigEndian, &nsub); err != nil {
				return redact.Error(fmt.Errorf("vnc: read hextile subrect count: %w", err))
			}
			for s := 0; s < int(nsub); s++ {
				color := fg
				if sub&hextileSubcolored != 0 {
					v, err := readPixel(r, bypp)
					if err != nil {
						return redact.Error(fmt.Errorf("vnc: read hextile subrect colour: %w", err))
					}
					color = v
				}
				var xy [2]byte
				if _, err := io.ReadFull(r, xy[:]); err != nil {
					return redact.Error(fmt.Errorf("vnc: read hextile subrect xy: %w", err))
				}
				sx := int(xy[0] >> 4)
				sy := int(xy[0] & 0x0f)
				sw := int(xy[1]>>4) + 1
				sh := int(xy[1]&0x0f) + 1
				fb.fillRect(px+sx, py+sy, sw, sh, color)
			}
		}
	}
	return nil
}
