package vnc

import (
	"bytes"
	"crypto/aes"
	"crypto/des"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"

	"github.com/roboalchemist/kvm-cli/pkg/redact"
)

// RFB protocol version advertised by the client.
const clientVersion = "RFB 003.008\n"

// Security types.
const (
	secNone    = 1
	secVNCAuth = 2
	secAppleDH = 30 // Apple Remote Desktop / Screen Sharing
	secARDAuth = 30 // alias used by some servers
	secTight   = 16
)

// Client-to-server message types.
const (
	msgSetPixelFormat           = 0
	msgSetEncodings             = 2
	msgFramebufferUpdateRequest = 3
	msgKeyEvent                 = 4
	msgPointerEvent             = 5
	msgClientCutText            = 6
)

// Server-to-client message types.
const (
	s2cFramebufferUpdate = 0
	s2cSetColourMap      = 1
	s2cBell              = 2
	s2cServerCutText     = 3
)

// Encodings.
const (
	encRaw          int32 = 0
	encCopyRect     int32 = 1
	encHextile      int32 = 5
	encZRLE         int32 = 16
	encCursorPseudo int32 = -239
	encDesktopSize  int32 = -223
)

// pixelFormat is the 16-byte RFB PIXEL_FORMAT, normalised by the client to 32bpp
// little-endian true colour with RGB in the low bytes.
type pixelFormat struct {
	BitsPerPixel uint8
	Depth        uint8
	BigEndian    uint8
	TrueColor    uint8
	RedMax       uint16
	GreenMax     uint16
	BlueMax      uint16
	RedShift     uint8
	GreenShift   uint8
	BlueShift    uint8
	_            [3]byte
}

// requestedFormat is the format we always ask for: 32bpp, little-endian,
// 8:8:8 RGB in bytes 0..2 (so a decoded pixel's bytes are R,G,B,A).
func requestedFormat() pixelFormat {
	return pixelFormat{
		BitsPerPixel: 32, Depth: 24, BigEndian: 0, TrueColor: 1,
		RedMax: 255, GreenMax: 255, BlueMax: 255,
		RedShift: 0, GreenShift: 8, BlueShift: 16,
	}
}

// bypp returns bytes per pixel for the format.
func (p pixelFormat) bypp() int { return int((p.BitsPerPixel + 7) / 8) }

// readHandshake performs the version exchange and security handshake, returning
// the server's chosen desktop name after a successful authentication.
func readHandshake(conn io.ReadWriter, creds Credentials) (name string, width, height int, err error) {
	// Version exchange.
	if _, err := conn.Write([]byte(clientVersion)); err != nil {
		return "", 0, 0, redact.Error(fmt.Errorf("vnc: send version: %w", err))
	}
	verBuf := make([]byte, 12)
	if _, err := io.ReadFull(conn, verBuf); err != nil {
		return "", 0, 0, redact.Error(fmt.Errorf("vnc: read server version: %w", err))
	}
	if !bytes.HasPrefix(verBuf, []byte("RFB ")) {
		return "", 0, 0, fmt.Errorf("vnc: not an RFB server (got %q)", string(verBuf))
	}
	// The server version determines the security handshake shape. 3.3 uses a
	// single uint32; 3.7/3.8 use a list.
	major, minor := parseVersion(verBuf)
	if minor >= 7 {
		if err := security38(conn, creds); err != nil {
			return "", 0, 0, err
		}
	} else {
		if err := security33(conn, creds); err != nil {
			return "", 0, 0, err
		}
	}
	_ = major
	return serverInit(conn)
}

func parseVersion(b []byte) (int, int) {
	// b is "RFB 003.008\n"
	var major, minor int
	_, _ = fmt.Sscanf(string(b), "RFB %3d.%3d", &major, &minor)
	return major, minor
}

// security38 handles the 3.7/3.8 security type list.
func security38(conn io.ReadWriter, creds Credentials) error {
	var n uint8
	if err := binary.Read(conn, binary.BigEndian, &n); err != nil {
		return redact.Error(fmt.Errorf("vnc: read security count: %w", err))
	}
	if n == 0 {
		return readConnFailed(conn)
	}
	types := make([]byte, n)
	if _, err := io.ReadFull(conn, types); err != nil {
		return redact.Error(fmt.Errorf("vnc: read security types: %w", err))
	}
	return selectSecurity(conn, types, creds)
}

