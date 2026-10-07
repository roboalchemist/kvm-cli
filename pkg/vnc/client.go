package vnc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"image/png"
	"io"
	"net"
	"time"

	"github.com/roboalchemist/kvm-cli/pkg/redact"
)

// Credentials carries the optional VNC/ARD username and password.
type Credentials struct {
	Username string
	Password string
}

// Options configures a Client.
type Options struct {
	// Username/Password authenticate the connection. Password is required by
	// VNC-DES auth; both may be needed for Apple (ARD) auth.
	Credentials Credentials
	// Timeout bounds the dial and each read. Non-positive uses 30s.
	Timeout time.Duration
	// Debug logs wire activity to stderr.
	Debug bool
}

// Client is a connected RFB session.
type Client struct {
	conn net.Conn
	r    *bufio.Reader
	fb   *framebuffer

	width, height int
	name          string
	encodings     []int32

	timeout time.Duration
	debug   bool

	lastX, lastY int
}

// Dial connects to a VNC server at addr ("host:port"), performs the RFB
// handshake (including VNC and Apple-ARD authentication), negotiates a
// 32bpp little-endian pixel format, and requests a first full framebuffer.
func Dial(ctx context.Context, addr string, opts Options) (*Client, error) {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, redact.Error(fmt.Errorf("vnc: dial %s: %w", addr, err))
	}
	c := &Client{
		conn:      conn,
		r:         bufio.NewReader(conn),
		timeout:   timeout,
		debug:     opts.Debug,
		encodings: []int32{encDesktopSize, encHextile, encCopyRect, encRaw},
	}
	name, w, h, err := readHandshake(conn, opts.Credentials)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	c.name = name
	c.width, c.height = w, h
	c.fb = newFramebuffer(w, h)

	if err := setPixelFormat(conn); err != nil {
		_ = conn.Close()
		return nil, redact.Error(fmt.Errorf("vnc: set pixel format: %w", err))
	}
	if err := setEncodings(conn, c.encodings); err != nil {
		_ = conn.Close()
		return nil, redact.Error(fmt.Errorf("vnc: set encodings: %w", err))
	}
	return c, nil
}

// Width returns the negotiated framebuffer width.
func (c *Client) Width() int { return c.width }

// Height returns the negotiated framebuffer height.
func (c *Client) Height() int { return c.height }

// DesktopName returns the server's desktop name.
func (c *Client) DesktopName() string { return c.name }

// Screenshot requests a full framebuffer update and returns the frame as PNG
// bytes plus its dimensions.
func (c *Client) Screenshot(ctx context.Context) ([]byte, int, int, error) {
	if err := framebufferUpdateRequest(c.conn, false, 0, 0, uint16(c.width), uint16(c.height)); err != nil {
		return nil, 0, 0, redact.Error(fmt.Errorf("vnc: request framebuffer: %w", err))
	}
	if err := c.readFramebufferUpdate(ctx); err != nil {
		return nil, 0, 0, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, c.fb.img); err != nil {
		return nil, 0, 0, fmt.Errorf("vnc: encode png: %w", err)
	}
	return buf.Bytes(), c.width, c.height, nil
}

// readFramebufferUpdate reads server messages until one FramebufferUpdate has
// been fully applied.
func (c *Client) readFramebufferUpdate(ctx context.Context) error {
	if c.timeout > 0 && ctx != nil {
		_ = c.conn.SetReadDeadline(time.Now().Add(c.timeout))
	}
	for {
		msg, err := c.r.ReadByte()
		if err != nil {
			return redact.Error(fmt.Errorf("vnc: read message: %w", err))
		}
		switch msg {
		case s2cFramebufferUpdate:
			done, err := c.readUpdate()
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		case s2cSetColourMap:
			var hdr [5]byte
			if err := c.readFull(hdr[:]); err != nil {
				return err
			}
			n := int(binary.BigEndian.Uint16(hdr[3:5]))
			skip := make([]byte, n*6)
			if err := c.readFull(skip); err != nil {
				return err
			}
		case s2cBell:
			// no payload
		case s2cServerCutText:
			var hdr [7]byte
			if err := c.readFull(hdr[:]); err != nil {
				return err
			}
			n := binary.BigEndian.Uint32(hdr[3:7])
			if err := c.readFull(make([]byte, n)); err != nil {
				return err
			}
		default:
			return fmt.Errorf("vnc: unexpected server message type %d", msg)
		}
	}
}

// readUpdate reads a FramebufferUpdate message body and applies every rectangle.
func (c *Client) readUpdate() (bool, error) {
	var hdr [3]byte // padding + nrects
	if err := c.readFull(hdr[:]); err != nil {
		return false, err
	}
	nrects := int(binary.BigEndian.Uint16(hdr[1:3]))
	for i := 0; i < nrects; i++ {
		var rh [12]byte
		if err := c.readFull(rh[:]); err != nil {
			return false, err
		}
		x := int(binary.BigEndian.Uint16(rh[0:2]))
		y := int(binary.BigEndian.Uint16(rh[2:4]))
		w := int(binary.BigEndian.Uint16(rh[4:6]))
		h := int(binary.BigEndian.Uint16(rh[6:8]))
		enc := int32(binary.BigEndian.Uint32(rh[8:12]))
		bypp := requestedFormat().bypp()
		switch enc {
		case encRaw:
			if err := decodeRaw(c.r, c.fb, x, y, w, h, bypp); err != nil {
				return false, err
			}
		case encCopyRect:
			if err := decodeCopyRect(c.r, c.fb, x, y, w, h); err != nil {
				return false, err
			}
		case encHextile:
			if err := decodeHextile(c.r, c.fb, x, y, w, h, bypp); err != nil {
				return false, err
			}
		case encDesktopSize:
			c.width, c.height = w, h
			c.fb.resize(w, h)
		case encCursorPseudo:
			length := w*h*bypp + ((w+7)/8)*h
			if err := c.readFull(make([]byte, length)); err != nil {
				return false, err
			}
		default:
			return false, fmt.Errorf("vnc: unsupported encoding %d", enc)
		}
	}
	return true, nil
}

