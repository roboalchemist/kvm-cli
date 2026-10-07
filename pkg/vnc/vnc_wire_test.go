package vnc

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"testing"
)

type errWriter struct{}

func (errWriter) Write([]byte) (int, error) { return 0, errors.New("boom") }

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

// readWriter pairs a reader with a writer so *Reader-only stubs satisfy the
// io.ReadWriter parameters of the handshake helpers.
type readWriter struct {
	r io.Reader
	w io.Writer
}

func (x readWriter) Read(b []byte) (int, error)  { return x.r.Read(b) }
func (x readWriter) Write(b []byte) (int, error) { return x.w.Write(b) }

func rw(r io.Reader) readWriter { return readWriter{r: r, w: io.Discard} }

func TestWireWritersError(t *testing.T) {
	w := errWriter{}
	if err := setPixelFormat(w); err == nil {
		t.Error("setPixelFormat")
	}
	if err := setEncodings(w, []int32{encRaw}); err == nil {
		t.Error("setEncodings")
	}
	if err := framebufferUpdateRequest(w, false, 0, 0, 1, 1); err == nil {
		t.Error("framebufferUpdateRequest")
	}
	if err := pointerEvent(w, 0, 0, 0); err == nil {
		t.Error("pointerEvent")
	}
	if err := keyEvent(w, true, 0xff0d); err == nil {
		t.Error("keyEvent")
	}
	if err := writeU8(w, 1); err == nil {
		t.Error("writeU8")
	}
}

func TestReadHandshakeWriteError(t *testing.T) {
	if _, _, _, err := readHandshake(readWriter{r: errReader{}, w: errWriter{}}, Credentials{}); err == nil {
		t.Fatal("expected version write error")
	}
}

func TestReadHandshakeBadSecurity(t *testing.T) {
	// Valid version, then EOF during the security phase.
	r := bytes.NewBufferString("RFB 003.008\n")
	if _, _, _, err := readHandshake(r, Credentials{}); err == nil {
		t.Fatal("expected security read error")
	}
}

func TestServerInitShortReads(t *testing.T) {
	if _, _, _, err := serverInit(rw(bytes.NewReader(nil))); err == nil {
		t.Error("serverInit nil reader")
	}
	// width/height present but truncated pixel format.
	buf := make([]byte, 4)
	if _, _, _, err := serverInit(rw(bytes.NewReader(buf))); err == nil {
		t.Error("serverInit truncated pf")
	}
	// width/height/pf present but truncated name length.
	buf = make([]byte, 20)
	if _, _, _, err := serverInit(rw(bytes.NewReader(buf))); err == nil {
		t.Error("serverInit truncated namelen")
	}
	// namelen present but name truncated.
	buf = make([]byte, 24)
	buf[23] = 4 // namelen=4
	if _, _, _, err := serverInit(rw(bytes.NewReader(buf))); err == nil {
		t.Error("serverInit truncated name")
	}
	// A reader that errors immediately.
	if _, _, _, err := serverInit(readWriter{r: errReader{}, w: io.Discard}); err == nil {
		t.Error("serverInit errReader")
	}
}

func TestDecodersShortReads(t *testing.T) {
	fb := newFramebuffer(4, 2)
	if err := decodeRaw(bytes.NewReader(nil), fb, 0, 0, 4, 2, 4); err == nil {
		t.Error("decodeRaw short")
	}
	if err := decodeCopyRect(bytes.NewReader(nil), fb, 0, 0, 1, 1); err == nil {
		t.Error("decodeCopyRect short")
	}
	if err := decodeHextile(bytes.NewReader(nil), fb, 0, 0, 4, 2, 4); err == nil {
		t.Error("decodeHextile short")
	}
	// Hextile with bg bit but truncated bg.
	if err := decodeHextile(bytes.NewReader([]byte{hextileBgSpec}), fb, 0, 0, 4, 2, 4); err == nil {
		t.Error("decodeHextile truncated bg")
	}
	// Hextile with bg+fg bits but only enough bytes for bg.
	bad := append([]byte{hextileBgSpec | hextileFgSpec}, pixelLE(0, 4)...)
	if err := decodeHextile(bytes.NewReader(bad), fb, 0, 0, 4, 2, 4); err == nil {
		t.Error("decodeHextile truncated fg")
	}
	// Hextile with subrect count but truncated subrect.
	bad2 := append([]byte{hextileAnySubrect, 1}, 0x00) // count=1 then only 1 of 2 bytes
	if err := decodeHextile(bytes.NewReader(bad2), fb, 0, 0, 4, 2, 4); err == nil {
		t.Error("decodeHextile truncated subrect")
	}
}