// security33 handles the legacy 3.3 security handshake, where the server
// dictates a single security type as a uint32 and the client does not send a
// selection byte (RFC 6143 §7.1.1, 3.3 wording).
func security33(conn io.ReadWriter, creds Credentials) error {
	var sec uint32
	if err := binary.Read(conn, binary.BigEndian, &sec); err != nil {
		return redact.Error(fmt.Errorf("vnc: read security type: %w", err))
	}
	switch sec {
	case 0:
		var n uint32
		if err := binary.Read(conn, binary.BigEndian, &n); err != nil {
			return redact.Error(fmt.Errorf("vnc: read failure reason length: %w", err))
		}
		msg := make([]byte, n)
		_, _ = io.ReadFull(conn, msg)
		return fmt.Errorf("vnc: connection refused: %s", string(msg))
	case secNone:
		return nil
	case secVNCAuth:
		return vncAuth(conn, creds)
	case secAppleDH:
		return ardAuthPending(conn, creds)
	default:
		return fmt.Errorf("vnc: unsupported 3.3 security type %d", sec)
	}
}

// selectSecurity chooses an auth type from those offered and runs it. None is
// preferred when offered; otherwise VNC DES; otherwise Apple ARD DH.
func selectSecurity(conn io.ReadWriter, offered []byte, creds Credentials) error {
	has := func(t byte) bool {
		for _, o := range offered {
			if o == t {
				return true
			}
		}
		return false
	}
	switch {
	case has(secNone):
		if err := writeU8(conn, secNone); err != nil {
			return err
		}
		// RFB 3.8 still sends a SecurityResult for the None type.
		return readAuthResult(conn)
	case creds.Username != "" && has(secAppleDH):
		// macOS Screen Sharing offers both VNC DES (2) and Apple DH (30). The
		// DES path uses a separate VNC-only password; when a username is given we
		// want the Apple DH scheme, which authenticates with the account.
		if err := writeU8(conn, secAppleDH); err != nil {
			return err
		}
		return ardAuthPending(conn, creds)
	case has(secVNCAuth):
		if err := writeU8(conn, secVNCAuth); err != nil {
			return err
		}
		return vncAuth(conn, creds)
	case has(secAppleDH):
		if err := writeU8(conn, secAppleDH); err != nil {
			return err
		}
		return ardAuthPending(conn, creds)
	default:
		return fmt.Errorf("vnc: no supported security type (offered %v)", offered)
	}
}

// readConnFailed reads a 3.8 connection-failed reason.
func readConnFailed(conn io.ReadWriter) error {
	var n uint32
	if err := binary.Read(conn, binary.BigEndian, &n); err != nil {
		return redact.Error(fmt.Errorf("vnc: read failure reason length: %w", err))
	}
	if n == 0 || n > 4096 {
		return fmt.Errorf("vnc: connection refused by server")
	}
	msg := make([]byte, n)
	_, _ = io.ReadFull(conn, msg)
	return fmt.Errorf("vnc: connection refused: %s", string(msg))
}

// vncAuth performs the VNC DES challenge/response (RFC 6143 §7.2.2).
func vncAuth(conn io.ReadWriter, creds Credentials) error {
	challenge := make([]byte, 16)
	if _, err := io.ReadFull(conn, challenge); err != nil {
		return redact.Error(fmt.Errorf("vnc: read challenge: %w", err))
	}
	if creds.Password == "" {
		return fmt.Errorf("vnc: server requires a password but none was provided")
	}
	key := desKey(creds.Password)
	block, err := des.NewCipher(key)
	if err != nil {
		return redact.Error(fmt.Errorf("vnc: des cipher: %w", err))
	}
	resp := make([]byte, 16)
	block.Encrypt(resp[0:8], challenge[0:8])
	block.Encrypt(resp[8:16], challenge[8:16])
	if _, err := conn.Write(resp); err != nil {
		return redact.Error(fmt.Errorf("vnc: send auth response: %w", err))
	}
	return readAuthResult(conn)
}

// desKey builds the VNC DES key: password padded/truncated to 8 ASCII bytes,
// then each byte bit-reversed (RFC 6143 §7.2.2).
func desKey(password string) []byte {
	pw := make([]byte, 8)
	copy(pw, []byte(password))
	out := make([]byte, 8)
	for i, b := range pw {
		var r byte
		for bit := 0; bit < 8; bit++ {
			if b&(1<<bit) != 0 {
				r |= 1 << (7 - bit)
			}
		}
		out[i] = r
	}
	return out
}

