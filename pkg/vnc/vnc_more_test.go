package vnc

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// scriptServer accepts one connection, writes a canned message stream after a
// standard None-auth 3.8 handshake, and drains any client messages in the
// background so writes never block.
type scriptServer struct {
	t      *testing.T
	ln     net.Listener
	width  int
	height int
	stream []byte // messages written after ServerInit
	got    []byte // client bytes captured after handshake
}

func newScriptServer(t *testing.T, stream []byte) *scriptServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return &scriptServer{t: t, ln: ln, width: 4, height: 2, stream: stream}
}

func (s *scriptServer) addr() string { return s.ln.Addr().String() }

func (s *scriptServer) serve() {
	go func() {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte("RFB 003.008\n"))
		io.ReadFull(conn, make([]byte, 12))
		conn.Write([]byte{1, secNone})
		var chosen [1]byte
		io.ReadFull(conn, chosen[:])
		binary.Write(conn, binary.BigEndian, uint32(0))
		var shared [1]byte
		io.ReadFull(conn, shared[:])
		// ServerInit.
		hdr := make([]byte, 24)
		binary.BigEndian.PutUint16(hdr[0:], uint16(s.width))
		binary.BigEndian.PutUint16(hdr[2:], uint16(s.height))
		hdr[4] = 32
		hdr[5] = 24
		hdr[6] = 0
		hdr[7] = 1
		binary.BigEndian.PutUint16(hdr[8:], 255)
		binary.BigEndian.PutUint16(hdr[10:], 255)
		binary.BigEndian.PutUint16(hdr[12:], 255)
		hdr[14] = 0
		hdr[15] = 8
		hdr[16] = 16
		binary.BigEndian.PutUint32(hdr[20:], 4)
		conn.Write(hdr)
		conn.Write([]byte("fake"))
		// Drain client setup + requests.
		go io.Copy(io.Discard, conn)
		conn.Write(s.stream)
		time.Sleep(700 * time.Millisecond)
	}()
}

func updateHeader(nrects int) []byte {
	b := []byte{s2cFramebufferUpdate, 0, 0, 0}
	binary.BigEndian.PutUint16(b[2:], uint16(nrects))
	return b
}

func testUpdateRect(x, y, w, h uint16, enc int32) []byte {
	b := make([]byte, 12)
	binary.BigEndian.PutUint16(b[0:], x)
	binary.BigEndian.PutUint16(b[2:], y)
	binary.BigEndian.PutUint16(b[4:], w)
	binary.BigEndian.PutUint16(b[6:], h)
	binary.BigEndian.PutUint32(b[8:], uint32(enc))
	return b
}

