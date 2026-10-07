package vnc

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/des"
	"crypto/md5"
	"encoding/binary"
	"image"
	"image/png"
	"io"
	"math/big"
	"net"
	"testing"
	"time"
)

// rfbServer is a scripted fake RFB server for tests.
type rfbServer struct {
	t        *testing.T
	ln       net.Listener
	auth     byte // secNone, secVNCAuth, secAppleDH
	user     string
	pass     string
	width    int
	height   int
	encoding int32 // how to encode the framebuffer update
	pixels   map[[2]int]uint32

	gotPointer chan [5]byte
	gotKeys    chan [8]byte
}

func newRFBServer(t *testing.T, auth byte, enc int32) *rfbServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return &rfbServer{
		t: t, ln: ln, auth: auth, encoding: enc, width: 4, height: 2,
		pixels:     map[[2]int]uint32{},
		gotPointer: make(chan [5]byte, 16),
		gotKeys:    make(chan [8]byte, 32),
	}
}

func (s *rfbServer) addr() string { return s.ln.Addr().String() }
func (s *rfbServer) close()       { _ = s.ln.Close() }

func (s *rfbServer) serve() {
	go func() {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if err := s.handshake(conn); err != nil {
			return
		}
		s.loop(conn)
	}()
}

func (s *rfbServer) handshake(conn net.Conn) error {
	if _, err := conn.Write([]byte("RFB 003.008\n")); err != nil {
		return err
	}
	if _, err := io.ReadFull(conn, make([]byte, 12)); err != nil {
		return err
	}
	switch s.auth {
	case secNone:
		conn.Write([]byte{1, secNone})
		var chosen [1]byte
		io.ReadFull(conn, chosen[:])
		binary.Write(conn, binary.BigEndian, uint32(0)) // security result OK
	case secVNCAuth:
		conn.Write([]byte{1, secVNCAuth})
		var chosen [1]byte
		io.ReadFull(conn, chosen[:])
		challenge := []byte("0123456789abcdef")
		conn.Write(challenge)
		resp := make([]byte, 16)
		if _, err := io.ReadFull(conn, resp); err != nil {
			return err
		}
		// Verify the DES response equals what the client should compute.
		key := desKey(s.pass)
		blk, _ := des.NewCipher(key)
		want := make([]byte, 16)
		blk.Encrypt(want[0:8], challenge[0:8])
		blk.Encrypt(want[8:16], challenge[8:16])
		if !bytes.Equal(resp, want) {
			binary.Write(conn, binary.BigEndian, uint32(1))
			return nil
		}
		binary.Write(conn, binary.BigEndian, uint32(0))
	case secAppleDH:
		conn.Write([]byte{1, secAppleDH})
		var chosen [1]byte
		io.ReadFull(conn, chosen[:])
		if err := s.ardHandshake(conn); err != nil {
			return err
		}
	}
	// ClientInit (shared flag).
	var shared [1]byte
	if _, err := io.ReadFull(conn, shared[:]); err != nil {
		return err
	}
	// ServerInit.
	pf := make([]byte, 16)
	binary.BigEndian.PutUint16(pf[4:], 255)
	binary.BigEndian.PutUint16(pf[6:], 255)
	binary.BigEndian.PutUint16(pf[8:], 255)
	binary.BigEndian.PutUint16(pf[0:], 32)
	pf[2] = 24
	pf[3] = 1
	pf[10] = 0
	pf[11] = 8
	pf[12] = 16
	name := []byte("fake")
	hdr := make([]byte, 24)
	binary.BigEndian.PutUint16(hdr[0:], uint16(s.width))
	binary.BigEndian.PutUint16(hdr[2:], uint16(s.height))
	copy(hdr[4:20], pf)
	binary.BigEndian.PutUint32(hdr[20:], uint32(len(name)))
	if _, err := conn.Write(hdr); err != nil {
		return err
	}
	if _, err := conn.Write(name); err != nil {
		return err
	}
	return nil
}

