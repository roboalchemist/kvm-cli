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

// hextileStream builds a single-rect Hextile update whose tile uses the given
// subencoding and payload.
func hextileStream(w, h int, tile []byte) []byte {
	var st bytes.Buffer
	st.Write(updateHeader(1))
	st.Write(testUpdateRect(0, 0, uint16(w), uint16(h), encHextile))
	st.Write(tile)
	return st.Bytes()
}

func TestHextileRawSubencoding(t *testing.T) {
	tile := []byte{hextileRaw}
	for i := 0; i < 4*2; i++ {
		tile = append(tile, pixelLE(0x0000ff, 4)...) // R=ff
	}
	srv := newScriptServer(t, hextileStream(4, 2, tile))
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
	if r, _, _, _ := decodePNG(t, data).At(0, 0).RGBA(); r>>8 != 0xff {
		t.Errorf("hextile RAW pixel R = %x", r>>8)
	}
}

func TestHextileBgOnlyAndFgSubrects(t *testing.T) {
	// Tile 1: bg only.
	tileA := []byte{hextileBgSpec}
	tileA = append(tileA, pixelLE(0x008000, 4)...) // G=0x80
	// Tile 2: fg + 1 non-coloured subrect at (0,0) size 1x1.
	tileB := []byte{hextileFgSpec | hextileAnySubrect}
	tileB = append(tileB, pixelLE(0x0000ff, 4)...) // fg R=0xff
	tileB = append(tileB, 1)                       // one subrect
	tileB = append(tileB, 0x00, 0x00)              // x=0,y=0,w=1,h=1
	var st bytes.Buffer
	st.Write(updateHeader(2))
	st.Write(testUpdateRect(0, 0, 4, 1, encHextile))
	st.Write(tileA)
	st.Write(testUpdateRect(4, 0, 4, 1, encHextile))
	st.Write(tileB)
	srv := newScriptServer(t, st.Bytes())
	srv.width = 8
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
	if _, g, _, _ := img.At(0, 0).RGBA(); g>>8 != 0x80 {
		t.Errorf("bg-only tile G = %x", g>>8)
	}
	if r, _, _, _ := img.At(4, 0).RGBA(); r>>8 != 0xff {
		t.Errorf("fg subrect R = %x", r>>8)
	}
}

func TestHugeDesktopName(t *testing.T) {
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
		io.ReadFull(conn, make([]byte, 1))
		binary.Write(conn, binary.BigEndian, uint32(0))
		io.ReadFull(conn, make([]byte, 1))
		hdr := make([]byte, 24)
		binary.BigEndian.PutUint16(hdr[0:], 4)
		binary.BigEndian.PutUint16(hdr[2:], 2)
		binary.BigEndian.PutUint32(hdr[20:], 1<<21) // > 1<<20
		conn.Write(hdr)
		time.Sleep(300 * time.Millisecond)
	}()
	if _, err := Dial(context.Background(), ln.Addr().String(), Options{Timeout: 3 * time.Second}); err == nil {
		t.Fatal("expected implausible name length error")
	}
}

func TestNotRFBServer(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte("HTTP/1.1 200\n"))
		time.Sleep(200 * time.Millisecond)
	}()
	if _, err := Dial(context.Background(), ln.Addr().String(), Options{Timeout: 3 * time.Second}); err == nil {
		t.Fatal("expected non-RFB error")
	}
}

// ardServer runs a fake ARD handshake with configurable rejection/keylen.
func ardServer(t *testing.T, keyLen int, reject bool) net.Listener {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		conn, e := ln.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		conn.Write([]byte("RFB 003.008\n"))
		io.ReadFull(conn, make([]byte, 12))
		conn.Write([]byte{1, secAppleDH})
		io.ReadFull(conn, make([]byte, 1))
		var hdr [4]byte
		binary.BigEndian.PutUint16(hdr[0:], 5)
		binary.BigEndian.PutUint16(hdr[2:], uint16(keyLen))
		conn.Write(hdr[:])
		if keyLen > 0 {
			conn.Write(bytes.Repeat([]byte{0x80}, keyLen))
			conn.Write(bytes.Repeat([]byte{0x11}, keyLen))
		}
		if reject {
			binary.Write(conn, binary.BigEndian, uint32(1))
			msg := []byte("denied")
			b := make([]byte, 4)
			binary.BigEndian.PutUint32(b, uint32(len(msg)))
			conn.Write(b)
			conn.Write(msg)
			time.Sleep(300 * time.Millisecond)
			return
		}
		io.ReadFull(conn, make([]byte, 128+keyLen))
		time.Sleep(300 * time.Millisecond)
	}()
	return ln
}