func TestScreenshotSkipsMiscMessages(t *testing.T) {
	// Bell, SetColourMap (0 entries), ServerCutText, then one RAW update.
	var st bytes.Buffer
	st.WriteByte(s2cBell)
	st.WriteByte(s2cSetColourMap)
	st.Write([]byte{0, 0, 0, 0}) // pad + first + num(=0)
	st.WriteByte(s2cServerCutText)
	st.Write([]byte{0, 0, 0, 0, 0, 0, 0})
	st.Write(updateHeader(1))
	st.Write(testUpdateRect(0, 0, 4, 2, encRaw))
	st.Write(bytes.Repeat([]byte{0}, 4*2*4))
	srv := newScriptServer(t, st.Bytes())
	defer srv.ln.Close()
	srv.serve()

	c, err := Dial(context.Background(), srv.addr(), Options{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, w, h, err := c.Screenshot(context.Background()); err != nil || w != 4 || h != 2 {
		t.Fatalf("screenshot: %dx%d err %v", w, h, err)
	}
}

func TestDesktopSizeAndCursorPseudo(t *testing.T) {
	var st bytes.Buffer
	// DesktopSize pseudo (resize to 8x6) then a Cursor pseudo, then a RAW update.
	st.Write(updateHeader(3))
	st.Write(testUpdateRect(0, 0, 8, 6, encDesktopSize))
	// Cursor: 2x2 image + mask => 2*2*4 + ((2+7)/8)*2 = 16 + 2 = 18 bytes.
	st.Write(testUpdateRect(0, 0, 2, 2, encCursorPseudo))
	st.Write(bytes.Repeat([]byte{1}, 18))
	st.Write(testUpdateRect(0, 0, 8, 6, encRaw))
	st.Write(bytes.Repeat([]byte{0x44}, 8*6*4))
	srv := newScriptServer(t, st.Bytes())
	defer srv.ln.Close()
	srv.serve()

	c, err := Dial(context.Background(), srv.addr(), Options{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	data, w, h, err := c.Screenshot(context.Background())
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if w != 8 || h != 6 {
		t.Fatalf("after DesktopSize dims = %dx%d want 8x6", w, h)
	}
	img := decodePNG(t, data)
	if img.Bounds().Dx() != 8 || img.Bounds().Dy() != 6 {
		t.Fatalf("png dims %v", img.Bounds())
	}
}

func TestCopyRectDecode(t *testing.T) {
	var st bytes.Buffer
	// First a RAW fill of the whole 4x2 with red, then CopyRect (0,0)->(2,0) sz (2,2).
	st.Write(updateHeader(2))
	st.Write(testUpdateRect(0, 0, 4, 2, encRaw))
	for i := 0; i < 4*2; i++ {
		st.Write(pixelLE(0x0000ff, 4)) // R=0xff
	}
	st.Write(testUpdateRect(2, 0, 2, 2, encCopyRect))
	st.Write([]byte{0, 0, 0, 0}) // src x=0,y=0
	srv := newScriptServer(t, st.Bytes())
	defer srv.ln.Close()
	srv.serve()

	c, err := Dial(context.Background(), srv.addr(), Options{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	data, _, _, err := c.Screenshot(context.Background())
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	img := decodePNG(t, data)
	r, _, _, _ := img.At(3, 1).RGBA()
	if r>>8 != 0xff {
		t.Errorf("copied pixel R = %x want ff", r>>8)
	}
}

func TestInputHelpers(t *testing.T) {
	srv := newRFBServer(t, secNone, encRaw)
	defer srv.close()
	srv.serve()
	c, err := Dial(context.Background(), srv.addr(), Options{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if c.DesktopName() != "fake" {
		t.Errorf("desktop name = %q", c.DesktopName())
	}
	if _, _, _, err := c.Screenshot(context.Background()); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	drain := func() {
		for {
			select {
			case <-srv.gotPointer:
			case <-srv.gotKeys:
			default:
				return
			}
		}
	}
	// Mouse helpers.
	if err := c.MouseDown("right"); err != nil {
		t.Fatalf("mousedown: %v", err)
	}
	if err := c.MouseUp("right"); err != nil {
		t.Fatalf("mouseup: %v", err)
	}
	if err := c.MouseDown("bogus"); err == nil {
		t.Error("mousedown bogus should error")
	}
	if err := c.MouseUp("bogus"); err == nil {
		t.Error("mouseup bogus should error")
	}
	if err := c.Wheel(0, 2); err != nil {
		t.Fatalf("wheel up: %v", err)
	}
	if err := c.Wheel(0, -1); err != nil {
		t.Fatalf("wheel down: %v", err)
	}
	// Key helpers.
	if err := c.KeyDown(keyShiftL); err != nil {
		t.Fatalf("keydown: %v", err)
	}
	if err := c.KeyUp(keyShiftL); err != nil {
		t.Fatalf("keyup: %v", err)
	}
	if err := c.Combo([]string{"ctrl", "alt", "delete"}); err != nil {
		t.Fatalf("combo: %v", err)
	}
	if err := c.Combo(nil); err == nil {
		t.Error("empty combo should error")
	}
	if err := c.Combo([]string{"notakey"}); err == nil {
		t.Error("combo with unknown key should error")
	}
	if err := c.KeyName("notakey"); err == nil {
		t.Error("KeyName unknown should error")
	}
	if err := c.Type("hi\n"); err != nil {
		t.Fatalf("type: %v", err)
	}
	drain()
}

func TestImageOpsUnit(t *testing.T) {
	fb := newFramebuffer(0, 0) // clamped to 1x1
	if fb.img.Bounds().Dx() != 1 {
		t.Errorf("clamped fb = %v", fb.img.Bounds())
	}
	fb = newFramebuffer(4, 4)
	fb.resize(0, 0) // ignored
	if fb.img.Bounds().Dx() != 4 {
		t.Error("resize to 0 should be ignored")
	}
	fb.resize(6, 6)
	if fb.img.Bounds().Dx() != 6 {
		t.Error("resize should apply")
	}
	fb.setPixel(-1, 0, 0) // out of bounds, ignored
	fb.setPixel(0, 0, 0x010203)
	if !bytes.Equal(fb.img.Pix[fb.img.PixOffset(0, 0):fb.img.PixOffset(0, 0)+3], []byte{3, 2, 1}) {
		t.Errorf("setPixel bytes = %v", fb.img.Pix[:3])
	}
	fb.fillRect(1, 1, 2, 2, 0xff0000)
	// readPixel round-trip.
	p, err := readPixel(bytes.NewReader([]byte{0x11, 0x22, 0x33, 0x44}), 4)
	if err != nil || p != 0x44332211 {
		t.Errorf("readPixel = %x err %v", p, err)
	}
	if _, err := readPixel(bytes.NewReader([]byte{0x01}), 4); err == nil {
		t.Error("readPixel short read should error")
	}
	tr := textKeysyms("a\n\tb")
	if len(tr) != 4 || tr[1] != keyReturn || tr[2] != keyTab {
		t.Errorf("textKeysyms = %v", tr)
	}
}

func TestSecurity33None(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte("RFB 003.003\n"))
		io.ReadFull(conn, make([]byte, 12))
		binary.Write(conn, binary.BigEndian, uint32(secNone)) // no selection byte
		var shared [1]byte
		io.ReadFull(conn, shared[:])
		hdr := make([]byte, 24)
		binary.BigEndian.PutUint16(hdr[0:], 4)
		binary.BigEndian.PutUint16(hdr[2:], 2)
		hdr[4] = 32
		hdr[5] = 24
		hdr[7] = 1
		binary.BigEndian.PutUint32(hdr[20:], 4)
		conn.Write(hdr)
		conn.Write([]byte("old3"))
		time.Sleep(300 * time.Millisecond)
	}()
	c, err := Dial(context.Background(), ln.Addr().String(), Options{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("dial 3.3: %v", err)
	}
	defer c.Close()
	if c.Width() != 4 || c.DesktopName() != "old3" {
		t.Errorf("3.3 init = %dx%d %q", c.Width(), c.Height(), c.DesktopName())
	}
}

func TestConnFailed(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte("RFB 003.008\n"))
		io.ReadFull(conn, make([]byte, 12))
		conn.Write([]byte{0}) // zero security types => failure follows
		msg := []byte("no way")
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(len(msg)))
		conn.Write(b)
		conn.Write(msg)
		time.Sleep(300 * time.Millisecond)
	}()
	if _, err := Dial(context.Background(), ln.Addr().String(), Options{Timeout: 3 * time.Second}); err == nil {
		t.Fatal("expected connection failure error")
	}
}

func TestUnsupportedSecurity(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte("RFB 003.008\n"))
		io.ReadFull(conn, make([]byte, 12))
		conn.Write([]byte{1, 99}) // only an unsupported type
		time.Sleep(300 * time.Millisecond)
	}()
	if _, err := Dial(context.Background(), ln.Addr().String(), Options{Timeout: 3 * time.Second}); err == nil {
		t.Fatal("expected unsupported security error")
	}
}

func TestScreenshotReadDeadline(t *testing.T) {
	// Server completes the handshake but never answers the update request.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte("RFB 003.008\n"))
		io.ReadFull(conn, make([]byte, 12))
		conn.Write([]byte{1, secNone})
		var chosen [1]byte
		io.ReadFull(conn, chosen[:])
		binary.Write(conn, binary.BigEndian, uint32(0))
		io.ReadFull(conn, chosen[:])
		hdr := make([]byte, 24)
		binary.BigEndian.PutUint16(hdr[0:], 4)
		binary.BigEndian.PutUint16(hdr[2:], 2)
		binary.BigEndian.PutUint32(hdr[20:], 4)
		conn.Write(hdr)
		conn.Write([]byte("fake"))
		time.Sleep(2 * time.Second)
	}()
	c, err := Dial(context.Background(), ln.Addr().String(), Options{Timeout: 400 * time.Millisecond})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, _, _, err := c.Screenshot(context.Background()); err == nil {
		t.Fatal("expected read deadline error")
	}
}

func TestVNC33ConnFailed(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte("RFB 003.003\n"))
		io.ReadFull(conn, make([]byte, 12))
		binary.Write(conn, binary.BigEndian, uint32(0)) // failure
		msg := []byte("nope")
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(len(msg)))
		conn.Write(b)
		conn.Write(msg)
		time.Sleep(300 * time.Millisecond)
	}()
	if _, err := Dial(context.Background(), ln.Addr().String(), Options{Timeout: 3 * time.Second}); err == nil {
		t.Fatal("expected 3.3 connection failure")
	}
}

func TestVNCDESNoPassword(t *testing.T) {
	srv := newRFBServer(t, secVNCAuth, encRaw)
	defer srv.close()
	srv.pass = "x"
	srv.serve()
	if _, err := Dial(context.Background(), srv.addr(), Options{Timeout: 3 * time.Second}); err == nil {
		t.Fatal("expected error when password is required but absent")
	}
}