// ardHandshake performs the server side of the Apple DH scheme and verifies the
// decrypted credentials.
func (s *rfbServer) ardHandshake(conn net.Conn) error {
	const keyLen = 128
	g := big.NewInt(5)
	// Random modulus (any >1 works for the DH algebra).
	modBytes := make([]byte, keyLen)
	modBytes[0] = 0x80
	for i := 1; i < keyLen; i++ {
		modBytes[i] = byte(i*7 + 1)
	}
	m := new(big.Int).SetBytes(modBytes)
	serverPriv := big.NewInt(0x1234567890abcdef)
	serverPub := new(big.Int).Exp(g, serverPriv, m)

	var hdr [4]byte
	binary.BigEndian.PutUint16(hdr[0:], uint16(g.Int64()))
	binary.BigEndian.PutUint16(hdr[2:], keyLen)
	conn.Write(hdr[:])
	conn.Write(padLeft(m.Bytes(), keyLen))
	conn.Write(padLeft(serverPub.Bytes(), keyLen))

	ct := make([]byte, 128)
	if _, err := io.ReadFull(conn, ct); err != nil {
		return err
	}
	clientPubBytes := make([]byte, keyLen)
	if _, err := io.ReadFull(conn, clientPubBytes); err != nil {
		return err
	}
	clientPub := new(big.Int).SetBytes(clientPubBytes)
	shared := new(big.Int).Exp(clientPub, serverPriv, m)
	digest := md5.Sum(shared.Bytes())
	blk, _ := aes.NewCipher(digest[:])
	plain := make([]byte, 128)
	for i := 0; i < 8; i++ {
		blk.Decrypt(plain[i*16:(i+1)*16], ct[i*16:(i+1)*16])
	}
	user := string(bytes.TrimRight(plain[0:64], "\x00"))
	pass := string(bytes.TrimRight(plain[64:128], "\x00"))
	ok := user == s.user && pass == s.pass
	var result uint32
	if !ok {
		result = 1
	}
	binary.Write(conn, binary.BigEndian, result)
	return nil
}

func padLeft(b []byte, n int) []byte {
	if len(b) >= n {
		return b
	}
	out := make([]byte, n)
	copy(out[n-len(b):], b)
	return out
}

// loop consumes client messages, replying to FramebufferUpdateRequest with one
// update and recording pointer/key events.
func (s *rfbServer) loop(conn net.Conn) {
	encodingsRead := false
	for {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		var mtype [1]byte
		if _, err := io.ReadFull(conn, mtype[:]); err != nil {
			return
		}
		switch mtype[0] {
		case msgSetPixelFormat:
			io.ReadFull(conn, make([]byte, 19))
		case msgSetEncodings:
			var hdr [3]byte
			io.ReadFull(conn, hdr[:])
			n := int(binary.BigEndian.Uint16(hdr[1:3]))
			io.ReadFull(conn, make([]byte, 4*n))
			encodingsRead = true
			_ = encodingsRead
		case msgFramebufferUpdateRequest:
			io.ReadFull(conn, make([]byte, 9))
			s.sendUpdate(conn)
		case msgPointerEvent:
			var e [5]byte
			io.ReadFull(conn, e[:])
			s.gotPointer <- e
		case msgKeyEvent:
			var e [7]byte
			io.ReadFull(conn, e[:])
			var full [8]byte
			full[0] = msgKeyEvent
			copy(full[1:], e[:])
			s.gotKeys <- full
		case msgClientCutText:
			var l [7]byte
			io.ReadFull(conn, l[:])
			n := binary.BigEndian.Uint32(l[3:7])
			io.ReadFull(conn, make([]byte, n))
		default:
			return
		}
	}
}

func (s *rfbServer) sendUpdate(conn net.Conn) {
	switch s.encoding {
	case encRaw:
		s.sendRawUpdate(conn)
	case encHextile:
		s.sendHextileUpdate(conn)
	}
}