func TestArdZeroKeyLen(t *testing.T) {
	ln := ardServer(t, 0, false)
	defer ln.Close()
	if _, err := Dial(context.Background(), ln.Addr().String(), Options{
		Credentials: Credentials{Username: "u", Password: "p"}, Timeout: 3 * time.Second,
	}); err == nil {
		t.Fatal("expected implausible key length error")
	}
}

func TestArdRejectionWithReason(t *testing.T) {
	ln := ardServer(t, 256, true)
	defer ln.Close()
	if _, err := Dial(context.Background(), ln.Addr().String(), Options{
		Credentials: Credentials{Username: "u", Password: "p"}, Timeout: 3 * time.Second,
	}); err == nil {
		t.Fatal("expected ARD rejection error")
	}
}

func TestArdChosenWithoutUsername(t *testing.T) {
	// Server offers only ARD DH and the client supplies no username: the
	// fallback DRH branch (no-username case) must still be taken.
	srv := newRFBServer(t, secAppleDH, encRaw)
	defer srv.close()
	srv.user = ""
	srv.pass = "pw"
	srv.serve()
	c, err := Dial(context.Background(), srv.addr(), Options{
		Credentials: Credentials{Password: "pw"}, Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("dial ARD without username: %v", err)
	}
	c.Close()
}

func TestButtonNamesAndErrors(t *testing.T) {
	for name, want := range map[string]uint8{
		"left": 1, "1": 1, "middle": 2, "2": 2, "right": 4, "3": 4,
		"up": 8, "4": 8, "down": 16, "5": 16, "LEFT": 1,
	} {
		got, ok := buttonBit(name)
		if !ok || got != want {
			t.Errorf("buttonBit(%q) = %d,%v want %d", name, got, ok, want)
		}
	}
	if _, ok := buttonBit("bogus"); ok {
		t.Error("buttonBit(bogus) should be unknown")
	}

	// Click with an unknown button errors without touching the network.
	c := &Client{}
	if err := c.Click("bogus"); err == nil {
		t.Error("Click bogus should error")
	}
	// Close is idempotent and safe on a nil conn.
	if err := c.Close(); err != nil {
		t.Errorf("Close nil = %v", err)
	}
}

func TestReadConnFailedZeroLength(t *testing.T) {
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
		conn.Write([]byte{0}) // zero types
		binary.Write(conn, binary.BigEndian, uint32(0))
		time.Sleep(200 * time.Millisecond)
	}()
	if _, err := Dial(context.Background(), ln.Addr().String(), Options{Timeout: 3 * time.Second}); err == nil {
		t.Fatal("expected failure with zero-length reason")
	}
}

func TestCopyRectOutOfBounds(t *testing.T) {
	var st bytes.Buffer
	st.Write(updateHeader(1))
	st.Write(testUpdateRect(0, 0, 2, 2, encCopyRect))
	st.Write([]byte{0xff, 0xff, 0xff, 0xff}) // source far out of bounds
	srv := newScriptServer(t, st.Bytes())
	defer srv.ln.Close()
	srv.serve()
	c, err := Dial(context.Background(), srv.addr(), Options{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, _, _, err := c.Screenshot(context.Background()); err != nil {
		t.Fatalf("screenshot with OOB copyrect: %v", err)
	}
}

func TestScreenshotConnClosed(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		conn.Write([]byte("RFB 003.008\n"))
		io.ReadFull(conn, make([]byte, 12))
		conn.Write([]byte{1, secNone})
		io.ReadFull(conn, make([]byte, 1))
		binary.Write(conn, binary.BigEndian, uint32(0))
		io.ReadFull(conn, make([]byte, 1))
		hdr := make([]byte, 24)
		binary.BigEndian.PutUint16(hdr[0:], 4)
		binary.BigEndian.PutUint16(hdr[2:], 2)
		binary.BigEndian.PutUint32(hdr[20:], 4)
		conn.Write(hdr)
		conn.Write([]byte("fake"))
		conn.Close() // drop before answering the update request
	}()
	c, err := Dial(context.Background(), ln.Addr().String(), Options{Timeout: 3 * time.Second})
	if err != nil {
		// The server closed immediately; a dial-time write error (broken pipe)
		// is an equally valid observation of the dropped connection.
		return
	}
	defer c.Close()
	if _, _, _, err := c.Screenshot(context.Background()); err == nil {
		t.Fatal("expected error when server closes before update")
	}
}

func TestVersionParse(t *testing.T) {
	maj, min := parseVersion([]byte("RFB 003.008\n"))
	if maj != 3 || min != 8 {
		t.Errorf("parseVersion = %d.%d", maj, min)
	}
}