// ---- Apple Remote Desktop (ARD) DH auth (security type 30) ------------------
//
// The server sends generator g and key length L, then the modulus m
// (L bytes, big-endian) and its public key (L bytes, big-endian). The client
// computes its public key g^s mod m and the shared secret serverKey^s mod m,
// then AES-ECB-encrypts a 128-byte block (username padded to 64, password
// padded to 64) with MD5(shared) as the key, and sends ciphertext||clientKey.

func ardAuthPending(conn io.ReadWriter, creds Credentials) error {
	var g, keyLen uint16
	if err := binary.Read(conn, binary.BigEndian, &g); err != nil {
		return redact.Error(fmt.Errorf("vnc: read DH generator: %w", err))
	}
	if err := binary.Read(conn, binary.BigEndian, &keyLen); err != nil {
		return redact.Error(fmt.Errorf("vnc: read DH key length: %w", err))
	}
	if keyLen == 0 || keyLen > 4096 {
		return fmt.Errorf("vnc: implausible DH key length %d", keyLen)
	}
	modulus := make([]byte, keyLen)
	if _, err := io.ReadFull(conn, modulus); err != nil {
		return redact.Error(fmt.Errorf("vnc: read DH modulus: %w", err))
	}
	serverKey := make([]byte, keyLen)
	if _, err := io.ReadFull(conn, serverKey); err != nil {
		return redact.Error(fmt.Errorf("vnc: read DH server key: %w", err))
	}
	return ardAuthRespond(conn, g, modulus, serverKey, creds)
}

func ardAuthRespond(conn io.ReadWriter, g uint16, modulus, serverKey []byte, creds Credentials) error {
	m := new(big.Int).SetBytes(modulus)
	sk := new(big.Int).SetBytes(serverKey)

	// Random 512-byte private exponent.
	privBytes := make([]byte, 512)
	if _, err := rand.Read(privBytes); err != nil {
		return redact.Error(fmt.Errorf("vnc: dh randomness: %w", err))
	}
	s := new(big.Int).SetBytes(privBytes)

	pub := new(big.Int).Exp(big.NewInt(int64(g)), s, m)
	shared := new(big.Int).Exp(sk, s, m)

	keyDigest := md5.Sum(shared.Bytes())
	block, err := aes.NewCipher(keyDigest[:])
	if err != nil {
		return redact.Error(fmt.Errorf("vnc: aes cipher: %w", err))
	}

	plain := make([]byte, 128)
	copy(plain[0:64], []byte(creds.Username))
	copy(plain[64:128], []byte(creds.Password))
	ct := make([]byte, 128)
	block.Encrypt(ct[0:16], plain[0:16])
	block.Encrypt(ct[16:32], plain[16:32])
	block.Encrypt(ct[32:48], plain[32:48])
	block.Encrypt(ct[48:64], plain[48:64])
	block.Encrypt(ct[64:80], plain[64:80])
	block.Encrypt(ct[80:96], plain[80:96])
	block.Encrypt(ct[96:112], plain[96:112])
	block.Encrypt(ct[112:128], plain[112:128])

	if _, err := conn.Write(ct); err != nil {
		return redact.Error(fmt.Errorf("vnc: send ARD auth: %w", err))
	}
	// The server reads exactly keyLen bytes for the client public key, so
	// left-pad the big-endian representation to keyLen.
	pubBytes := pub.Bytes()
	if n := len(modulus); len(pubBytes) < n {
		padded := make([]byte, n)
		copy(padded[n-len(pubBytes):], pubBytes)
		pubBytes = padded
	}
	if _, err := conn.Write(pubBytes); err != nil {
		return redact.Error(fmt.Errorf("vnc: send DH client key: %w", err))
	}
	return readAuthResult(conn)
}

// readAuthResult reads the 4-byte auth result and, on failure under 3.8, the
// reason string.
func readAuthResult(conn io.ReadWriter) error {
	var result uint32
	if err := binary.Read(conn, binary.BigEndian, &result); err != nil {
		return redact.Error(fmt.Errorf("vnc: read auth result: %w", err))
	}
	if result == 0 {
		return nil
	}
	// 3.8 carries a reason string.
	var n uint32
	if err := binary.Read(conn, binary.BigEndian, &n); err == nil && n > 0 && n < 4096 {
		msg := make([]byte, n)
		_, _ = io.ReadFull(conn, msg)
		return fmt.Errorf("vnc: authentication failed: %s", string(msg))
	}
	return fmt.Errorf("vnc: authentication failed (result %d)", result)
}