func (s *rfbServer) sendRawUpdate(conn net.Conn) {
	var buf bytes.Buffer
	buf.WriteByte(s2cFramebufferUpdate)
	buf.WriteByte(0)
	binary.Write(&buf, binary.BigEndian, uint16(1))
	binary.Write(&buf, binary.BigEndian, uint16(0)) // x
	binary.Write(&buf, binary.BigEndian, uint16(0)) // y
	binary.Write(&buf, binary.BigEndian, uint16(s.width))
	binary.Write(&buf, binary.BigEndian, uint16(s.height))
	binary.Write(&buf, binary.BigEndian, encRaw)
	for y := 0; y < s.height; y++ {
		for x := 0; x < s.width; x++ {
			px, ok := s.pixels[[2]int{x, y}]
			if !ok {
				px = 0
			}
			buf.Write(pixelLE(px, 4))
		}
	}
	conn.Write(buf.Bytes())
}

// sendHextileUpdate sends a single hextile tile covering the whole (<=16px) screen
// with a background colour and one coloured subrect.
func (s *rfbServer) sendHextileUpdate(conn net.Conn) {
	// bg = 0x001122, fg subrect covers (1,0)-(2,1) with 0x334455.
	bg := uint32(0x112233)
	fgc := uint32(0x445566)
	var body bytes.Buffer
	// subencoding: bgSpec|fgSpec|anySubrect|Subcolored
	sub := byte(hextileBgSpec | hextileFgSpec | hextileAnySubrect | hextileSubcolored)
	body.WriteByte(sub)
	body.Write(pixelLE(bg, 4))
	body.Write(pixelLE(fgc, 4))
	body.WriteByte(1) // one subrect
	body.Write(pixelLE(fgc, 4))
	// subrect x=1,y=0,w=2,h=2 => xy byte0 = (1<<4)|0 = 0x10, byte1 = ((2-1)<<4)|(2-1)=0x11
	body.WriteByte(0x10)
	body.WriteByte(0x11)

	var buf bytes.Buffer
	buf.WriteByte(s2cFramebufferUpdate)
	buf.WriteByte(0)
	binary.Write(&buf, binary.BigEndian, uint16(1))
	binary.Write(&buf, binary.BigEndian, uint16(0))
	binary.Write(&buf, binary.BigEndian, uint16(0))
	binary.Write(&buf, binary.BigEndian, uint16(s.width))
	binary.Write(&buf, binary.BigEndian, uint16(s.height))
	binary.Write(&buf, binary.BigEndian, encHextile)
	buf.Write(body.Bytes())
	conn.Write(buf.Bytes())
}

func pixelLE(px uint32, bypp int) []byte {
	b := make([]byte, bypp)
	for i := 0; i < bypp; i++ {
		b[i] = byte(px >> (8 * i))
	}
	return b
}

func decodePNG(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("png decode: %v", err)
	}
	return img
}