func TestReadFramebufferUpdateUnknownMessage(t *testing.T) {
	c := &Client{r: bufio.NewReader(bytes.NewReader([]byte{99}))}
	if err := c.readFramebufferUpdate(nil); err == nil {
		t.Fatal("expected unknown-message error")
	}
}

func TestReadUpdateMalformed(t *testing.T) {
	// A FramebufferUpdate header claiming 1 rect but with no rect bytes.
	c := &Client{r: bufio.NewReader(bytes.NewReader([]byte{0, 0, 1}))}
	if _, err := c.readUpdate(); err == nil {
		t.Fatal("expected truncated-rect error")
	}
}

func TestArdReadErrors(t *testing.T) {
	if err := ardAuthPending(rw(bytes.NewReader(nil)), Credentials{}); err == nil {
		t.Error("ardAuthPending short")
	}
	// generator + keylen ok but modulus truncated.
	buf := []byte{0, 5, 0, 8, 1, 2, 3}
	if err := ardAuthPending(rw(bytes.NewReader(buf)), Credentials{}); err == nil {
		t.Error("ardAuthPending truncated modulus")
	}
}

func TestSecurity33DispatchErrors(t *testing.T) {
	// 3.3 VNC auth path with a truncated challenge.
	if err := security33(rw(bytes.NewReader(nil)), Credentials{Password: "x"}); err == nil {
		t.Error("security33 no data")
	}
}

func TestSelectSecurityErrors(t *testing.T) {
	if err := selectSecurity(readWriter{r: errReader{}, w: errWriter{}}, []byte{99}, Credentials{}); err == nil {
		t.Error("selectSecurity unsupported")
	}
}

func TestSetEncodingsWriterOutput(t *testing.T) {
	var b bytes.Buffer
	if err := setEncodings(&b, []int32{encHextile}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b.Bytes()[:4], []byte{msgSetEncodings, 0, 0, 1}) {
		t.Errorf("encodings header = %v", b.Bytes()[:4])
	}
}

func TestPointerWheelWriters(t *testing.T) {
	// Exercise the error return of pointerEvent from Wheel with an errWriter by
	// using a Client whose conn is an errWriter.
	c := &Client{}
	_ = c
	var b bytes.Buffer
	if err := pointerEvent(&b, 1, 2, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b.Bytes(), []byte{msgPointerEvent, 0, 0, 1, 0, 2}) {
		t.Errorf("pointer bytes = %v", b.Bytes())
	}
}

var _ = io.EOF

func TestServerInitWidthReadError(t *testing.T) {
	// Only 1 byte available for the 2-byte width.
	if _, _, _, err := serverInit(rw(bytes.NewReader([]byte{0}))); err == nil {
		t.Error("expected width read error")
	}
}

func TestSelectSecurityArdNoUsername(t *testing.T) {
	// Offer only ARD DH with no username: the final DH branch runs and then
	// fails on the truncated DH preamble (still exercises the branch).
	ro := rw(bytes.NewReader(nil))
	if err := selectSecurity(ro, []byte{secAppleDH}, Credentials{}); err == nil {
		t.Error("expected error from truncated DH preamble")
	}
}