func (c *Client) readFull(b []byte) error {
	if _, err := io.ReadFull(c.r, b); err != nil {
		return redact.Error(fmt.Errorf("vnc: read: %w", err))
	}
	return nil
}

// ---- input -----------------------------------------------------------------

// PointerEvent moves the pointer to (x,y) and sets the button mask.
func (c *Client) PointerEvent(x, y int, buttons uint8) error {
	return pointerEvent(c.conn, uint16(x), uint16(y), buttons)
}

// MoveMouse moves the pointer to (x,y) without pressing any button.
func (c *Client) MoveMouse(x, y int) error {
	c.lastX, c.lastY = x, y
	return c.PointerEvent(x, y, 0)
}

// buttonBit maps a button name to its VNC mask bit.
func buttonBit(button string) (uint8, bool) {
	switch lowerASCII(button) {
	case "left", "1":
		return 1, true
	case "middle", "2":
		return 2, true
	case "right", "3":
		return 4, true
	case "up", "4":
		return 8, true
	case "down", "5":
		return 16, true
	default:
		return 0, false
	}
}

// Click performs a press+release of the named button at the current position.
func (c *Client) Click(button string) error {
	bit, ok := buttonBit(button)
	if !ok {
		return fmt.Errorf("vnc: unknown mouse button %q", button)
	}
	// The server tracks button state from the mask; release by sending mask 0.
	if err := pointerEvent(c.conn, uint16(c.lastX), uint16(c.lastY), bit); err != nil {
		return err
	}
	time.Sleep(40 * time.Millisecond)
	return pointerEvent(c.conn, uint16(c.lastX), uint16(c.lastY), 0)
}

// MouseDown presses a button at the current position.
func (c *Client) MouseDown(button string) error {
	bit, ok := buttonBit(button)
	if !ok {
		return fmt.Errorf("vnc: unknown mouse button %q", button)
	}
	return pointerEvent(c.conn, uint16(c.lastX), uint16(c.lastY), bit)
}

// MouseUp releases all buttons at the current position.
func (c *Client) MouseUp(button string) error {
	if _, ok := buttonBit(button); !ok {
		return fmt.Errorf("vnc: unknown mouse button %q", button)
	}
	return pointerEvent(c.conn, uint16(c.lastX), uint16(c.lastY), 0)
}

// lastX/lastY are tracked via MoveMouse; Click/Down/Up reuse the last position.
func (c *Client) setLast(x, y int) { c.lastX, c.lastY = x, y }

// ClickAt moves to (x,y) then clicks the named button.
func (c *Client) ClickAt(x, y int, button string) error {
	c.setLast(x, y)
	if err := c.MoveMouse(x, y); err != nil {
		return err
	}
	return c.Click(button)
}

// Wheel scrolls by delta (dy>0 scrolls up) at the current position.
func (c *Client) Wheel(dx, dy int) error {
	steps := dy
	bit := uint8(8) // button 4 = up
	if dy < 0 {
		steps = -dy
		bit = 16 // button 5 = down
	}
	for i := 0; i < steps; i++ {
		if err := pointerEvent(c.conn, uint16(c.lastX), uint16(c.lastY), bit); err != nil {
			return err
		}
		if err := pointerEvent(c.conn, uint16(c.lastX), uint16(c.lastY), 0); err != nil {
			return err
		}
	}
	return nil
}

// Key sends a key press+release for an X11 keysym.
func (c *Client) Key(keysym uint32) error {
	if err := keyEvent(c.conn, true, keysym); err != nil {
		return err
	}
	return keyEvent(c.conn, false, keysym)
}

// KeyDown presses a key (held until KeyUp).
func (c *Client) KeyDown(keysym uint32) error { return keyEvent(c.conn, true, keysym) }

// KeyUp releases a key.
func (c *Client) KeyUp(keysym uint32) error { return keyEvent(c.conn, false, keysym) }

// KeyName resolves a friendly key name and sends press+release.
func (c *Client) KeyName(name string) error {
	ks, ok := KeySym(name)
	if !ok {
		return fmt.Errorf("vnc: unknown key %q", name)
	}
	return c.Key(ks)
}

// Combo presses modifiers, taps the final key, then releases modifiers.
func (c *Client) Combo(names []string) error {
	keysyms := make([]uint32, 0, len(names))
	for _, n := range names {
		ks, ok := KeySym(n)
		if !ok {
			return fmt.Errorf("vnc: unknown key %q", n)
		}
		keysyms = append(keysyms, ks)
	}
	if len(keysyms) == 0 {
		return fmt.Errorf("vnc: empty key combo")
	}
	for _, ks := range keysyms[:len(keysyms)-1] {
		if err := c.KeyDown(ks); err != nil {
			return err
		}
	}
	last := keysyms[len(keysyms)-1]
	if err := c.Key(last); err != nil {
		return err
	}
	for i := len(keysyms) - 2; i >= 0; i-- {
		if err := c.KeyUp(keysyms[i]); err != nil {
			return err
		}
	}
	return nil
}

// Type sends each rune of text as a keysym press+release.
func (c *Client) Type(text string) error {
	for _, ks := range textKeysyms(text) {
		if err := c.Key(ks); err != nil {
			return err
		}
	}
	return nil
}

// Close terminates the session.
func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}