func TestDialNoneRaw(t *testing.T) {
	srv := newRFBServer(t, secNone, encRaw)
	defer srv.close()
	srv.pixels[[2]int{0, 0}] = 0x336699 // R=0x99 G=0x66 B=0x33
	srv.pixels[[2]int{3, 1}] = 0xff0000 // B=0xff
	srv.serve()

	c, err := Dial(context.Background(), srv.addr(), Options{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if c.Width() != 4 || c.Height() != 2 {
		t.Fatalf("dims %dx%d", c.Width(), c.Height())
	}
	data, w, h, err := c.Screenshot(context.Background())
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if w != 4 || h != 2 {
		t.Fatalf("shot dims %dx%d", w, h)
	}
	img := decodePNG(t, data)
	r, g, b, _ := img.At(0, 0).RGBA()
	if r>>8 != 0x99 || g>>8 != 0x66 || b>>8 != 0x33 {
		t.Errorf("pixel(0,0) = %x,%x,%x want 99,66,33", r>>8, g>>8, b>>8)
	}
	r, g, b, _ = img.At(3, 1).RGBA()
	if b>>8 != 0xff || r>>8 != 0 || g>>8 != 0 {
		t.Errorf("pixel(3,1) = %x,%x,%x want 0,0,ff", r>>8, g>>8, b>>8)
	}
}

func TestHextileDecode(t *testing.T) {
	srv := newRFBServer(t, secNone, encHextile)
	defer srv.close()
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
	// Background 0x112233 (R=0x33,G=0x22,B=0x11) at (0,0).
	r, g, b, _ := img.At(0, 0).RGBA()
	if r>>8 != 0x33 || g>>8 != 0x22 || b>>8 != 0x11 {
		t.Errorf("bg pixel = %x,%x,%x want 33,22,11", r>>8, g>>8, b>>8)
	}
	// Subrect fg 0x445566 (R=0x66,G=0x55,B=0x44) at (1,0)..(2,1).
	r, g, b, _ = img.At(2, 1).RGBA()
	if r>>8 != 0x66 || g>>8 != 0x55 || b>>8 != 0x44 {
		t.Errorf("fg pixel = %x,%x,%x want 66,55,44", r>>8, g>>8, b>>8)
	}
}

func TestVNCDESAuth(t *testing.T) {
	srv := newRFBServer(t, secVNCAuth, encRaw)
	defer srv.close()
	srv.pass = "secret"
	srv.serve()
	c, err := Dial(context.Background(), srv.addr(), Options{
		Credentials: Credentials{Password: "secret"}, Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial with VNC auth: %v", err)
	}
	c.Close()
}

func TestVNCDESAuthWrongPassword(t *testing.T) {
	srv := newRFBServer(t, secVNCAuth, encRaw)
	defer srv.close()
	srv.pass = "secret"
	srv.serve()
	if _, err := Dial(context.Background(), srv.addr(), Options{
		Credentials: Credentials{Password: "wrong"}, Timeout: 3 * time.Second,
	}); err == nil {
		t.Fatal("expected auth failure with wrong password")
	}
}

func TestArdAuth(t *testing.T) {
	srv := newRFBServer(t, secAppleDH, encRaw)
	defer srv.close()
	srv.user = "alice"
	srv.pass = "hunter2"
	srv.serve()
	c, err := Dial(context.Background(), srv.addr(), Options{
		Credentials: Credentials{Username: "alice", Password: "hunter2"}, Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial with ARD auth: %v", err)
	}
	c.Close()
}

func TestInputEvents(t *testing.T) {
	srv := newRFBServer(t, secNone, encRaw)
	defer srv.close()
	srv.serve()
	c, err := Dial(context.Background(), srv.addr(), Options{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	// Prime a screenshot so the server loop is live, then send input.
	if _, _, _, err := c.Screenshot(context.Background()); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if err := c.ClickAt(3, 1, "left"); err != nil {
		t.Fatalf("click at: %v", err)
	}
	// ClickAt yields: move (mask 0) -> press (mask 1) -> release (mask 0).
	readPtr := func() [5]byte {
		select {
		case p := <-srv.gotPointer:
			return p
		case <-time.After(2 * time.Second):
			t.Fatal("no pointer event received")
			return [5]byte{}
		}
	}
	if p := readPtr(); p[0] != 0 {
		t.Errorf("expected move mask 0, got %d", p[0])
	}
	p := readPtr()
	if p[0]&1 == 0 || binary.BigEndian.Uint16(p[1:3]) != 3 || binary.BigEndian.Uint16(p[3:5]) != 1 {
		t.Errorf("pointer press = mask %d x %d y %d", p[0], binary.BigEndian.Uint16(p[1:3]), binary.BigEndian.Uint16(p[3:5]))
	}
	if p := readPtr(); p[0] != 0 {
		t.Errorf("expected release mask 0, got %d", p[0])
	}

	if err := c.KeyName("enter"); err != nil {
		t.Fatalf("key: %v", err)
	}
	select {
	case k := <-srv.gotKeys:
		if k[1] != 1 { // down flag
			t.Errorf("key down flag = %d", k[1])
		}
		ks := binary.BigEndian.Uint32(k[4:8])
		if ks != keyReturn {
			t.Errorf("keysym = %x want %x", ks, keyReturn)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no key event received")
	}
}

func TestTypeText(t *testing.T) {
	srv := newRFBServer(t, secNone, encRaw)
	defer srv.close()
	srv.serve()
	c, err := Dial(context.Background(), srv.addr(), Options{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, _, _, err := c.Screenshot(context.Background()); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if err := c.Type("a"); err != nil {
		t.Fatalf("type: %v", err)
	}
	select {
	case k := <-srv.gotKeys:
		if binary.BigEndian.Uint32(k[4:8]) != 'a' {
			t.Errorf("typed keysym = %x", binary.BigEndian.Uint32(k[4:8]))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no key event")
	}
}

func TestKeySymNames(t *testing.T) {
	cases := map[string]uint32{
		"enter": keyReturn, "Return": keyReturn, "esc": keyEscape,
		"tab": keyTab, "space": 0x20, "a": 'a', "Z": 'Z',
		"up": keyUp, "f1": keyFBase, "f12": keyFBase + 11,
	}
	for name, want := range cases {
		got, ok := KeySym(name)
		if !ok || got != want {
			t.Errorf("KeySym(%q) = %x,%v want %x", name, got, ok, want)
		}
	}
	if _, ok := KeySym("nope"); ok {
		t.Error("KeySym(nope) should be unknown")
	}
	if _, ok := KeySym(""); ok {
		t.Error("KeySym(empty) should be unknown")
	}
}

func TestDesKeyBitReversal(t *testing.T) {
	// "password" -> per-byte bit-reversed key.
	got := desKey("password")
	// Verify against a manual reversal of the first byte 'p' (0x70).
	want0 := byte(0)
	for bit := 0; bit < 8; bit++ {
		if 0x70&(1<<bit) != 0 {
			want0 |= 1 << (7 - bit)
		}
	}
	if got[0] != want0 {
		t.Errorf("desKey[0] = %x want %x", got[0], want0)
	}
}

func TestSetEncodingsAndPixelFormatWire(t *testing.T) {
	var buf bytes.Buffer
	if err := setEncodings(&buf, []int32{encHextile, encRaw}); err != nil {
		t.Fatal(err)
	}
	if buf.Bytes()[0] != msgSetEncodings {
		t.Errorf("encodings msg type = %d", buf.Bytes()[0])
	}
	if n := binary.BigEndian.Uint16(buf.Bytes()[2:4]); n != 2 {
		t.Errorf("encodings count = %d", n)
	}
	buf.Reset()
	if err := setPixelFormat(&buf); err != nil {
		t.Fatal(err)
	}
	if buf.Bytes()[0] != msgSetPixelFormat || buf.Bytes()[4] != 32 {
		t.Errorf("pixel format header wrong: %v", buf.Bytes()[:6])
	}
}

func TestUnsupportedEncodingErrors(t *testing.T) {
	srv := newRFBServer(t, secNone, encRaw)
	defer srv.close()
	// Patch the server to send an unknown encoding.
	go func() {
		conn, err := srv.ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if err := srv.handshake(conn); err != nil {
			return
		}
		// Read SetPixelFormat + SetEncodings + UpdateRequest, then reply with a
		// bogus encoding.
		buf := make([]byte, 1)
		io.ReadFull(conn, buf)
		io.ReadFull(conn, make([]byte, 19))
		io.ReadFull(conn, buf)
		var hdr [3]byte
		io.ReadFull(conn, hdr[:])
		io.ReadFull(conn, make([]byte, 4*int(binary.BigEndian.Uint16(hdr[1:3]))))
		io.ReadFull(conn, buf)
		io.ReadFull(conn, make([]byte, 9))
		var out bytes.Buffer
		out.WriteByte(s2cFramebufferUpdate)
		out.WriteByte(0)
		binary.Write(&out, binary.BigEndian, uint16(1))
		binary.Write(&out, binary.BigEndian, uint16(0))
		binary.Write(&out, binary.BigEndian, uint16(0))
		binary.Write(&out, binary.BigEndian, uint16(4))
		binary.Write(&out, binary.BigEndian, uint16(2))
		binary.Write(&out, binary.BigEndian, int32(999))
		conn.Write(out.Bytes())
		time.Sleep(500 * time.Millisecond)
	}()
	c, err := Dial(context.Background(), srv.addr(), Options{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, _, _, err := c.Screenshot(context.Background()); err == nil {
		t.Fatal("expected error for unsupported encoding")
	}
}

func TestDialRefused(t *testing.T) {
	if _, err := Dial(context.Background(), "127.0.0.1:1", Options{Timeout: 500 * time.Millisecond}); err == nil {
		t.Fatal("expected dial error")
	}
}
