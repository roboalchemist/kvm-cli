// Package vnc implements a native RFB (VNC) client for kvm-cli: it can connect
// to a VNC server, capture the framebuffer, and inject pointer/keyboard events,
// so a machine reachable over VNC can be driven with the same computer-use
// primitives used for the GL.iNet KVM.
//
// It supports the security types seen in practice: None (1), VNC DES (2), and
// the Apple/ARD Diffie-Hellman scheme (30) used by macOS Screen Sharing. The
// framebuffer is normalised to 32bpp little-endian true colour; RAW, CopyRect,
// Hextile and ZRLE encodings plus the DesktopSize pseudo-encoding are decoded.
package vnc

// keysym.go maps friendly key names and ASCII to X11 keysyms (RFC 6143 §7.5.4).

// X11 keysyms for special keys.
const (
	keyBackspace = 0xff08
	keyTab       = 0xff09
	keyReturn    = 0xff0d
	keyEscape    = 0xff1b
	keyInsert    = 0xff63
	keyDelete    = 0xffff
	keyHome      = 0xff50
	keyEnd       = 0xff57
	keyPageUp    = 0xff55
	keyPageDown  = 0xff56
	keyLeft      = 0xff51
	keyUp        = 0xff52
	keyRight     = 0xff53
	keyDown      = 0xff54
	keyShiftL    = 0xffe1
	keyShiftR    = 0xffe2
	keyControlL  = 0xffe3
	keyControlR  = 0xffe4
	keyCapsLock  = 0xffe5
	keyAltL      = 0xffe9
	keyAltR      = 0xffea
	keySuperL    = 0xffeb
	keySuperR    = 0xffec
	keyF1        = 0xffbe
	keyFBase     = 0xffbe // F1..F12 are contiguous
)

// namedKeys maps friendly names (case-insensitive) to keysyms.
var namedKeys = map[string]uint32{
	"backspace": keyBackspace, "tab": keyTab, "enter": keyReturn,
	"return": keyReturn, "escape": keyEscape, "esc": keyEscape,
	"insert": keyInsert, "delete": keyDelete, "del": keyDelete,
	"home": keyHome, "end": keyEnd, "pageup": keyPageUp, "pagedown": keyPageDown,
	"left": keyLeft, "up": keyUp, "right": keyRight, "down": keyDown,
	"space": 0x20,
	"shift": keyShiftL, "shift_l": keyShiftL, "shift_r": keyShiftR,
	"ctrl": keyControlL, "control": keyControlL, "ctrl_l": keyControlL, "ctrl_r": keyControlR,
	"alt": keyAltL, "alt_l": keyAltL, "alt_r": keyAltR,
	"meta": keySuperL, "super": keySuperL, "cmd": keySuperL, "command": keySuperL,
	"super_l": keySuperL, "super_r": keySuperR,
	"capslock": keyCapsLock, "caps_lock": keyCapsLock,
	"f1": keyFBase + 0, "f2": keyFBase + 1, "f3": keyFBase + 2, "f4": keyFBase + 3,
	"f5": keyFBase + 4, "f6": keyFBase + 5, "f7": keyFBase + 6, "f8": keyFBase + 7,
	"f9": keyFBase + 8, "f10": keyFBase + 9, "f11": keyFBase + 10, "f12": keyFBase + 11,
}

// KeySym resolves a friendly key name to an X11 keysym. A single printable
// character resolves to its Unicode/ASCII code point. It returns ok=false for
// unknown names.
func KeySym(name string) (uint32, bool) {
	if name == "" {
		return 0, false
	}
	// Single character (including a space).
	if rs := []rune(name); len(rs) == 1 && rs[0] >= 0x20 && rs[0] < 0x7f {
		return uint32(rs[0]), true
	}
	lower := lowerASCII(name)
	if ks, ok := namedKeys[lower]; ok {
		return ks, true
	}
	return 0, false
}

// textKeysyms converts each rune of s to a keysym appropriate for typing.
// Newlines map to Return and tabs to Tab; other runes use their code point.
func textKeysyms(s string) []uint32 {
	out := make([]uint32, 0, len(s))
	for _, r := range s {
		switch r {
		case '\n':
			out = append(out, keyReturn)
		case '\t':
			out = append(out, keyTab)
		default:
			out = append(out, uint32(r))
		}
	}
	return out
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}