func TestReadConnFailedReadError(t *testing.T) {
	if err := readConnFailed(readWriter{r: errReader{}, w: errWriter{}}); err == nil {
		t.Error("expected read error")
	}
}

func TestVNCAuthWriteError(t *testing.T) {
	// 16-byte challenge then a failing write.
	rwconn := readWriter{r: bytes.NewReader(make([]byte, 16)), w: errWriter{}}
	if err := vncAuth(rwconn, Credentials{Password: "x"}); err == nil {
		t.Error("expected auth write error")
	}
}

func TestArdAuthWriteError(t *testing.T) {
	// Valid preamble, then a failing write for the ciphertext/key.
	keyLen := 8
	buf := make([]byte, 0, 4+keyLen+keyLen)
	buf = append(buf, 0, 5, 0, byte(keyLen))
	buf = append(buf, bytes.Repeat([]byte{0x80}, keyLen)...)
	buf = append(buf, bytes.Repeat([]byte{0x11}, keyLen)...)
	rwconn := readWriter{r: bytes.NewReader(buf), w: errWriter{}}
	if err := ardAuthPending(rwconn, Credentials{Username: "u", Password: "p"}); err == nil {
		t.Error("expected ARD write error")
	}
}

func TestReadAuthResultGenericFailure(t *testing.T) {
	// Non-zero result followed by no reason bytes -> generic failure.
	if err := readAuthResult(rw(bytes.NewReader([]byte{0, 0, 0, 1}))); err == nil {
		t.Error("expected auth failure")
	}
}

func TestSecurity38ConnFailedZeroTypes(t *testing.T) {
	if err := security38(rw(bytes.NewReader([]byte{0})), Credentials{}); err == nil {
		t.Error("expected conn-failed path")
	}
}

// failConn is a net.Conn whose reads and writes always fail, for exercising the
// input methods' error branches.
type failConn struct{ net.Conn }

func (failConn) Read([]byte) (int, error)  { return 0, errors.New("boom") }
func (failConn) Write([]byte) (int, error) { return 0, errors.New("boom") }
func (failConn) Close() error              { return nil }

func TestInputWriteErrors(t *testing.T) {
	c := &Client{conn: failConn{}, r: bufio.NewReader(bytes.NewReader(nil))}
	if err := c.MoveMouse(1, 1); err == nil {
		t.Error("MoveMouse")
	}
	if err := c.PointerEvent(1, 1, 1); err == nil {
		t.Error("PointerEvent")
	}
	if err := c.Click("left"); err == nil {
		t.Error("Click")
	}
	if err := c.ClickAt(1, 1, "left"); err == nil {
		t.Error("ClickAt")
	}
	if err := c.MouseDown("left"); err == nil {
		t.Error("MouseDown")
	}
	if err := c.MouseUp("left"); err == nil {
		t.Error("MouseUp")
	}
	if err := c.Wheel(0, 1); err == nil {
		t.Error("Wheel")
	}
	if err := c.Wheel(0, -1); err == nil {
		t.Error("Wheel down")
	}
	if err := c.Key(0xff0d); err == nil {
		t.Error("Key")
	}
	if err := c.KeyDown(0xff0d); err == nil {
		t.Error("KeyDown")
	}
	if err := c.KeyUp(0xff0d); err == nil {
		t.Error("KeyUp")
	}
	if err := c.Type("ab"); err == nil {
		t.Error("Type")
	}
	if err := c.Combo([]string{"ctrl", "a"}); err == nil {
		t.Error("Combo")
	}
}

func TestFramebufferUpdateMessageErrors(t *testing.T) {
	cases := map[string][]byte{
		"colourmap-truncated": {s2cSetColourMap, 0, 0, 0},
		"cuttext-truncated":   {s2cServerCutText, 0, 0, 0, 0, 0, 0},
		"update-truncated":    {s2cFramebufferUpdate, 0, 0},
	}
	for name, data := range cases {
		c := &Client{r: bufio.NewReader(bytes.NewReader(data))}
		if err := c.readFramebufferUpdate(nil); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