// serverInit negotiates client init + server init and returns the desktop name.
func serverInit(conn io.ReadWriter) (string, int, int, error) {
	// ClientInit: shared flag = 1 (allow other clients).
	if err := writeU8(conn, 1); err != nil {
		return "", 0, 0, err
	}
	var width, height uint16
	pf := make([]byte, 16)
	var nameLen uint32
	if err := binary.Read(conn, binary.BigEndian, &width); err != nil {
		return "", 0, 0, redact.Error(fmt.Errorf("vnc: read width: %w", err))
	}
	if err := binary.Read(conn, binary.BigEndian, &height); err != nil {
		return "", 0, 0, redact.Error(fmt.Errorf("vnc: read height: %w", err))
	}
	if _, err := io.ReadFull(conn, pf); err != nil {
		return "", 0, 0, redact.Error(fmt.Errorf("vnc: read server pixel format: %w", err))
	}
	if err := binary.Read(conn, binary.BigEndian, &nameLen); err != nil {
		return "", 0, 0, redact.Error(fmt.Errorf("vnc: read name length: %w", err))
	}
	if nameLen > 1<<20 {
		return "", 0, 0, fmt.Errorf("vnc: implausible desktop name length %d", nameLen)
	}
	name := make([]byte, nameLen)
	if _, err := io.ReadFull(conn, name); err != nil {
		return "", 0, 0, redact.Error(fmt.Errorf("vnc: read desktop name: %w", err))
	}
	return string(name), int(width), int(height), nil
}

// setPixelFormat sends a SetPixelFormat for the normalised requested format.
func setPixelFormat(conn io.Writer) error {
	buf := make([]byte, 4+16)
	buf[0] = msgSetPixelFormat
	pf := requestedFormat()
	buf[4] = pf.BitsPerPixel
	buf[5] = pf.Depth
	buf[6] = pf.BigEndian
	buf[7] = pf.TrueColor
	binary.BigEndian.PutUint16(buf[8:], pf.RedMax)
	binary.BigEndian.PutUint16(buf[10:], pf.GreenMax)
	binary.BigEndian.PutUint16(buf[12:], pf.BlueMax)
	buf[14] = pf.RedShift
	buf[15] = pf.GreenShift
	buf[16] = pf.BlueShift
	_, err := conn.Write(buf)
	return err
}

// setEncodings advertises the encodings we can decode, most-preferred first.
func setEncodings(conn io.Writer, encs []int32) error {
	buf := make([]byte, 4+4*len(encs))
	buf[0] = msgSetEncodings
	binary.BigEndian.PutUint16(buf[2:], uint16(len(encs)))
	for i, e := range encs {
		binary.BigEndian.PutUint32(buf[4+4*i:], uint32(e))
	}
	_, err := conn.Write(buf)
	return err
}

// framebufferUpdateRequest asks for an incrementally (incremental=0 => full) updated region.
func framebufferUpdateRequest(conn io.Writer, incremental bool, x, y, w, h uint16) error {
	buf := make([]byte, 10)
	buf[0] = msgFramebufferUpdateRequest
	if incremental {
		buf[1] = 1
	}
	binary.BigEndian.PutUint16(buf[2:], x)
	binary.BigEndian.PutUint16(buf[4:], y)
	binary.BigEndian.PutUint16(buf[6:], w)
	binary.BigEndian.PutUint16(buf[8:], h)
	_, err := conn.Write(buf)
	return err
}

// pointerEvent sends a pointer position and button mask.
func pointerEvent(conn io.Writer, x, y uint16, mask uint8) error {
	buf := []byte{msgPointerEvent, mask, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(buf[2:], x)
	binary.BigEndian.PutUint16(buf[4:], y)
	_, err := conn.Write(buf)
	return err
}

// keyEvent sends a key event for an X11 keysym.
func keyEvent(conn io.Writer, down bool, keysym uint32) error {
	buf := make([]byte, 8)
	buf[0] = msgKeyEvent
	if down {
		buf[1] = 1
	}
	binary.BigEndian.PutUint32(buf[4:], keysym)
	_, err := conn.Write(buf)
	return err
}

func writeU8(w io.Writer, v byte) error {
	_, err := w.Write([]byte{v})
	return err
}
